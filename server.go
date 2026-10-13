package main

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"
)

//go:embed web/index.html
var indexHTML string

const maxBody = 8 << 20

// Server: binds to 127.0.0.1 only, checks the Host header (blocks DNS
// rebinding), and every hook/API call needs the secret token.
type Server struct {
	cfg   *Config
	store *Store
}

func NewServer(cfg *Config) *Server { return &Server{cfg: cfg, store: NewStore(cfg)} }

func (srv *Server) URL() string { return fmt.Sprintf("http://127.0.0.1:%d/", srv.cfg.Port) }

func (srv *Server) Listen() (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", srv.cfg.Port))
}

func decisionOutput(behavior, message string) map[string]any {
	d := map[string]any{"behavior": behavior}
	if behavior == "deny" {
		if message == "" {
			message = "The user denied this from Switchboard."
		}
		d["message"] = message
		d["interrupt"] = false
	}
	return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PermissionRequest", "decision": d}}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}

func (srv *Server) hostOK(r *http.Request) bool {
	p := srv.cfg.Port
	return r.Host == fmt.Sprintf("127.0.0.1:%d", p) || r.Host == fmt.Sprintf("localhost:%d", p)
}

func (srv *Server) tokenOK(r *http.Request) bool {
	t := r.Header.Get("X-Switchboard-Token")
	if t == "" {
		t = r.URL.Query().Get("t")
	}
	return subtle.ConstantTimeCompare([]byte(t), []byte(srv.cfg.Token)) == 1
}

func (srv *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/health" {
		writeJSON(w, 200, map[string]any{"ok": true, "app": "switchboard", "lang": "go"})
		return
	}
	if !srv.hostOK(r) {
		writeJSON(w, 403, map[string]any{"error": "bad host"})
		return
	}
	if r.Method == "GET" && path == "/" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, strings.ReplaceAll(indexHTML, "__SB_TOKEN__", srv.cfg.Token))
		return
	}
	if !srv.tokenOK(r) {
		writeJSON(w, 401, map[string]any{"error": "bad token"})
		return
	}
	if r.Method == "GET" {
		switch path {
		case "/api/state":
			writeJSON(w, 200, srv.store.Snapshot())
		case "/api/stream":
			srv.stream(w, r)
		default:
			writeJSON(w, 404, map[string]any{"error": "not found"})
		}
		return
	}
	if r.Method != "POST" {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	var body map[string]any
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil || json.Unmarshal(raw, &body) != nil || body == nil {
		writeJSON(w, 400, map[string]any{"error": "invalid json"})
		return
	}
	switch path {
	case "/hook/event":
		if str(body, "hook_event_name") == "PreToolUse" && str(body, "tool_name") == "AskUserQuestion" {
			srv.store.HandleEvent(body)
			srv.question(w, r, body)
			return
		}
		srv.store.HandleEvent(body)
		writeJSON(w, 200, map[string]any{})
	case "/hook/permission":
		srv.permission(w, r, body)
	case "/api/decide":
		ok := srv.store.Decide(str(body, "id"), str(body, "behavior"), strings.TrimSpace(str(body, "message")))
		writeJSON(w, map[bool]int{true: 200, false: 404}[ok], map[string]any{"ok": ok})
	case "/api/answer":
		answers, _ := body["answers"].(map[string]any)
		ok := srv.store.Answer(str(body, "id"), answers)
		writeJSON(w, map[bool]int{true: 200, false: 400}[ok], map[string]any{"ok": ok})
	case "/api/rules":
		srv.rules(w, body)
	case "/api/focus":
		env := srv.store.SessionEnv(str(body, "session_id"))
		showTerminal(env)
		writeJSON(w, 200, map[string]any{"ok": len(env) > 0})
	case "/api/compact":
		srv.compact(w, body)
	default:
		writeJSON(w, 404, map[string]any{"error": "not found"})
	}
}

func (srv *Server) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, 500, map[string]any{"error": "streaming unsupported"})
		return
	}
	ch := srv.store.Subscribe()
	defer srv.store.Unsubscribe(ch)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(200)
	first, _ := json.Marshal(srv.store.Snapshot())
	fmt.Fprintf(w, "data: %s\n\n", first)
	fl.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case data := <-ch:
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			fl.Flush()
		case <-ping.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// holdTime: seconds to hold a request, or 0 to let the terminal handle it right away.
