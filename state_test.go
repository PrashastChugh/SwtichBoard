package main

import "testing"

// Smallest check for QuickAllowRule: the native card's one-click "Always
// allow" must both resolve the pending request and leave behind an exact
// rule that a repeat of the same request matches.
func TestQuickAllowRule(t *testing.T) {
	old := sbHome
	sbHome = t.TempDir() // loadRules()/writeJSONAtomic must never touch the real ~/.switchboard
	defer func() { sbHome = old }()

	s := NewStore(&Config{})
	ev := map[string]any{"session_id": "s1", "cwd": "/proj", "tool_name": "Bash",
		"tool_input": map[string]any{"command": "npm test"}}
	p := s.AddPending(ev)

	if !s.QuickAllowRule(p.ID) {
		t.Fatal("QuickAllowRule returned false")
	}
	select {
	case <-p.done:
	default:
		t.Fatal("pending was not resolved")
	}
	if p.Decision != "allow" {
		t.Fatalf("decision = %q, want allow", p.Decision)
	}

	rules := s.Rules.List()
	if len(rules) != 1 || rules[0].Pattern != "npm test" || rules[0].Action != "allow" {
		t.Fatalf("unexpected rules: %+v", rules)
	}
	if s.Rules.Match(ev) == nil {
		t.Fatal("rule doesn't match a repeat of the same request")
	}
	if s.Rules.Match(map[string]any{"cwd": "/proj", "tool_name": "Bash",
		"tool_input": map[string]any{"command": "npm test && rm -rf ~"}}) != nil {
		t.Fatal("exact-match rule must not match a chained command")
	}
}
