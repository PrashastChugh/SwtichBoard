package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const endedKeep = 15 * 60 // seconds a closed session stays listed
const maxField = 20000    // trim huge tool inputs before sending to the UI

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func newID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// UI is implemented by the native macOS interface when it's running.
type UI interface {
	Update(state []byte)
	Toast(kind, title, body, sessionID string)
}

type Session struct {
	ID               string         `json:"id"`
	Cwd              string         `json:"cwd"`
	Project          string         `json:"project"`
	Status           string         `json:"status"` // working, needs_permission, needs_input, idle, error, ended
	Activity         string         `json:"activity"`
	LastPrompt       string         `json:"last_prompt"`
	Started          float64        `json:"started"`
	Updated          float64        `json:"updated"`
	Tokens           map[string]any `json:"tokens"`
	Error            string         `json:"error"`
	CanType          bool           `json:"can_type"`        // Switchboard can type into its terminal (for /compact)
	CanTypeReason    string         `json:"can_type_reason"` // why not, when it's a more specific reason than "unsupported terminal"
	Title            string         `json:"title"`           // what to show: your name for it, else Claude's title
	Name             string         `json:"name"`            // your own name (kept in names.json)
	claudeTitle      string
	Env              map[string]string `json:"-"`
	compactSuggested bool              // already nudged "good time to compact" this time above threshold
}

type Pending struct {
	ID       string
	Event    map[string]any
	Created  float64
	Kind     string // permission or question
	done     chan struct{}
	once     sync.Once
	Decision string // allow, deny, answer, "" (= let the terminal handle it)
	Message  string
	Answers  map[string]string
}

func (p *Pending) Resolve(decision, message string, answers map[string]string) {
	p.once.Do(func() {
		p.Decision, p.Message, p.Answers = decision, message, answers
		close(p.done)
	})
}

func trimValue(v any) any {
	switch t := v.(type) {
	case string:
		if len(t) > maxField {
			return t[:maxField] + "\n... (truncated)"
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = trimValue(x)
		}
		return out
	case []any:
		if len(t) > 200 {
			t = t[:200]
		}
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = trimValue(x)
		}
		return out
	}
	return v
}

func (p *Pending) Public() map[string]any {
	tool := str(p.Event, "tool_name")
	ti := obj(p.Event, "tool_input")
	if ti == nil {
		ti = map[string]any{}
	}
	return map[string]any{
		"id": p.ID, "session_id": str(p.Event, "session_id"), "cwd": str(p.Event, "cwd"),
		"project": projectName(str(p.Event, "cwd")), "tool": tool, "subject": subjectOf(tool, ti),
		"tool_input": trimValue(ti), "created": p.Created, "kind": p.Kind,
	}
}

type LogEntry struct {
	T       float64 `json:"t"`
	Session string  `json:"session"`
	Project string  `json:"project"`
	Kind    string  `json:"kind"`
	Text    string  `json:"text"`
}

type Store struct {
	mu        sync.Mutex
	cfg       *Config
	sessions  map[string]*Session
	meters    map[string]*TranscriptMeter
	pending   []*Pending
	log       []LogEntry
	Rules     *Rules
	clients   map[chan []byte]bool
	changed   chan struct{}
	lastAlert map[string]float64
	names     map[string]string // session id -> your own name for it
	windows   map[string]int    // session id -> context window size Claude Code reported
	ui        UI
}

func NewStore(cfg *Config) *Store {
	s := &Store{cfg: cfg, sessions: map[string]*Session{}, meters: map[string]*TranscriptMeter{},
		Rules: loadRules(), clients: map[chan []byte]bool{}, changed: make(chan struct{}, 1),
		lastAlert: map[string]float64{}, names: loadNames(), windows: map[string]int{}}
	go s.broadcaster()
	go s.watchTranscripts()
	return s
}

// watchTranscripts re-reads any transcript that grew. Hooks only fire on
// events, so an idle session (e.g. right after /compact) would otherwise keep
// showing its old context size until the next prompt.
func (s *Store) watchTranscripts() {
	for range time.Tick(3 * time.Second) {
		s.mu.Lock()
		var stale []string
		for sid, m := range s.meters {
			if x := s.sessions[sid]; x != nil && x.Status != "ended" && m.Grew() {
				stale = append(stale, sid)
			}
		}
		s.mu.Unlock()
		for _, sid := range stale {
			s.RefreshTokens(sid)
		}
	}
}