func (srv *Server) holdTime(ev map[string]any) int {
	st, cfg := srv.store, srv.cfg
	if cfg.SkipWhenLooking && userIsLooking(envOf(ev)) {
		st.MarkTerminalPrompt(ev, false) // you're already looking at it
		return 0
	}
	if st.HasClients() {
		return cfg.PermissionWait // dashboard open
	}
	if st.BackgroundOK() {
		return cfg.QuickHold // popups running
	}
	if cfg.PassthroughWhenClosed {
		st.MarkTerminalPrompt(ev, true) // nothing to answer with: normal prompt
		return 0
	}
	return cfg.PermissionWait
}

// wait blocks until you decide, the time runs out, or the hook goes away.
func (srv *Server) wait(r *http.Request, p *Pending, seconds int) {
	t := time.NewTimer(time.Duration(seconds) * time.Second)
	defer t.Stop()
	select {
	case <-p.done:
	case <-t.C:
	case <-r.Context().Done(): // e.g. you pressed Esc in Claude Code
	}
	p.Resolve("", "", nil)
	srv.store.FinishPending(p)
}

func (srv *Server) permission(w http.ResponseWriter, r *http.Request, ev map[string]any) {
	if str(ev, "tool_name") == "AskUserQuestion" {
		writeJSON(w, 200, map[string]any{}) // questions are handled at PreToolUse: never a second card
		return
	}
	if rule := srv.store.Rules.Match(ev); rule != nil {
		srv.store.NoteRule(ev, rule)
		msg := ""
		if rule.Action == "deny" {
			msg = "Blocked by a Switchboard rule."
		}
		writeJSON(w, 200, decisionOutput(rule.Action, msg))
		return
	}
	hold := srv.holdTime(ev)
	if hold == 0 {
		writeJSON(w, 200, map[string]any{})
		return
	}
	p := srv.store.AddPending(ev)
	srv.wait(r, p, hold)
	if p.Decision == "allow" || p.Decision == "deny" {
		writeJSON(w, 200, decisionOutput(p.Decision, p.Message))
		return
	}
	writeJSON(w, 200, map[string]any{}) // no decision: Claude Code shows its own prompt
}

// question: Claude asks a multiple-choice question; we answer it at PreToolUse.
func (srv *Server) question(w http.ResponseWriter, r *http.Request, ev map[string]any) {
	hold := srv.holdTime(ev)
	if hold == 0 {
		writeJSON(w, 200, map[string]any{})
		return
	}
	p := srv.store.AddPending(ev)
	srv.wait(r, p, hold)
	if p.Decision == "answer" && len(p.Answers) > 0 {
		updated := map[string]any{}
		for k, v := range obj(ev, "tool_input") {
			updated[k] = v
		}
		updated["answers"] = p.Answers
		writeJSON(w, 200, map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": "PreToolUse", "permissionDecision": "allow",
			"permissionDecisionReason": "Answered in Switchboard", "updatedInput": updated}})
		return
	}
	writeJSON(w, 200, map[string]any{}) // no answer: Claude Code shows its own picker
}

func (srv *Server) rules(w http.ResponseWriter, body map[string]any) {
	switch str(body, "action") {
	case "add":
		r := obj(body, "rule")
		rule, err := srv.store.Rules.Add(str(r, "action"), str(r, "tool"), str(r, "pattern"), str(r, "project"))
		if err != nil {
			writeJSON(w, 400, map[string]any{"error": err.Error()})
			return
		}
		srv.store.ApplyRulesToPending()
		srv.store.Touch()
		writeJSON(w, 200, map[string]any{"ok": true, "rule": rule})
	case "delete":
		srv.store.Rules.Delete(str(body, "id"))
		srv.store.Touch()
		writeJSON(w, 200, map[string]any{"ok": true})
	default:
		writeJSON(w, 400, map[string]any{"error": "unknown action"})
	}
}

// cleanLine keeps typed text to one safe line (no Enter, no control keys).
func cleanLine(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return clip(strings.TrimSpace(b.String()), max)
}

// compact types /compact into the session's terminal, only while Claude is idle.
func (srv *Server) compact(w http.ResponseWriter, body map[string]any) {
	sid := str(body, "session_id")
	x, ok := srv.store.SessionCopy(sid)
	if !ok {
		writeJSON(w, 404, map[string]any{"error": "That session isn't connected anymore."})
		return
	}
	if x.Status == "working" || x.Status == "needs_permission" || srv.store.hasPendingLocked(sid) {
		writeJSON(w, 409, map[string]any{"error": "Claude is busy in that session. Compact when it's your turn."})
		return
	}
	text := "/compact"
	if f := cleanLine(str(body, "focus"), 400); f != "" {
		text += " " + f
	}
	if err := sendText(x.Env, text); err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	srv.store.LogFor(sid, "compact", "Sent "+clip(text, 120))
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Store) hasPendingLocked(sid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasPending(sid)
}
