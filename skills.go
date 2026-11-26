package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var validSkillName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// skillPath resolves where a skill's SKILL.md goes: "global" means
// ~/.claude/skills/<name>/SKILL.md, anything else is treated as a project
// folder and gets <project>/.claude/skills/<name>/SKILL.md.
func skillPath(name, scope string) (string, error) {
	if !validSkillName.MatchString(name) {
		return "", errors.New("name must use only letters, digits, hyphens and underscores")
	}
	if scope == "global" || scope == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, ".claude", "skills", name, "SKILL.md"), nil
	}
	return filepath.Join(scope, ".claude", "skills", name, "SKILL.md"), nil
}

// createSkill writes a new SKILL.md, refusing to overwrite one that's
// already there - delete it yourself first if you want to replace it.
func createSkill(name, description, content, scope string) error {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	content = strings.TrimSpace(content)
	if name == "" || description == "" || content == "" {
		return errors.New("name, description and content are all required")
	}
	path, err := skillPath(name, scope)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return errors.New("a skill named " + name + " already exists at that scope - delete it first to replace it")
	}
	body := "---\nname: " + name + "\ndescription: " + yamlQuote(description) + "\n---\n\n" + content + "\n"
	if err := writeFileAtomic(path, []byte(body)); err != nil {
		return err
	}
	markCreated(path, true)
	return nil
}

// skillParts splits a SKILL.md into its frontmatter lines and body text.
func skillParts(path string) (front []string, body string, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, strings.TrimSpace(string(b)), nil
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return lines[1:i], strings.TrimSpace(strings.Join(lines[i+1:], "\n")), nil
		}
	}
	return nil, strings.TrimSpace(string(b)), nil
}

// skillContent returns the instructions part of an existing skill, for editing.
func skillContent(name, scope string) (string, error) {
	path, err := skillPath(name, scope)
	if err != nil {
		return "", err
	}
	_, body, err := skillParts(path)
	if err != nil {
		return "", errors.New("that skill doesn't exist")
	}
	return body, nil
}

// updateSkill rewrites an existing skill's description and instructions,
// keeping its name and any other frontmatter lines (e.g. allowed-tools).
func updateSkill(name, description, content, scope string) error {
	description, content = strings.TrimSpace(description), strings.TrimSpace(content)
	if description == "" || content == "" {
		return errors.New("description and content are both required")
	}
	path, err := skillPath(name, scope)
	if err != nil {
		return err
	}
	front, _, err := skillParts(path)
	if err != nil {
		return errors.New("that skill doesn't exist")
	}
	kept, inDesc := []string{}, false
	for _, l := range front {
		t := strings.TrimSpace(l)
		if inDesc && l != "" && (l[0] == ' ' || l[0] == '\t') {
			continue // continuation of the old description
		}
		inDesc = false
		if strings.HasPrefix(t, "name:") {
			continue
		}
		if strings.HasPrefix(t, "description:") {
			inDesc = true
			continue
		}
		kept = append(kept, l)
	}
	head := append([]string{"name: " + name, "description: " + yamlQuote(description)}, kept...)
	return writeFileAtomic(path, []byte("---\n"+strings.Join(head, "\n")+"\n---\n\n"+content+"\n"))
}

// Skills made through Switchboard are remembered by path, so the list can tell
// them apart from files you wrote by hand.
func createdSkillsPath() string { return filepath.Join(sbHome, "skills_created.json") }

func loadCreated() map[string]bool {
	var paths []string
	if b, err := os.ReadFile(createdSkillsPath()); err == nil {
		_ = json.Unmarshal(b, &paths)
	}
	out := map[string]bool{}
	for _, p := range paths {
		out[p] = true
	}
	return out
}

func markCreated(path string, on bool) {
	m := loadCreated()
	if on {
		m[path] = true
	} else {
		delete(m, path)
	}
	paths := []string{}
	for p := range m {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	b, _ := json.Marshal(paths)
	_ = writeFileAtomic(createdSkillsPath(), b)
}

// yamlQuote: a description can contain ':' or other characters that would
// break a bare YAML scalar - always emit it as a quoted string instead of
// trying to detect which descriptions need it.
func yamlQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", " ")
	return `"` + s + `"`
}

// deleteSkill removes a skill's whole folder (not just SKILL.md).
func deleteSkill(name, scope string) error {
	path, err := skillPath(name, scope)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return errors.New("that skill doesn't exist")
	}
	markCreated(path, false)
	return os.RemoveAll(filepath.Dir(path))
}

type SkillInfo struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Scope       string  `json:"scope"`    // "global", "plugin:<name>" or a project path
	ReadOnly    bool    `json:"readonly"` // plugin or synced skills: listed here, managed elsewhere
	Path        string  `json:"path"`
	Origin      string  `json:"origin"`    // switchboard | own | synced | plugin
	Source      string  `json:"source"`    // plugin id, e.g. superpowers@claude-plugins-official
	Status      string  `json:"status"`    // active | manual | disabled | not-enabled
	LastUsed    float64 `json:"last_used"` // unix seconds, 0 = never seen
	Uses        int     `json:"uses"`
	manual      bool
}