func loadNames() map[string]string {
	names := map[string]string{}
	if b, err := os.ReadFile(namesPath()); err == nil {
		_ = json.Unmarshal(b, &names)
	}
	return names
}

func (x *Session) refreshTitle() {
	x.Title = x.Name
	if x.Title == "" {
		x.Title = x.claudeTitle
	}
}

// SetName gives a session your own name; an empty name goes back to Claude's.
func (s *Store) SetName(sid, name string) bool {
	name = strings.TrimSpace(cleanLine(name, 80))
	s.mu.Lock()
	x := s.sessions[sid]
	if x == nil {
		s.mu.Unlock()
		return false
	}
	if name == "" {
		delete(s.names, sid)
	} else {
		s.names[sid] = name
	}
	x.Name = name
	x.refreshTitle()
	snap := make(map[string]string, len(s.names))
	for k, v := range s.names {
		snap[k] = v
	}
	s.mu.Unlock()
	_ = writeJSONAtomic(namesPath(), snap)
	s.Touch()
	return true
}

// ----- live updates ------------------------------------------------------

func (s *Store) SetUI(ui UI) {
	s.mu.Lock()
	s.ui = ui
	s.mu.Unlock()
	s.Touch()
}

func (s *Store) Touch() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *Store) Subscribe() chan []byte {
	ch := make(chan []byte, 50)
	s.mu.Lock()
	s.clients[ch] = true
	s.mu.Unlock()
	s.Touch()
	return ch
}

func (s *Store) Unsubscribe(ch chan []byte) {
	s.mu.Lock()
	delete(s.clients, ch)
	s.mu.Unlock()
	s.Touch()
}

// HasClients: is a dashboard tab open?
func (s *Store) HasClients() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients) > 0
}

// BackgroundOK: can you answer without the dashboard (Switchboard's own popups)?
func (s *Store) BackgroundOK() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ui != nil
}

func (s *Store) broadcaster() {
	for range s.changed {
		time.Sleep(120 * time.Millisecond) // coalesce bursts of events
		select {
		case <-s.changed:
		default:
		}
		data, _ := json.Marshal(s.Snapshot())
		s.mu.Lock()
		clients := make([]chan []byte, 0, len(s.clients))
		for ch := range s.clients {
			clients = append(clients, ch)
		}
		ui := s.ui
		s.mu.Unlock()
		for _, ch := range clients {
			select {
			case ch <- data:
			default:
			}
		}
		if ui != nil {
			b, _ := json.Marshal(s.UIState())
			ui.Update(b)
		}
	}
}

func (s *Store) Snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := now()
	list := make([]*Session, 0, len(s.sessions))
	for id, x := range s.sessions {
		if x.Status == "ended" && t-x.Updated > endedKeep {
			delete(s.sessions, id)
			delete(s.meters, id)
			continue
		}
		list = append(list, x)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Started < list[j].Started })
	pend := make([]map[string]any, 0, len(s.pending))
	for _, p := range s.pending {
		pend = append(pend, p.Public())
	}
	logs := append([]LogEntry{}, s.log...) // never null in JSON
	if len(logs) > 80 {
		logs = logs[len(logs)-80:]
	}
	return map[string]any{
		"now": t, "sessions": list, "pending": pend, "log": logs, "rules": s.Rules.List(),
		"clients": len(s.clients), "popups": s.ui != nil, "context_window": s.cfg.ContextWindow,
	}
}

// ----- helpers (call with s.mu held) --------------------------------------

func (s *Store) session(ev map[string]any) *Session {
	sid := str(ev, "session_id")
	if sid == "" {
		sid = "unknown"
	}
	t := now()
	x := s.sessions[sid]
	if x == nil {
		cwd := str(ev, "cwd")
		x = &Session{ID: sid, Cwd: cwd, Project: projectName(cwd), Status: "idle", Activity: "Session started",
			Started: t, Env: map[string]string{}, Name: s.names[sid]}
		x.refreshTitle()
		s.sessions[sid] = x
	}
	x.Updated = t
	for k, v := range envOf(ev) {
		x.Env[k] = v
	}
	x.CanType, x.CanTypeReason = canTypeReason(withCwd(x.Env, x.Cwd), s.liveEnvsLocked(sid))
	if tp := str(ev, "transcript_path"); tp != "" && s.meters[sid] == nil {
		s.meters[sid] = newMeter(tp)
	}
	return x
}

