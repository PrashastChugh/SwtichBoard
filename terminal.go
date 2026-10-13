package main

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// errNoTyping is shown when Switchboard can't type into a session's terminal.
var errNoTyping = errors.New("Switchboard can't type into this terminal. It works with iTerm2, " +
	"Apple Terminal, tmux and WezTerm. Type /compact in the session yourself.")

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func runTimeout(d time.Duration, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.Output(); close(done) }()
	select {
	case <-done:
		return strings.TrimSpace(string(out)), err
	case <-time.After(d):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return "", errors.New("timed out")
	}
}

func tmuxArgs(env map[string]string, args ...string) []string {
	sock := strings.SplitN(env["TMUX"], ",", 2)[0]
	return append([]string{"-S", sock}, args...)
}

func tmuxSelect(env map[string]string) {
	if env["TMUX"] == "" || env["TMUX_PANE"] == "" {
		return
	}
	pane := env["TMUX_PANE"]
	_, _ = run("tmux", tmuxArgs(env, "select-window", "-t", pane, ";", "select-pane", "-t", pane)...)
}

// Typing is done in two steps (text, then a separate Enter) so Claude Code
// doesn't mistake it for a paste and leave it unsent.
func tmuxSend(env map[string]string, text string) error {
	pane := env["TMUX_PANE"]
	if _, err := run("tmux", tmuxArgs(env, "send-keys", "-t", pane, "-l", text)...); err != nil {
		return err
	}
	time.Sleep(300 * time.Millisecond)
	_, err := run("tmux", tmuxArgs(env, "send-keys", "-t", pane, "Enter")...)
	return err
}

func weztermSend(env map[string]string, text string) error {
	pane := env["WEZTERM_PANE"]
	if _, err := run("wezterm", "cli", "send-text", "--pane-id", pane, "--no-paste", text); err != nil {
		return err
	}
	time.Sleep(300 * time.Millisecond)
	_, err := run("wezterm", "cli", "send-text", "--pane-id", pane, "--no-paste", "\r")
	return err
}

func hasTmux(env map[string]string) bool { return env["TMUX"] != "" && env["TMUX_PANE"] != "" }
func hasWezterm(env map[string]string) bool {
	if env["WEZTERM_PANE"] == "" {
		return false
	}
	_, err := exec.LookPath("wezterm")
	return err == nil
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("open", url).Start()
	case "windows":
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		if isWSL() {
			_ = exec.Command("cmd.exe", "/c", "start", url).Start()
			return
		}
		_ = exec.Command("xdg-open", url).Start()
	}
}

func isWSL() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	b, _ := os.ReadFile("/proc/version")
	return strings.Contains(strings.ToLower(string(b)), "microsoft")
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// notify sends a plain system notification (used when Switchboard's own popups aren't running).
func notify(title, body string) {
	body = clip(body, 240)
	switch {
	case runtime.GOOS == "darwin":
		_ = exec.Command("osascript", "-e", "display notification "+asQuote(body)+" with title "+asQuote(title)+
			` sound name "Glass"`).Start()
	case runtime.GOOS == "windows" || isWSL():
		ps := "[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null;" +
			"[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] > $null;" +
			"$x = New-Object Windows.Data.Xml.Dom.XmlDocument;" +
			"$x.LoadXml('<toast><visual><binding template=\"ToastGeneric\"><text>" +
			strings.ReplaceAll(xmlEscape(title), "'", "''") + "</text><text>" + strings.ReplaceAll(xmlEscape(body), "'", "''") +
			"</text></binding></visual></toast>');" +
			"$id = '{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\\WindowsPowerShell\\v1.0\\powershell.exe';" +
			"[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($id).Show([Windows.UI.Notifications.ToastNotification]::new($x))"
		_ = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", ps).Start()
	default:
		if _, err := exec.LookPath("notify-send"); err == nil {
			_ = exec.Command("notify-send", "-a", "Switchboard", title, body).Start()
		}
	}
}

// asQuote makes an AppleScript string literal.
func asQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
