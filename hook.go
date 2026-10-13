package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Terminal identity, so Switchboard can jump back to (or type into) the right tab.
var envKeys = []string{"TERM_PROGRAM", "ITERM_SESSION_ID", "TERM_SESSION_ID", "TMUX", "TMUX_PANE",
	"__CFBundleIdentifier", "KITTY_WINDOW_ID", "WEZTERM_PANE"}

// runHook is what Claude Code executes. Design rule: it must never break Claude
// Code. On any problem it exits 0 silently and Claude Code carries on as normal.
func runHook() int {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 0
	}
	cfg, err := readConfig()
	if err != nil || cfg.Token == "" {
		return 0
	}
	var ev map[string]any
	if json.Unmarshal(raw, &ev) != nil || ev == nil {
		return 0
	}
	name := str(ev, "hook_event_name")

	env := map[string]string{}
	for _, k := range envKeys {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	if name == "SessionStart" || name == "UserPromptSubmit" {
		if tty := parentTTY(); tty != "" {
			env["SB_TTY"] = tty
		}
	}
	if len(env) > 0 {
		ev["_sb_env"] = env
		if b, err := json.Marshal(ev); err == nil {
			raw = b
		}
	}

	question := name == "PreToolUse" && str(ev, "tool_name") == "AskUserQuestion"
	blocking := name == "PermissionRequest" || question
	path := "/hook/event"
	if name == "PermissionRequest" {
		path = "/hook/permission"
	}
	timeout := 1500 * time.Millisecond
	if blocking {
		timeout = time.Duration(cfg.PermissionWait+20) * time.Second
	}
	// Proxy: nil, so HTTP(S)_PROXY settings never route localhost traffic elsewhere.
	client := &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil}}
	req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d%s", cfg.Port, path), bytes.NewReader(raw))
	if err != nil {
		return 0
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Switchboard-Token", cfg.Token)
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if blocking && bytes.Contains(body, []byte("hookSpecificOutput")) {
		os.Stdout.Write(body)
	}
	return 0
}

// parentTTY finds the terminal device of the Claude Code process (our parent),
// used to find the right Terminal.app tab. Only run on a couple of events.
func parentTTY() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	out, err := exec.Command("ps", "-o", "tty=", "-p", fmt.Sprint(os.Getppid())).Output()
	if err != nil {
		return ""
	}
	t := strings.TrimSpace(string(out))
	if t == "" || t == "?" || t == "??" {
		return ""
	}
	if !strings.HasPrefix(t, "/dev/") {
		t = "/dev/" + t
	}
	return t
}