// liveEnvsLocked returns every other live (non-ended) session's terminal
// env - caller must hold s.mu. Used to detect an ambiguous compact target
// (e.g. two sessions both in JetBrains, which can't be told apart).
func (s *Store) liveEnvsLocked(excludeSid string) []map[string]string {
	var out []map[string]string
	for id, x := range s.sessions {
		if id == excludeSid || x.Status == "ended" {
			continue
		}
		out = append(out, withCwd(x.Env, x.Cwd))
	}
	return out
}

// withCwd copies env and adds the session folder as SB_CWD, which the
// JetBrains window matching needs.
func withCwd(env map[string]string, cwd string) map[string]string {
	out := make(map[string]string, len(env)+1)
	for k, v := range env {
		out[k] = v
	}
	out["SB_CWD"] = cwd
	return out
}

// LiveEnvs is liveEnvsLocked for callers that don't already hold s.mu.
func (s *Store) LiveEnvs(excludeSid string) []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.liveEnvsLocked(excludeSid)
}

// KnownProjectDirs: every project folder Switchboard currently knows about,
// from live sessions and pending requests - the same universe the Rules and
// Skills dialogs use for their project dropdowns, so skill listing doesn't
// need to scan the filesystem for folders it has no other reason to know.
func (s *Store) KnownProjectDirs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, x := range s.sessions {
		if x.Cwd != "" && !seen[x.Cwd] {
			seen[x.Cwd] = true
			out = append(out, x.Cwd)
		}
	}
	for _, p := range s.pending {
		cwd := str(p.Event, "cwd")
		if cwd != "" && !seen[cwd] {
			seen[cwd] = true
			out = append(out, cwd)
		}
	}
	return out
}

func (s *Store) addLog(x *Session, kind, text string) {
	s.log = append(s.log, LogEntry{T: now(), Session: x.ID, Project: x.Project, Kind: kind, Text: text})
	if len(s.log) > 300 {
		s.log = s.log[len(s.log)-300:]
	}
}

func (s *Store) hasPending(sid string) bool {
	for _, p := range s.pending {
		if str(p.Event, "session_id") == sid {
			return true
		}
	}
	return false
}

// alert shows a short card (popups running) or a system notification.
func (s *Store) alert(x *Session, kind, title, body string) {
	key := x.ID + "/" + kind
	t := now()
	if t-s.lastAlert[key] < 8 {
		return
	}
	s.lastAlert[key] = t
	if s.ui != nil {
		go s.ui.Toast(kind, title, body, x.ID)
		return
	}
	if s.cfg.OSNotifications {
		go notify(title, body)
	}
}

// compactThreshold: context fraction at which Switchboard nudges "good time
// to compact" - matches the dashboard's own 70% highlight threshold.
const compactThreshold = 0.70

func (s *Store) RefreshTokens(sid string) {
	go func() {
		s.mu.Lock()
		m := s.meters[sid]
		win := s.cfg.ContextWindow
		reported := s.windows[sid]
		s.mu.Unlock()
		if m == nil {
			return
		}
		m.Update() // only this goroutine family touches the meter's file state
		sum := m.Summary()
		title := m.Title()
		if model, _ := sum["model"].(string); model != "" {
			if w := contextWindowFor(model); w > 0 {
				win = w
			}
		}
		src := "default"
		if win != s.cfg.ContextWindow {
			src = "model"
		}
		if reported > 0 {
			win, src = reported, "statusline"
		}
		sum["context_window"] = win
		sum["window_source"] = src

		s.mu.Lock()
		x := s.sessions[sid]
		var alertPct int
		if x != nil {
			x.Tokens = sum
			x.claudeTitle = title
			x.refreshTitle()
			if ctx, ok := sum["context"].(int64); ok && win > 0 {
				pct := float64(ctx) / float64(win)
				switch {
				case pct >= compactThreshold && !x.compactSuggested:
					x.compactSuggested = true
					alertPct = int(pct * 100)
					s.addLog(x, "note", fmt.Sprintf("Good time to compact (~%d%% of context used)", alertPct))
				case pct < compactThreshold:
					x.compactSuggested = false
				}
			}
		}
		s.mu.Unlock()
		if alertPct > 0 {
			s.alert(x, "compact", x.Project+" - good time to compact",
				fmt.Sprintf("Context is about %d%% full. Wrap up this task, then compact.", alertPct))
		}
		s.Touch()
	}()
}

// SetWindow records the context window size Claude Code reported for a
// session through its status line - the one exact source for it.
func (s *Store) SetWindow(sid string, size int) {
	if sid == "" || size <= 0 {
		return
	}
	s.mu.Lock()
	changed := s.windows[sid] != size
	s.windows[sid] = size
	s.mu.Unlock()
	if changed {
		s.RefreshTokens(sid)
	}
}

