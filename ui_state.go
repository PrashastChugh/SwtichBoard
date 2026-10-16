package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// What the native macOS interface draws. Go prepares all the text; the
// Objective-C side only lays it out, so the logic stays here and testable.

type uiOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type uiQuestion struct {
	Question string     `json:"question"`
	Multi    bool       `json:"multi"`
	Options  []uiOption `json:"options"`
}

type uiPending struct {
	ID        string       `json:"id"`
	SessionID string       `json:"session_id"`
	Kind      string       `json:"kind"`
	Project   string       `json:"project"`
	Verb      string       `json:"verb"`
	Code      string       `json:"code"`
	Created   float64      `json:"created"`
	Questions []uiQuestion `json:"questions"`
}

type uiSession struct {
	ID       string `json:"id"`
	Project  string `json:"project"`
	Status   string `json:"status"`
	Activity string `json:"activity"`
}

type uiStateT struct {
	Sessions   []uiSession `json:"sessions"`
	Pending    []uiPending `json:"pending"`
	WidgetText string      `json:"widget_text"`
	Count      int         `json:"count"`
}

var statusDot = map[string]string{"needs_permission": "🟠", "needs_input": "🟠", "working": "🟢", "idle": "⚪️", "error": "🔴"}

func linesOf(s string, n int) []string {
	parts := strings.Split(s, "\n")
	if len(parts) > n {
		parts = parts[:n]
	}
	return parts
}

// describe: a short "wants to ..." line and the text shown in the code box.
func describe(tool string, ti map[string]any, subject string) (string, string) {
	switch {
	case shellTools[tool]:
		return "wants to run a command", "$ " + subject
	case tool == "Edit":
		var out []string
		for _, l := range linesOf(str(ti, "old_string"), 3) {
			out = append(out, "- "+l)
		}
		for _, l := range linesOf(str(ti, "new_string"), 3) {
			out = append(out, "+ "+l)
		}
		code := strings.Join(out, "\n")
		if code == "" {
			code = subject
		}
		return "wants to edit " + baseName(subject), code
	case tool == "MultiEdit":
		n := 0
		if e, ok := ti["edits"].([]any); ok {
			n = len(e)
		}
		return fmt.Sprintf("wants to make %d edits in %s", n, baseName(subject)), subject
	case tool == "Write":
		code := strings.Join(linesOf(str(ti, "content"), 5), "\n")
		if code == "" {
			code = subject
		}
		return "wants to create " + baseName(subject), code
	case tool == "WebFetch":
		return "wants to open a web page", subject
	case subject != "":
		return "wants to use " + tool, subject
	}
	var parts []string
	for k, v := range ti {
		parts = append(parts, fmt.Sprintf("%s: %s", k, clip(fmt.Sprint(v), 60)))
		if len(parts) == 4 {
			break
		}
	}
	return "wants to use " + tool, strings.Join(parts, ", ")
}

func questionsOf(ti map[string]any) []uiQuestion {
	var out []uiQuestion
	qs, _ := ti["questions"].([]any)
	for _, raw := range qs {
		q, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		uq := uiQuestion{Question: str(q, "question")}
		uq.Multi, _ = q["multiSelect"].(bool)
		opts, _ := q["options"].([]any)
		for _, o := range opts {
			switch v := o.(type) {
			case map[string]any:
				uq.Options = append(uq.Options, uiOption{Label: str(v, "label"), Description: str(v, "description")})
			case string:
				uq.Options = append(uq.Options, uiOption{Label: v})
			}
			if len(uq.Options) == 6 {
				break
			}
		}
		out = append(out, uq)
		if len(out) == 4 {
			break
		}
	}
	return out
}

func (s *Store) UIState() uiStateT {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := uiStateT{Sessions: []uiSession{}, Pending: []uiPending{}}
	var dots strings.Builder
	waiting := 0
	live := make([]*Session, 0, len(s.sessions))
	for _, x := range s.sessions {
		if x.Status != "ended" {
			live = append(live, x)
		}
	}
	sortSessions(live)
	for i, x := range live {
		st.Sessions = append(st.Sessions, uiSession{ID: x.ID, Project: x.Project, Status: x.Status, Activity: x.Activity})
		if i < 10 {
			d := statusDot[x.Status]
			if d == "" {
				d = "⚪️"
			}
			dots.WriteString(d)
		}
		if x.Status == "needs_permission" || x.Status == "needs_input" {
			waiting++
		}
	}
	for _, p := range s.pending {
		tool := str(p.Event, "tool_name")
		ti := obj(p.Event, "tool_input")
		subject := subjectOf(tool, ti)
		up := uiPending{ID: p.ID, SessionID: str(p.Event, "session_id"), Kind: p.Kind,
			Project: projectName(str(p.Event, "cwd")), Created: p.Created}
		up.Questions = []uiQuestion{}
		if p.Kind == "question" {
			up.Questions = append(up.Questions, questionsOf(ti)...)
		} else {
			up.Verb, up.Code = describe(tool, ti, subject)
		}
		st.Pending = append(st.Pending, up)
	}
	text := "☎︎  " + dots.String()
	if dots.Len() == 0 {
		text = "☎︎  no sessions"
	}
	if n := len(st.Pending); n > 0 {
		text += fmt.Sprintf("   %d waiting", n)
	}
	st.WidgetText = text
	st.Count = len(st.Pending)
	if waiting > st.Count {
		st.Count = waiting
	}
	return st
}

func sortSessions(list []*Session) {
	order := map[string]int{"needs_permission": 0, "needs_input": 1, "error": 2, "idle": 3, "working": 4}
	for i := 1; i < len(list); i++ {
		for j := i; j > 0; j-- {
			a, b := list[j-1], list[j]
			oa, ob := order[a.Status], order[b.Status]
			if oa < ob || (oa == ob && a.Started <= b.Started) {
				break
			}
			list[j-1], list[j] = b, a
		}
	}
}

// handleUIAction runs a button press from the native interface.
func (srv *Server) handleUIAction(kind, id, payload string) {
	st := srv.store
	switch kind {
	case "allow", "deny":
		st.Decide(id, kind, "")
	case "always_allow":
		st.QuickAllowRule(id)
	case "open_terminal": // jump to the terminal and show the prompt there
		showTerminal(st.SessionEnv(payload))
		st.Decide(id, "terminal", "")
	case "answer":
		var m map[string]any
		if json.Unmarshal([]byte(payload), &m) == nil {
			st.Answer(id, m)
		}
	case "show":
		showTerminal(st.SessionEnv(id))
	case "compact_now": // the compact-nudge toast's own button
		if ok, msg := srv.doCompact(id, ""); !ok {
			st.LogFor(id, "compact", "Couldn't compact: "+msg)
			// The toast fires on click with no response UI of its own - without
			// this, a failed attempt (e.g. Claude started working again before
			// you clicked) looks like the button silently did nothing.
			if x, found := st.SessionCopy(id); found {
				st.alert(&x, "compact-failed", x.Project+" - couldn't compact", msg)
			}
		}
	case "dashboard":
		showDashboard(srv.URL())
	case "widget_moved":
		var x, y float64
		if _, err := fmt.Sscanf(payload, "%f,%f", &x, &y); err == nil {
			srv.cfg.WidgetPos = []float64{x, y}
			if c, err := readConfig(); err == nil {
				c.WidgetPos = []float64{x, y}
				_ = saveConfig(c)
			}
		}
	}
}
