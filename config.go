package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Everything Switchboard stores lives in ~/.switchboard (or $SWITCHBOARD_HOME).
var sbHome = func() string {
	if v := os.Getenv("SWITCHBOARD_HOME"); v != "" {
		return v
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".switchboard")
}()

func configPath() string { return filepath.Join(sbHome, "config.json") }
func rulesPath() string  { return filepath.Join(sbHome, "rules.json") }
func backupDir() string  { return filepath.Join(sbHome, "backups") }

type Config struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	// Seconds to hold a request while a dashboard is open, before the terminal prompt takes over.
	PermissionWait int `json:"permission_wait"`
	// Seconds to hold a request when only the popups / menu bar are available.
	QuickHold int `json:"quick_hold"`
	// System notifications when Switchboard's own popups aren't running.
	OSNotifications bool `json:"os_notifications"`
	NotifyOnFinish  bool `json:"notify_on_finish"`
	// With nothing to answer from (no dashboard, no popups), don't hold requests.
	PassthroughWhenClosed bool `json:"passthrough_when_closed"`
	// Don't hold or notify when that session's terminal is already in front (macOS).
	SkipWhenLooking bool `json:"skip_when_looking"`
	// macOS: Switchboard's own floating cards and widget.
	Popups    bool      `json:"popups"`
	Widget    bool      `json:"widget"`
	WidgetPos []float64 `json:"widget_pos,omitempty"`
	// Context window used for the "% full" figure in the dashboard.
	ContextWindow int `json:"context_window"`
}

func defaultConfig() Config {
	return Config{
		Port: 47821, PermissionWait: 540, QuickHold: 60,
		OSNotifications: true, NotifyOnFinish: true, PassthroughWhenClosed: true,
		SkipWhenLooking: true, Popups: true, Widget: true, ContextWindow: 200000,
	}
}

// readConfig never writes: safe to call from the hook.
func readConfig() (Config, error) {
	c := defaultConfig()
	b, err := os.ReadFile(configPath())
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	return c, err
}

// loadConfig creates the file and a secret token on first use.
func loadConfig() Config {
	_ = os.MkdirAll(sbHome, 0o755)
	c, err := readConfig()
	changed := err != nil
	if c.Token == "" {
		buf := make([]byte, 24)
		_, _ = rand.Read(buf)
		c.Token = base64.RawURLEncoding.EncodeToString(buf)
		changed = true
	}
	if c.Port == 0 {
		c.Port = 47821
	}
	if changed {
		_ = saveConfig(c)
	}
	return c
}

func saveConfig(c Config) error { return writeJSONAtomic(configPath(), c) }

func writeJSONAtomic(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	return os.Rename(tmp.Name(), path)
}

// ---- helpers for the loosely typed hook JSON ----

func str(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func obj(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if o, ok := m[key].(map[string]any); ok {
		return o
	}
	return nil
}

func envOf(ev map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range obj(ev, "_sb_env") {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

func baseName(p string) string {
	p = strings.TrimRight(strings.ReplaceAll(p, "\\", "/"), "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func projectName(cwd string) string {
	if cwd == "" {
		return "unknown"
	}
	return baseName(cwd)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