// releaseSession frees held requests made obsolete by progress in the terminal.
func (s *Store) releaseSession(sid, toolUseID string) {
	for _, p := range s.pending {
		if str(p.Event, "session_id") != sid {
			continue
		}
		if toolUseID == "" || str(p.Event, "tool_use_id") == toolUseID {
			p.Resolve("", "", nil)
		}
	}
}

// releaseSameCall frees a held card whose request has no tool_use_id (Claude
// Code's permission requests may not carry one) once the same tool call
// finishes - i.e. you answered it in the terminal instead.
func (s *Store) releaseSameCall(sid string, done map[string]any) {
	want, _ := json.Marshal(obj(done, "tool_input"))
	for _, p := range s.pending {
		if str(p.Event, "session_id") != sid || str(p.Event, "tool_use_id") != "" ||
			str(p.Event, "tool_name") != str(done, "tool_name") {
			continue
		}
		if got, _ := json.Marshal(obj(p.Event, "tool_input")); string(got) == string(want) {
			p.Resolve("", "", nil)
		}
	}
}

func activityFor(tool string, ti map[string]any) string {
	subject := subjectOf(tool, ti)
	switch {
	case tool == "Read":
		return "Reading " + baseName(subject)
	case tool == "Edit" || tool == "MultiEdit" || tool == "NotebookEdit":
		return "Editing " + baseName(subject)
	case tool == "Write":
		return "Writing " + baseName(subject)
	case shellTools[tool]:
		if subject == "" {
			return "Running a command"
		}
		return "Running " + clip(firstLine(subject), 120)
	case tool == "Grep" || tool == "Glob":
		return "Searching " + clip(subject, 80)
	case tool == "WebFetch":
		return "Fetching " + clip(subject, 100)
	case tool == "WebSearch":
		return "Searching the web for " + clip(subject, 80)
	case tool == "Task" || tool == "Agent":
		return "Delegating: " + clip(subject, 100)
	case tool == "AskUserQuestion":
		return "Asking you a question"
	case tool == "":
		return "Using a tool"
	}
	return "Using " + tool
}

// ----- hook events ---------------------------------------------------------

func (s *Store) HandleEvent(ev map[string]any) {
	name := str(ev, "hook_event_name")
	s.mu.Lock()
	x := s.session(ev)
	sid := x.ID
	tool, ti := str(ev, "tool_name"), obj(ev, "tool_input")
	refresh := false
	switch name {
	case "SessionStart":
		src := str(ev, "source")
		if src == "" {
			src = "startup"
		}
		x.Status, x.Error = "idle", ""
		x.Activity = "Ready"
		if src == "compact" {
			x.Activity = "Context compacted. Ready."
		}
		s.addLog(x, "start", fmt.Sprintf("Session started (%s)", src))
		refresh = true
	case "UserPromptSubmit":
		s.releaseSession(sid, "")
		prompt := strings.TrimSpace(str(ev, "prompt"))
		x.Status, x.Activity, x.LastPrompt, x.Error = "working", "Thinking", clip(prompt, 300), ""
		s.addLog(x, "prompt", "You: "+clip(prompt, 140))
	case "PreToolUse":
		if !s.hasPending(sid) {
			x.Status = "working"
		}
		x.Activity = activityFor(tool, ti)
	case "PostToolUse", "PostToolUseFailure":
		if id := str(ev, "tool_use_id"); id != "" {
			s.releaseSession(sid, id)
		}
		s.releaseSameCall(sid, ev)
		if !s.hasPending(sid) {
			x.Status = "working"
		}
		if name == "PostToolUseFailure" {
			s.addLog(x, "fail", "Failed: "+activityFor(tool, ti))
		}
	case "Notification":
		ntype := str(ev, "notification_type")
		msg := str(ev, "message")
		if msg == "" {
			msg = "Claude needs your attention"
		}
		if ntype == "idle_prompt" {
			x.Status, x.Activity = "needs_input", "Waiting for your reply"
			s.alert(x, "input", x.Project+" is waiting for you", msg)
		} else if ntype == "permission_prompt" && !s.hasPending(sid) {
			x.Status, x.Activity = "needs_permission", "Asking permission in the terminal"
			s.alert(x, "perm", x.Project+" needs permission", msg)
		}
		s.addLog(x, "note", msg)
	case "PreCompact":
		trigger := str(ev, "trigger")
		x.Status, x.Activity = "working", "Compacting context"
		s.addLog(x, "compact", fmt.Sprintf("Compacting context (%s)", map[bool]string{true: "automatic", false: "manual"}[trigger == "auto"]))
	case "Stop":
		s.releaseSession(sid, "")
		x.Status, x.Activity = "idle", "Finished. Your turn."
		s.addLog(x, "stop", "Finished responding")
		if s.cfg.NotifyOnFinish {
			body := clip(x.LastPrompt, 120)
			if body == "" {
				body = "Your turn"
			}
			s.alert(x, "done", x.Project+" finished", body)
		}
		refresh = true
	case "StopFailure":
		s.releaseSession(sid, "")
		e := str(ev, "error_type")
		if e == "" {
			e = str(ev, "error")
		}
		if e == "" {
			e = "API error"
		}
		x.Status, x.Activity, x.Error = "error", "Stopped: "+e, e
		s.addLog(x, "fail", "Turn failed: "+e)
		s.alert(x, "error", x.Project+" stopped with an error", e)
	case "SubagentStart":
		at := str(ev, "agent_type")
		if at == "" {
			at = "agent"
		}
		x.Activity = "Running a subagent (" + at + ")"
	case "SubagentStop":
		x.Activity = "Subagent finished"
	case "SessionEnd":
		s.releaseSession(sid, "")
		x.Status, x.Activity = "ended", "Session closed"
		r := str(ev, "reason")
		if r == "" {
			r = "exit"
		}
		s.addLog(x, "end", "Session ended ("+r+")")
	}
	s.mu.Unlock()
	if refresh {
		s.RefreshTokens(sid)
	}
	s.Touch()
}

