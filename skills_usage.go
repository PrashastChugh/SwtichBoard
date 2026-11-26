package main

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Last-used times come from Claude Code's own transcripts: every use of a
// skill is a Skill tool call (or a typed /name command) with a timestamp.
// Each transcript is scanned once and re-read only when it grows.

var (
	skillCallRe = regexp.MustCompile(`"name":"Skill","input":\{"skill":"([^"]*)"`)
	slashCmdRe  = regexp.MustCompile(`<command-name>/([^<]+)</command-name>`)
	stampRe     = regexp.MustCompile(`"timestamp":"([^"]+)"`)
)

type skillUse struct {
	last  float64
	count int
}

type usageIndex map[string]skillUse

type usageFile struct {
	size int64
	mod  int64
	uses usageIndex
}

var (
	usageMu    sync.Mutex
	usageCache = map[string]usageFile{}
)

func skillUsage() usageIndex {
	usageMu.Lock()
	defer usageMu.Unlock()
	h, _ := os.UserHomeDir()
	files, _ := filepath.Glob(filepath.Join(h, ".claude", "projects", "*", "*.jsonl"))
	total := usageIndex{}
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		c, ok := usageCache[f]
		if !ok || c.size != st.Size() || c.mod != st.ModTime().UnixNano() {
			c = usageFile{size: st.Size(), mod: st.ModTime().UnixNano(), uses: scanUsage(f)}
			usageCache[f] = c
		}
		for name, u := range c.uses {
			t := total[name]
			t.count += u.count
			if u.last > t.last {
				t.last = u.last
			}
			total[name] = t
		}
	}
	return total
}

func scanUsage(path string) usageIndex {
	out := usageIndex{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			scanUsageLine(line, out)
		}
		if err != nil {
			if err != io.EOF {
				return out
			}
			break
		}
	}
	return out
}

func scanUsageLine(line []byte, out usageIndex) {
	var names []string
	if containsBytes(line, `"name":"Skill"`) {
		for _, m := range skillCallRe.FindAllSubmatch(line, -1) {
			names = append(names, string(m[1]))
		}
	}
	if containsBytes(line, `<command-name>/`) {
		for _, m := range slashCmdRe.FindAllSubmatch(line, -1) {
			names = append(names, string(m[1]))
		}
	}
	if len(names) == 0 {
		return
	}
	var ts float64
	if m := stampRe.FindSubmatch(line); m != nil {
		if t, err := time.Parse(time.RFC3339, string(m[1])); err == nil {
			ts = float64(t.UnixNano()) / 1e9
		}
	}
	for _, n := range names {
		u := out[n]
		u.count++
		if ts > u.last {
			u.last = ts
		}
		out[n] = u
	}
}

func containsBytes(b []byte, sub string) bool { return bytes.Contains(b, []byte(sub)) }

// lookup finds a skill's uses under the names Claude Code logs it by: the
// bare name, or "plugin:name" for plugin skills (synced ones are
// "anthropic-skills:name").
func (u usageIndex) lookup(sk *SkillInfo) (last float64, count int) {
	keys := []string{sk.Name}
	switch {
	case strings.HasPrefix(sk.Scope, "plugin:"):
		keys = append(keys, strings.TrimPrefix(sk.Scope, "plugin:")+":"+sk.Name)
	case sk.Origin == "synced":
		keys = append(keys, "anthropic-skills:"+sk.Name)
	}
	for _, k := range keys {
		if x, ok := u[k]; ok {
			count += x.count
			if x.last > last {
				last = x.last
			}
		}
	}
	return
}

// historyProjectDirs lists the folders you've run Claude Code in, read from
// the "cwd" recorded in each project's newest transcript. Folders that no
// longer exist are skipped.
func historyProjectDirs() []string {
	h, _ := os.UserHomeDir()
	dirs, _ := filepath.Glob(filepath.Join(h, ".claude", "projects", "*"))
	cwdRe := regexp.MustCompile(`"cwd":"([^"]+)"`)
	var out []string
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join(d, "*.jsonl"))
		var newest string
		var mod time.Time
		for _, f := range files {
			if st, err := os.Stat(f); err == nil && st.ModTime().After(mod) {
				newest, mod = f, st.ModTime()
			}
		}
		if newest == "" {
			continue
		}
		f, err := os.Open(newest)
		if err != nil {
			continue
		}
		r := bufio.NewReaderSize(f, 1<<20)
		for i := 0; i < 200; i++ {
			line, err := r.ReadBytes('\n')
			if m := cwdRe.FindSubmatch(line); m != nil {
				if st, e := os.Stat(string(m[1])); e == nil && st.IsDir() && string(m[1]) != h { // home is "Global"
					out = append(out, string(m[1]))
				}
				break
			}
			if err != nil {
				break
			}
		}
		f.Close()
	}
	return out
}

// allProjectDirs: live sessions first, then everything from history.
func (s *Store) allProjectDirs() []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range append(s.KnownProjectDirs(), historyProjectDirs()...) {
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}