// listSkills scans the global skills folder plus every known project's
// .claude/skills folder - the same "known projects" universe the Rules and
// Skills dialogs already use, so this never needs to scan the filesystem
// for folders Switchboard has no other reason to know about.
func listSkills(projects []string) []SkillInfo {
	out := []SkillInfo{}
	h, _ := os.UserHomeDir()
	for _, sk := range scanSkillTree(filepath.Join(h, ".claude", "skills"), "global", 3) {
		// only skills sitting directly in the folder can be deleted from here
		sk.ReadOnly = !fileExists(filepath.Join(h, ".claude", "skills", sk.Name, "SKILL.md"))
		out = append(out, sk)
	}
	out = append(out, pluginSkills(h)...)
	for _, p := range projects {
		if p == "" {
			continue
		}
		out = append(out, scanSkillDir(filepath.Join(p, ".claude", "skills"), p)...)
	}
	decorateSkills(out, h)
	return out
}

// decorateSkills fills in where each skill came from, whether it's active,
// and when Claude Code last used it.
func decorateSkills(list []SkillInfo, home string) {
	enabled := enabledPlugins(home)
	created := loadCreated()
	usage := skillUsage()
	for i := range list {
		sk := &list[i]
		switch {
		case strings.HasPrefix(sk.Scope, "plugin:"):
			sk.Origin = "plugin"
			switch on, known := enabled[sk.Source]; {
			case known && on:
				sk.Status = "active"
			case known:
				sk.Status = "disabled"
			default:
				sk.Status = "not-enabled"
			}
		case strings.Contains(filepath.ToSlash(sk.Path), "/.claude/skills/synced/"):
			sk.Origin, sk.Status = "synced", "active"
		case created[sk.Path]:
			sk.Origin, sk.Status = "switchboard", "active"
		default:
			sk.Origin, sk.Status = "own", "active"
		}
		if sk.manual && sk.Status == "active" {
			sk.Status = "manual"
		}
		sk.LastUsed, sk.Uses = usage.lookup(sk)
	}
}

// enabledPlugins reads the on/off switches Claude Code keeps in settings.json.
func enabledPlugins(home string) map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return out
	}
	var f struct {
		Enabled map[string]bool `json:"enabledPlugins"`
	}
	if json.Unmarshal(b, &f) == nil {
		out = f.Enabled
	}
	return out
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// pluginSkills lists skills of installed Claude Code plugins, read from
// installed_plugins.json so old cached versions aren't listed twice.
func pluginSkills(home string) []SkillInfo {
	b, err := os.ReadFile(filepath.Join(home, ".claude", "plugins", "installed_plugins.json"))
	if err != nil {
		return nil
	}
	var f struct {
		Plugins map[string][]struct {
			InstallPath string `json:"installPath"`
		} `json:"plugins"`
	}
	if json.Unmarshal(b, &f) != nil {
		return nil
	}
	var out []SkillInfo
	for id, installs := range f.Plugins {
		name, _, _ := strings.Cut(id, "@")
		for _, in := range installs {
			for _, sk := range scanSkillDir(filepath.Join(in.InstallPath, "skills"), "plugin:"+name) {
				sk.ReadOnly, sk.Source = true, id
				out = append(out, sk)
			}
		}
	}
	return out
}

// scanSkillTree finds SKILL.md files up to depth folders down (synced skills
// sit one or two folders below ~/.claude/skills).
func scanSkillTree(dir, scope string, depth int) []SkillInfo {
	if sk, ok := readSkill(filepath.Join(dir, "SKILL.md"), scope); ok {
		return []SkillInfo{sk}
	}
	if depth == 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []SkillInfo
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, scanSkillTree(filepath.Join(dir, e.Name()), scope, depth-1)...)
		}
	}
	return out
}

func scanSkillDir(dir, scope string) []SkillInfo {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []SkillInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if sk, ok := readSkill(filepath.Join(dir, e.Name(), "SKILL.md"), scope); ok {
			out = append(out, sk)
		}
	}
	return out
}

// readSkill pulls name, description and the manual-only flag out of a
// SKILL.md's YAML frontmatter. Deliberately a simple line scan, not a real
// YAML parser; it does follow a folded/literal description (">" or "|") onto
// its indented lines, so the whole description shows.
func readSkill(path, scope string) (SkillInfo, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return SkillInfo{}, false
	}
	sk := SkillInfo{Scope: scope, Path: path}
	inFrontmatter, inDesc := false, false
	var desc []string
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			if inFrontmatter {
				break
			}
			inFrontmatter = true
			continue
		}
		if !inFrontmatter {
			continue
		}
		if inDesc {
			if line != "" && (line[0] == ' ' || line[0] == '\t') {
				desc = append(desc, trimmed)
				continue
			}
			inDesc = false
		}
		if v, found := strings.CutPrefix(trimmed, "name:"); found {
			sk.Name = unquoteYAML(strings.TrimSpace(v))
		}
		if v, found := strings.CutPrefix(trimmed, "description:"); found {
			v = strings.TrimSpace(v)
			if v == ">" || v == "|" || v == ">-" || v == "|-" || v == "" {
				inDesc = true
			} else {
				desc = []string{unquoteYAML(v)}
			}
		}
		if v, found := strings.CutPrefix(trimmed, "disable-model-invocation:"); found {
			sk.manual = strings.TrimSpace(v) == "true"
		}
	}
	sk.Description = strings.Join(desc, " ")
	return sk, sk.Name != ""
}

func unquoteYAML(s string) string {
	s = strings.TrimPrefix(s, `"`)
	s = strings.TrimSuffix(s, `"`)
	s = strings.ReplaceAll(s, `\"`, `"`)
	s = strings.ReplaceAll(s, `\\`, `\`)
	return s
}