// MarkTerminalPrompt reflects a request that goes straight to the terminal.
func (s *Store) MarkTerminalPrompt(ev map[string]any, alert bool) {
	s.mu.Lock()
	x := s.session(ev)
	tool := str(ev, "tool_name")
	subject := subjectOf(tool, obj(ev, "tool_input"))
	if tool == "AskUserQuestion" {
		x.Status, x.Activity = "needs_input", "Asking you a question in the terminal"
		s.addLog(x, "perm", "Asks in terminal: "+clip(subject, 120))
		if alert {
			s.alert(x, "question", x.Project+" has a question", clip(subject, 200))
		}
	} else {
		x.Status, x.Activity = "needs_permission", "Asking permission in the terminal"
		s.addLog(x, "perm", fmt.Sprintf("Asks in terminal to use %s: %s", tool, clip(subject, 120)))
		if alert {
			s.alert(x, "perm", x.Project+" needs permission", fmt.Sprintf("%s: %s", tool, clip(subject, 160)))
		}
	}
	s.mu.Unlock()
	s.Touch()
}

// ----- the queue -----------------------------------------------------------

func (s *Store) AddPending(ev map[string]any) *Pending {
	kind := "permission"
	if str(ev, "tool_name") == "AskUserQuestion" {
		kind = "question"
	}
	p := &Pending{ID: newID(6), Event: ev, Created: now(), Kind: kind, done: make(chan struct{})}
	s.mu.Lock()
	x := s.session(ev)
	s.pending = append(s.pending, p)
	pub := p.Public()
	subject, tool := pub["subject"].(string), pub["tool"].(string)
	var title, body string
	if kind == "question" {
		x.Status, x.Activity = "needs_input", "Asking you a question"
		s.addLog(x, "perm", "Asks: "+clip(subject, 140))
		title, body = x.Project+" has a question", clip(subject, 200)
	} else {
		x.Status, x.Activity = "needs_permission", "Waiting for permission: "+tool
		s.addLog(x, "perm", fmt.Sprintf("Asks to use %s: %s", tool, clip(subject, 120)))
		title, body = x.Project+" needs permission", fmt.Sprintf("%s: %s", tool, clip(subject, 200))
	}
	popups := s.ui != nil
	s.mu.Unlock()
	if !popups && s.cfg.OSNotifications { // with popups, the card itself is the alert
		go notify(title, body)
	}
	s.RefreshTokens(x.ID)
	s.Touch()
	return p
}

