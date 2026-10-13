package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func claudeSettingsPath() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".claude", "settings.json")
}

func installedBinary() string {
	name := "switchboard"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(sbHome, "bin", name)
}

type hookEvent struct {
	name       string
	hasMatcher bool
	minimal    bool // included with --minimal
}

var hookEvents = []hookEvent{
	{"SessionStart", true, true}, {"SessionEnd", true, true}, {"UserPromptSubmit", false, true},
	{"PreToolUse", true, true}, {"PostToolUse", true, false}, {"PostToolUseFailure", true, false},
	{"PermissionRequest", true, true}, {"Notification", true, true}, {"Stop", false, true},
	{"StopFailure", true, true}, {"SubagentStart", true, false}, {"SubagentStop", true, false},
	{"PreCompact", true, true},
}

// isOurs recognises Switchboard hooks, including the older Python version's.
func isOurs(handler any) bool {
	b, _ := json.Marshal(handler)
	s := strings.ReplaceAll(string(b), `\\`, "/")
	return strings.Contains(s, ".switchboard/hook.py") || strings.Contains(s, ".switchboard/bin/switchboard") ||
		strings.Contains(s, filepath.ToSlash(installedBinary())) || strings.Contains(s, "app=switchboard")
}

func loadClaudeSettings() (map[string]any, error) {
	b, err := os.ReadFile(claudeSettingsPath())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(b)) == "" {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON (%v). Fix it first, then run install again", claudeSettingsPath(), err)
	}
	return m, nil
}

func backupSettings() string {
	src := claudeSettingsPath()
	b, err := os.ReadFile(src)
	if err != nil {
		return ""
	}
	_ = os.MkdirAll(backupDir(), 0o755)
	dst := filepath.Join(backupDir(), "settings-"+time.Now().Format("20060102-150405")+".json")
	if os.WriteFile(dst, b, 0o644) != nil {
		return ""
	}
	return dst
}

func stripOurs(settings map[string]any) {
	hooks, _ := settings["hooks"].(map[string]any)
	for event, groupsAny := range hooks {
		groups, _ := groupsAny.([]any)
		var kept []any
		for _, g := range groups {
			gm, ok := g.(map[string]any)
			if !ok {
				kept = append(kept, g)
				continue
			}
			hs, _ := gm["hooks"].([]any)
			var keepHooks []any
			for _, h := range hs {
				if !isOurs(h) {
					keepHooks = append(keepHooks, h)
				}
			}
			if len(keepHooks) > 0 {
				ng := map[string]any{}
				for k, v := range gm {
					ng[k] = v
				}
				ng["hooks"] = keepHooks
				kept = append(kept, ng)
			}
		}
		if len(kept) > 0 {
			hooks[event] = kept
		} else {
			delete(hooks, event)
		}
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
}

// copySelf puts this program at ~/.switchboard/bin so moving the download folder won't break hooks.
func copySelf() (string, error) {
	src, err := os.Executable()
	if err != nil {
		return "", err
	}
	src, _ = filepath.EvalSymlinks(src)
	dst := installedBinary()
	if filepath.Clean(src) == filepath.Clean(dst) {
		return dst, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return "", err
	}
	out.Close()
	return dst, os.Rename(tmp, dst)
}

func handlerFor(cfg Config, event, bin string, shellForm bool) map[string]any {
	timeout := 10
	switch event {
	case "PermissionRequest", "PreToolUse": // PreToolUse waits when Claude asks you a question
		timeout = cfg.PermissionWait + 40
	case "SessionEnd":
		timeout = 5
	}
	bin = filepath.ToSlash(bin)
	if shellForm {
		return map[string]any{"type": "command", "command": `"` + bin + `" hook`, "timeout": timeout}
	}
	return map[string]any{"type": "command", "command": bin, "args": []any{"hook"}, "timeout": timeout}
}

func install(minimal, shellForm bool) (string, error) {
	cfg := loadConfig()
	bin, err := copySelf()
	if err != nil {
		return "", fmt.Errorf("couldn't copy Switchboard to %s: %v", installedBinary(), err)
	}
	settings, err := loadClaudeSettings()
	if err != nil {
		return "", err
	}
	stripOurs(settings)
	backup := backupSettings()
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for _, e := range hookEvents {
		if minimal && !e.minimal {
			continue
		}
		group := map[string]any{"hooks": []any{handlerFor(cfg, e.name, bin, shellForm)}}
		if e.hasMatcher {
			group["matcher"] = ""
		}
		list, _ := hooks[e.name].([]any)
		hooks[e.name] = append(list, group)
	}
	settings["hooks"] = hooks
	_ = os.Remove(filepath.Join(sbHome, "hook.py")) // leftover from the Python version
	return backup, writeJSONAtomic(claudeSettingsPath(), settings)
}

func uninstall() (string, error) {
	settings, err := loadClaudeSettings()
	if err != nil {
		return "", err
	}
	backup := backupSettings()
	stripOurs(settings)
	return backup, writeJSONAtomic(claudeSettingsPath(), settings)
}

func installedEvents() []string {
	settings, err := loadClaudeSettings()
	if err != nil {
		return nil
	}
	hooks, _ := settings["hooks"].(map[string]any)
	var out []string
	for event, groupsAny := range hooks {
		groups, _ := groupsAny.([]any)
	found:
		for _, g := range groups {
			gm, _ := g.(map[string]any)
			hs, _ := gm["hooks"].([]any)
			for _, h := range hs {
				if isOurs(h) {
					out = append(out, event)
					break found
				}
			}
		}
	}
	return out
}

// ----- macOS: start at login -------------------------------------------------

const launchLabel = "com.switchboard.app"

func plistPath() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Library", "LaunchAgents", launchLabel+".plist")
}

func autostart(on bool) error {
	if runtime.GOOS != "darwin" {
		return errors.New("start at login is set up for macOS only")
	}
	uid := fmt.Sprint(os.Getuid())
	_ = exec.Command("launchctl", "bootout", "gui/"+uid, plistPath()).Run()
	// also remove the Python version's login item if present
	h, _ := os.UserHomeDir()
	old := filepath.Join(h, "Library", "LaunchAgents", "com.switchboard.menubar.plist")
	if _, err := os.Stat(old); err == nil {
		_ = exec.Command("launchctl", "bootout", "gui/"+uid, old).Run()
		_ = os.Remove(old)
	}
	if !on {
		_ = os.Remove(plistPath())
		return nil
	}
	bin, err := copySelf()
	if err != nil {
		return err
	}
	logf := filepath.Join(sbHome, "switchboard.log")
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + launchLabel + `</string>
  <key>ProgramArguments</key><array><string>` + xmlEscape(bin) + `</string><string>start</string><string>--no-browser</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>StandardOutPath</key><string>` + xmlEscape(logf) + `</string>
  <key>StandardErrorPath</key><string>` + xmlEscape(logf) + `</string>
  <key>EnvironmentVariables</key><dict><key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string></dict>
</dict>
</plist>
`
	if err := os.MkdirAll(filepath.Dir(plistPath()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(plistPath(), []byte(plist), 0o644); err != nil {
		return err
	}
	if exec.Command("launchctl", "bootstrap", "gui/"+uid, plistPath()).Run() != nil {
		_ = exec.Command("launchctl", "load", "-w", plistPath()).Run()
	}
	return nil
}
