package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const version = "2.0.0"

const usage = `Switchboard %s: one place for all your Claude Code sessions.

  switchboard install          add hooks to ~/.claude/settings.json (backs it up first)
  switchboard start            start Switchboard (on a Mac: floating widget, cards, menu bar)
  switchboard start --dashboard   also open the web dashboard
  switchboard start --headless    server + dashboard only, no native interface
  switchboard status           show whether hooks are installed and it's running
  switchboard autostart on|off start automatically when you log in (macOS)
  switchboard demo             send example sessions to a running Switchboard
  switchboard uninstall        remove Switchboard's hooks (other hooks untouched)
  switchboard version

Options for install:  --minimal (fewer hooks, less live detail)   --shell-form (older Claude Code)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Printf(usage, version)
		return
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "hook":
		os.Exit(runHook())
	case "start":
		cmdStart(args)
	case "install":
		fs := flag.NewFlagSet("install", flag.ExitOnError)
		minimal := fs.Bool("minimal", false, "skip per-tool events")
		shell := fs.Bool("shell-form", false, "for older Claude Code versions")
		_ = fs.Parse(args)
		backup, err := install(*minimal, *shell)
		if err != nil {
			fail(err)
		}
		fmt.Println("Hooks installed in", claudeSettingsPath())
		if backup != "" {
			fmt.Println("Backup of your previous settings:", backup)
		}
		fmt.Println("Switchboard itself was copied to", installedBinary())
		fmt.Println("Start it with: switchboard start   (or ./switchboard start from this folder)")
	case "uninstall":
		backup, err := uninstall()
		if err != nil {
			fail(err)
		}
		_ = autostartOffQuiet()
		fmt.Println("Switchboard's hooks were removed from", claudeSettingsPath())
		if backup != "" {
			fmt.Println("Backup:", backup)
		}
	case "status":
		cfg := loadConfig()
		ev := installedEvents()
		if len(ev) == 0 {
			fmt.Println("Hooks: not installed (run: switchboard install)")
		} else {
			fmt.Println("Hooks installed for:", strings.Join(ev, ", "))
		}
		if isRunning(cfg) {
			fmt.Println("Running at", fmt.Sprintf("http://127.0.0.1:%d", cfg.Port))
		} else {
			fmt.Println("Not running")
		}
		fmt.Println("Data folder:", sbHome)
	case "autostart":
		if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
			fail(fmt.Errorf("use: switchboard autostart on   or   switchboard autostart off"))
		}
		if err := autostart(args[0] == "on"); err != nil {
			fail(err)
		}
		if args[0] == "on" {
			fmt.Println("Done. Switchboard is starting now and will start every time you log in.")
		} else {
			fmt.Println("Switchboard won't start at login anymore.")
		}
	case "demo":
		cmdDemo()
	case "version", "--version", "-v":
		fmt.Println("switchboard", version)
	default:
		fmt.Printf(usage, version)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "Error:", err)
	os.Exit(1)
}

func autostartOffQuiet() error {
	if _, err := os.Stat(plistPath()); err == nil {
		return autostart(false)
	}
	return nil
}

var localClient = &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}

func isRunning(cfg Config) bool {
	resp, err := localClient.Get(fmt.Sprintf("http://127.0.0.1:%d/health", cfg.Port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var m map[string]any
	return json.NewDecoder(resp.Body).Decode(&m) == nil && m["app"] == "switchboard"
}

func cmdStart(args []string) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	noBrowser := fs.Bool("no-browser", false, "don't open the dashboard")
	dashboard := fs.Bool("dashboard", false, "also open the web dashboard")
	headless := fs.Bool("headless", false, "no native interface")
	_ = fs.Parse(args)
	cfg := loadConfig()
	if isRunning(cfg) {
		fmt.Println("Switchboard is already running. Opening the dashboard.")
		if !*noBrowser {
			openBrowser(fmt.Sprintf("http://127.0.0.1:%d/", cfg.Port))
		}
		return
	}
	srv := NewServer(&cfg)
	ln, err := srv.Listen()
	if err != nil {
		fail(fmt.Errorf("can't use port %d (%v). Change \"port\" in %s, then run install again", cfg.Port, err, configPath()))
	}
	go func() { _ = (&http.Server{Handler: srv}).Serve(ln) }()
	if len(installedEvents()) == 0 {
		fmt.Println("Note: hooks aren't installed yet. Run: switchboard install")
	}
	if nativeUI && !*headless {
		fmt.Println("Switchboard is running: look for the floating widget (bottom-right) and the ☎︎ menu bar icon.")
		fmt.Println("Dashboard:", srv.URL(), "  (Ctrl+C here to stop)")
		if *dashboard {
			openBrowser(srv.URL())
		}
		runNativeUI(srv) // blocks until you quit
		return
	}
	fmt.Println("Switchboard running at", srv.URL(), " (Ctrl+C to stop)")
	if !*noBrowser {
		go func() { time.Sleep(300 * time.Millisecond); openBrowser(srv.URL()) }()
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Println("\nStopped.")
}

func post(cfg Config, path string, v any, timeout time.Duration) (string, error) {
	b, _ := json.Marshal(v)
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d%s", cfg.Port, path), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Switchboard-Token", cfg.Token)
	c := &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil}}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return buf.String(), nil
}

func cmdDemo() {
	cfg := loadConfig()
	if !isRunning(cfg) {
		fail(fmt.Errorf("start Switchboard first: switchboard start"))
	}
	ready := func() bool {
		resp, err := localClient.Get(fmt.Sprintf("http://127.0.0.1:%d/api/state?t=%s", cfg.Port, cfg.Token))
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		c, _ := m["clients"].(float64)
		p, _ := m["popups"].(bool)
		return c > 0 || p
	}
	if !ready() {
		fmt.Println("Waiting for the dashboard to open...")
		openBrowser(fmt.Sprintf("http://127.0.0.1:%d/", cfg.Port))
		for i := 0; i < 120 && !ready(); i++ {
			time.Sleep(500 * time.Millisecond)
		}
	}
	home, _ := os.UserHomeDir()
	tmp, _ := os.MkdirTemp("", "switchboard-demo-")
	sessions := [][2]string{{"demo-1", filepath.Join(home, "code", "shop-api")},
		{"demo-2", filepath.Join(home, "code", "landing-page")}, {"demo-3", filepath.Join(home, "code", "ml-pipeline")}}
	for i, s := range sessions {
		tp := filepath.Join(tmp, s[0]+".jsonl")
		line, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"id": fmt.Sprint("m", i),
			"model": "claude-opus-5-5", "usage": map[string]any{"input_tokens": 9, "output_tokens": 3800 * (i + 1),
				"cache_read_input_tokens": 52000 * (i + 1), "cache_creation_input_tokens": 1200}}})
		_ = os.WriteFile(tp, append(line, '\n'), 0o644)
		_, _ = post(cfg, "/hook/event", map[string]any{"hook_event_name": "SessionStart", "session_id": s[0], "cwd": s[1],
			"source": "startup", "transcript_path": tp}, 5*time.Second)
	}
	_, _ = post(cfg, "/hook/event", map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "demo-3",
		"cwd": sessions[2][1], "prompt": "Why is the nightly job slow?"}, 5*time.Second)
	_, _ = post(cfg, "/hook/event", map[string]any{"hook_event_name": "Stop", "session_id": "demo-3", "cwd": sessions[2][1]}, 5*time.Second)
	done := make(chan string, 2)
	go func() {
		out, _ := post(cfg, "/hook/permission", map[string]any{"hook_event_name": "PermissionRequest", "session_id": "demo-2",
			"cwd": sessions[1][1], "tool_name": "Bash", "tool_use_id": "t1",
			"tool_input": map[string]any{"command": "npm install framer-motion@11", "description": "Install animation lib"}}, 10*time.Minute)
		done <- "permission: " + out
	}()
	go func() {
		out, _ := post(cfg, "/hook/event", map[string]any{"hook_event_name": "PreToolUse", "session_id": "demo-1",
			"cwd": sessions[0][1], "tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []any{
				map[string]any{"question": "Which rate limit should the checkout endpoint use?", "multiSelect": false,
					"options": []any{map[string]any{"label": "20 per minute", "description": "Safe default"},
						map[string]any{"label": "60 per minute", "description": "For busy stores"},
						map[string]any{"label": "Per-user limit", "description": "Needs login"}}}}}}, 10*time.Minute)
		done <- "question: " + out
	}()
	fmt.Println("Demo sent: one permission request and one question are waiting. Answer them, or press Ctrl+C.")
	for i := 0; i < 2; i++ {
		fmt.Println("Answered ->", <-done)
	}
}