func (s *Store) FinishPending(p *Pending) {
	s.mu.Lock()
	kept := s.pending[:0]
	for _, q := range s.pending {
		if q != p {
			kept = append(kept, q)
		}
	}
	s.pending = kept
	if x := s.sessions[str(p.Event, "session_id")]; x != nil {
		if !s.hasPending(x.ID) && (x.Status == "needs_permission" || x.Status == "needs_input") {
			switch p.Decision {
			case "allow":
				x.Status, x.Activity = "working", "Approved. Continuing."
			case "deny":
				x.Status, x.Activity = "working", "Denied. Claude is adjusting."
			case "answer":
				x.Status, x.Activity = "working", "Answered. Continuing."
			default:
				if p.Kind == "question" {
					x.Status, x.Activity = "needs_input", "Asking you a question in the terminal"
				} else {
					x.Status, x.Activity = "needs_permission", "Asking permission in the terminal"
				}
			}
		}
		label := map[string]string{"allow": "Allowed", "deny": "Denied", "answer": "Answered"}[p.Decision]
		if label == "" {
			label = "Sent to terminal"
		}
		subj, _ := p.Public()["subject"].(string)
		if subj == "" {
			subj = str(p.Event, "tool_name")
		}
		s.addLog(x, "decide", label+": "+clip(subj, 120))
	}
	s.mu.Unlock()
	s.Touch()
}

func (s *Store) find(id string) *Pending {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.pending {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (s *Store) Decide(id, behavior, message string) bool {
	p := s.find(id)
	if p == nil {
		return false
	}
	if behavior != "allow" && behavior != "deny" {
		behavior = ""
	}
	if behavior == "deny" && message == "" {
		message = "The user denied this from Switchboard."
	}
	p.Resolve(behavior, message, nil)
	return true
}

// Answer a multiple-choice question: {question text: label or [labels]}.
func (s *Store) Answer(id string, answers map[string]any) bool {
	p := s.find(id)
	if p == nil || p.Kind != "question" || len(answers) == 0 {
		return false
	}
	clean := map[string]string{}
	for q, a := range answers {
		switch v := a.(type) {
		case string:
			clean[q] = v
		case []any:
			parts := make([]string, 0, len(v))
			for _, x := range v {
				parts = append(parts, fmt.Sprint(x))
			}
			clean[q] = strings.Join(parts, ", ")
		default:
			clean[q] = fmt.Sprint(v)
		}
		if clean[q] == "" {
			return false
		}
	}
	p.Resolve("answer", "", clean)
	return true
}

// QuickAllowRule: the native card's one-click "Always allow" - an exact-match
// allow rule for this tool+subject+project, then the request is allowed.
// Narrower than the dashboard's "Always allow..." dialog (no wildcard, no
// editing) by design: a single click shouldn't be able to authorize more
// than the exact thing just asked for.
func (s *Store) QuickAllowRule(id string) bool {
	p := s.find(id)
	if p == nil {
		return false
	}
	tool, cwd := str(p.Event, "tool_name"), str(p.Event, "cwd")
	subject := subjectOf(tool, obj(p.Event, "tool_input"))
	if subject == "" {
		return s.Decide(id, "allow", "") // nothing to scope a rule to: just allow
	}
	if _, err := s.Rules.Add("allow", tool, subject, cwd); err != nil {
		return false
	}
	s.ApplyRulesToPending()
	return true
}

func (s *Store) NoteRule(ev map[string]any, rule *Rule) {
	s.mu.Lock()
	x := s.session(ev)
	verb := "Auto-allowed"
	if rule.Action == "deny" {
		verb = "Auto-denied"
	} else {
		x.Status = "working"
	}
	s.addLog(x, "rule", verb+" by rule: "+clip(subjectOf(str(ev, "tool_name"), obj(ev, "tool_input")), 120))
	s.mu.Unlock()
	s.Touch()
}

func (s *Store) ApplyRulesToPending() {
	s.mu.Lock()
	items := append([]*Pending{}, s.pending...)
	s.mu.Unlock()
	for _, p := range items {
		if p.Kind != "permission" {
			continue
		}
		if rule := s.Rules.Match(p.Event); rule != nil {
			msg := ""
			if rule.Action == "deny" {
				msg = "Blocked by a Switchboard rule."
			}
			p.Resolve(rule.Action, msg, nil)
		}
	}
}

func (s *Store) SessionEnv(sid string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	if x := s.sessions[sid]; x != nil {
		for k, v := range x.Env {
			out[k] = v
		}
	}
	return out
}

func (s *Store) SessionCopy(sid string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if x := s.sessions[sid]; x != nil {
		c := *x
		return c, true
	}
	return Session{}, false
}

func (s *Store) LogFor(sid, kind, text string) {
	s.mu.Lock()
	if x := s.sessions[sid]; x != nil {
		s.addLog(x, kind, text)
	}
	s.mu.Unlock()
	s.Touch()
}
