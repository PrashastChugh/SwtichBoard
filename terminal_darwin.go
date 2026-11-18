//go:build darwin

package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const itermID = "com.googlecode.iterm2"
const appleTerminalID = "com.apple.Terminal"

func isJetBrains(app string) bool { return strings.HasPrefix(app, "com.jetbrains.") }

// ambiguousJetBrains: true if env is a JetBrains terminal and another live
// session shares its project. Switchboard picks the IDE window by project
// name, so sessions in different projects are fine, but two sessions in the
// same project (or one nested in the other, or with an unknown folder) have
// the same window and can't be told apart by their terminal tabs.
func ambiguousJetBrains(others []map[string]string, env map[string]string) bool {
	if !isJetBrains(appOf(env)) {
		return false
	}
	cwd := env["SB_CWD"]
	for _, o := range others {
		if !isJetBrains(appOf(o)) {
			continue
		}
		oc := o["SB_CWD"]
		if cwd == "" || oc == "" || cwd == oc ||
			strings.HasPrefix(oc, cwd+"/") || strings.HasPrefix(cwd, oc+"/") {
			return true
		}
	}
	return false
}

// canTypeReason explains a disabled Compact button for the dashboard
// tooltip - distinguishing "unsupported terminal" from "ambiguous with
// another live JetBrains session", which otherwise look identical (both
// just can_type:false) and are easy to mistake for the same cause.
func canTypeReason(env map[string]string, others []map[string]string) (bool, string) {
	if !canSendText(env) {
		return false, ""
	}
	if ambiguousJetBrains(others, env) {
		return false, "Another Claude session is open in the same JetBrains project - Switchboard can't tell their terminal tabs apart, so Compact is disabled here until only one of them is active."
	}
	return true, ""
}

func itermUUID(env map[string]string) string {
	if i := strings.Index(env["ITERM_SESSION_ID"], ":"); i >= 0 {
		return env["ITERM_SESSION_ID"][i+1:]
	}
	return ""
}

func appOf(env map[string]string) string {
	if b := env["__CFBundleIdentifier"]; b != "" {
		return b
	}
	if env["ITERM_SESSION_ID"] != "" || env["TERM_PROGRAM"] == "iTerm.app" {
		return itermID
	}
	if env["TERM_PROGRAM"] == "Apple_Terminal" {
		return appleTerminalID
	}
	return ""
}

func frontmostApp() string {
	asn, err := runTimeout(2*time.Second, "lsappinfo", "front")
	if err != nil || asn == "" {
		return ""
	}
	out, err := runTimeout(2*time.Second, "lsappinfo", "info", "-only", "bundleid", asn)
	if err != nil {
		return ""
	}
	if i := strings.Index(out, "="); i >= 0 {
		return strings.Trim(strings.TrimSpace(out[i+1:]), `"`)
	}
	return ""
}

// userIsLooking: is that session's terminal in front (and, for iTerm2, the exact tab)?
func userIsLooking(env map[string]string) bool {
	app := appOf(env)
	front := frontmostApp()
	if app == "" || front == "" || !strings.EqualFold(app, front) {
		return false
	}
	if uuid := itermUUID(env); uuid != "" && app == itermID {
		cur, err := runTimeout(3*time.Second, "osascript", "-e",
			`tell application "iTerm2" to get unique id of current session of current window`)
		if err == nil && cur != "" {
			return cur == uuid
		}
	}
	return true
}

func itermFind(uuid, action string) string {
	return `tell application "iTerm2"
  repeat with w in windows
    repeat with t in tabs of w
      repeat with s in sessions of t
        if unique id of s is ` + asQuote(uuid) + ` then
` + action + `
          return "ok"
        end if
      end repeat
    end repeat
  end repeat
end tell
return "missing"`
}

func terminalFind(tty, action string) string {
	return `tell application "Terminal"
  repeat with w in windows
    repeat with t in tabs of w
      if tty of t is ` + asQuote(tty) + ` then
` + action + `
        return "ok"
      end if
    end repeat
  end repeat
end tell
return "missing"`
}

// showTerminal brings the session's terminal (and tab/pane when known) to the front.
func showTerminal(env map[string]string) {
	if len(env) == 0 {
		return
	}
	go func() {
		app, uuid := appOf(env), itermUUID(env)
		switch {
		case app == itermID && uuid != "":
			_, _ = runTimeout(5*time.Second, "osascript", "-e", itermFind(uuid,
				"          activate\n          select w\n          select t\n          select s"))
		case app == appleTerminalID && env["SB_TTY"] != "":
			_, _ = runTimeout(5*time.Second, "osascript", "-e", terminalFind(env["SB_TTY"],
				"        activate\n        set selected of t to true\n        set index of w to 1"))
		case app != "":
			_, _ = run("open", "-b", app)
		}
		tmuxSelect(env)
	}()
}

func canSendText(env map[string]string) bool {
	app := appOf(env)
	return hasTmux(env) || hasWezterm(env) ||
		(app == itermID && itermUUID(env) != "") ||
		(app == appleTerminalID && env["SB_TTY"] != "") ||
		isJetBrains(app)
}

// jetbrainsSend: JetBrains IDEs have no AppleScript dictionary for their
// terminal tabs, so Switchboard raises the project's window through the
// Accessibility API (matched by project name) and synthesizes the keystrokes
// itself (Core Graphics, in ui_darwin_impl.h). It refuses instead of guessing
// when no window, several windows, or an unconfirmed focus come back.
func jetbrainsSend(bundleID, cwd, text string) error {
	switch sendKeystrokesNative(bundleID, cwd, text) {
	case 1:
		return nil
	case 2:
		return fmt.Errorf("couldn't find the JetBrains window for this project (%s). Open that project's window and try again", filepath.Base(cwd))
	case 3:
		return fmt.Errorf("more than one JetBrains window matches this project (%s), so Switchboard won't guess. Close the extra window and try again", filepath.Base(cwd))
	case 4:
		return fmt.Errorf("couldn't bring this project's JetBrains window to the front, so nothing was typed")
	}
	return fmt.Errorf("couldn't type into the JetBrains IDE: switchboard needs Accessibility permission " +
		"(System Settings → Privacy & Security → Accessibility → allow switchboard)")
}

// sendText types a line into the session's terminal and presses Enter.
func sendText(env map[string]string, text string) error {
	app, uuid := appOf(env), itermUUID(env)
	switch {
	case hasTmux(env): // inside tmux, type into the pane itself
		return tmuxSend(env, text)
	case hasWezterm(env):
		return weztermSend(env, text)
	case app == itermID && uuid != "":
		out, err := runTimeout(5*time.Second, "osascript", "-e", itermFind(uuid,
			"          tell s to write text "+asQuote(text)+" newline NO\n          delay 0.3\n          tell s to write text \"\""))
		if err != nil || out != "ok" {
			return errNoTyping
		}
		return nil
	case app == appleTerminalID && env["SB_TTY"] != "":
		out, err := runTimeout(5*time.Second, "osascript", "-e", terminalFind(env["SB_TTY"],
			"        do script "+asQuote(text)+" in t"))
		if err != nil || out != "ok" {
			return errNoTyping
		}
		return nil
	case isJetBrains(app):
		return jetbrainsSend(app, env["SB_CWD"], text)
	}
	return errNoTyping
}
