package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Rules answer matching requests instantly. Deny beats allow.
// Safety: a wildcard allow rule for a shell command never matches a command
// that chains or redirects, so "npm test*" can't approve "npm test && rm -rf ~".

var shellTools = map[string]bool{"Bash": true, "PowerShell": true}
var fileTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "Read": true, "NotebookEdit": true}
var risky = []string{";", "&&", "||", "|", "`", "$(", ">", "<", "\n"}

type Rule struct {
	ID      string  `json:"id"`
	Action  string  `json:"action"`
	Tool    string  `json:"tool"`
	Pattern string  `json:"pattern"`
	Project string  `json:"project"`
	Created float64 `json:"created"`
	Hits    int     `json:"hits"`
}

type Rules struct {
	mu    sync.Mutex
	path  string
	items []Rule
}

func subjectOf(tool string, ti map[string]any) string {
	switch {
	case shellTools[tool]:
		return str(ti, "command")
	case fileTools[tool]:
		if p := str(ti, "file_path"); p != "" {
			return p
		}
		return str(ti, "notebook_path")
	case tool == "Grep" || tool == "Glob":
		return str(ti, "pattern")
	case tool == "WebFetch":
		return str(ti, "url")
	case tool == "WebSearch":
		return str(ti, "query")
	case tool == "Task" || tool == "Agent":
		return str(ti, "description")
	case tool == "AskUserQuestion":
		if qs, ok := ti["questions"].([]any); ok && len(qs) > 0 {
			if q, ok := qs[0].(map[string]any); ok {
				return str(q, "question")
			}
		}
	}
	return ""
}

func normPath(p string) string {
	if p == "" || p == "*" {
		return p
	}
	p = filepath.Clean(p)
	if filepath.Separator == '\\' {
		p = strings.ToLower(p)
	}
	return p
}

// globMatch: "*" matches anything (including "/"), "?" one character.
func globMatch(pattern, s string) bool {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString("(?s:.*)")
		case '?':
			b.WriteString("(?s:.)")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(s)
}

func loadRules() *Rules {
	r := &Rules{path: rulesPath()}
	if b, err := os.ReadFile(r.path); err == nil {
		_ = json.Unmarshal(b, &r.items)
	}
	return r
}

func (r *Rules) List() []Rule {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Rule{}, r.items...)
}

func (r *Rules) Add(action, tool, pattern, project string) (Rule, error) {
	if action != "allow" && action != "deny" {
		return Rule{}, errors.New("action must be allow or deny")
	}
	tool = strings.TrimSpace(tool)
	if tool == "" {
		tool = "*"
	}
	if pattern == "" {
		pattern = "*"
	}
	if project == "" {
		project = "*"
	}
	if action == "allow" && tool == "*" && pattern == "*" {
		return Rule{}, errors.New("that rule would allow everything; narrow the tool or pattern")
	}
	id := make([]byte, 5)
	_, _ = rand.Read(id)
	rule := Rule{ID: hex.EncodeToString(id), Action: action, Tool: tool, Pattern: pattern, Project: project,
		Created: float64(time.Now().UnixNano()) / 1e9}
	r.mu.Lock()
	r.items = append(r.items, rule)
	err := writeJSONAtomic(r.path, r.items)
	r.mu.Unlock()
	return rule, err
}

func (r *Rules) Delete(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.items[:0]
	for _, it := range r.items {
		if it.ID != id {
			kept = append(kept, it)
		}
	}
	r.items = kept
	_ = writeJSONAtomic(r.path, r.items)
}

func ruleMatches(rule Rule, tool, cwd, subject string) bool {
	if rule.Tool != "*" && rule.Tool != tool {
		return false
	}
	if rule.Project != "*" && normPath(rule.Project) != normPath(cwd) {
		return false
	}
	if rule.Pattern == subject {
		return true
	}
	if !globMatch(rule.Pattern, subject) {
		return false
	}
	if rule.Action == "allow" && shellTools[tool] {
		for _, tok := range risky {
			if strings.Contains(subject, tok) && !strings.Contains(rule.Pattern, tok) {
				return false
			}
		}
	}
	return true
}

// Match returns the winning rule (deny first) or nil.
func (r *Rules) Match(ev map[string]any) *Rule {
	tool, cwd := str(ev, "tool_name"), str(ev, "cwd")
	subject := subjectOf(tool, obj(ev, "tool_input"))
	r.mu.Lock()
	defer r.mu.Unlock()
	found := -1
	for i, rule := range r.items {
		if ruleMatches(rule, tool, cwd, subject) {
			if rule.Action == "deny" {
				found = i
				break
			}
			if found < 0 {
				found = i
			}
		}
	}
	if found < 0 {
		return nil
	}
	r.items[found].Hits++
	_ = writeJSONAtomic(r.path, r.items)
	rule := r.items[found]
	return &rule
}
