//go:build darwin

package main

import (
	"fmt"
	"strings"
	"time"
)

const itermID = "com.googlecode.iterm2"
const appleTerminalID = "com.apple.Terminal"

func isJetBrains(app string) bool { return strings.HasPrefix(app, "com.jetbrains.") }

// ambiguousJetBrains: true if env is a JetBrains terminal and another live
// session is also using a JetBrains IDE. Activation targets the app, not a
// window or tab - with two such sessions there's no way to tell them apart,
// so typing here risks landing in the wrong one's terminal entirely (not
// just the wrong tab of the same window - confirmed 2026-10-15: it hit a
// different window's session, broader than first scoped).
func ambiguousJetBrains(others []map[string]string, env map[string]string) bool {
	if !isJetBrains(appOf(env)) {
		return false
	}
	for _, o := range others {
		if isJetBrains(appOf(o)) {
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
		return false, "Another Claude session is also open in a JetBrains IDE right now - Switchboard can't tell terminal tabs (or windows) apart there, so Compact is disabled here until only one JetBrains session is active."
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
// terminal tabs, so there's no way to address a session directly like the
// other terminals below. This activates the IDE and synthesizes the
// keystrokes itself (Core Graphics, in ui_darwin_impl.h) rather than
// shelling out to osascript/System Events, so the Accessibility permission
// this needs is scoped to switchboard alone, not the shared AppleScript
// interpreter every app on the Mac can invoke.
// ponytail: keystrokes go wherever focus lands in the JetBrains app -
// confirmed 2026-10-15 this isn't even window-scoped, let alone tab-scoped:
// with two sessions each in their own JetBrains window, activation can
// bring forward the wrong window entirely. ambiguousJetBrains (above) is
// the guard: refuse to compact rather than risk hitting the wrong session.
// Upgrade path if that matters: none via scripting - JetBrains exposes no
// window/tab/session API to target.
func jetbrainsSend(bundleID, text string) error {
	if !sendKeystrokesNative(bundleID, text) {
		return fmt.Errorf("couldn't type into the JetBrains IDE: switchboard needs Accessibility permission " +
			"(System Settings → Privacy & Security → Accessibility → allow switchboard)")
	}
	return nil
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
		return jetbrainsSend(app, text)
	}
	return errNoTyping
}
