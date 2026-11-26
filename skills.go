package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
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
	return writeFileAtomic(path, []byte(body))
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
	return os.RemoveAll(filepath.Dir(path))
}

type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Scope       string `json:"scope"`    // "global", "plugin:<name>" or a project path
	ReadOnly    bool   `json:"readonly"` // plugin or synced skills: listed here, managed elsewhere
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
				sk.ReadOnly = true
				out = append(out, sk)
			}
		}
	}
	return out
}

// scanSkillTree finds SKILL.md files up to depth folders down (synced skills
// sit one or two folders below ~/.claude/skills).
func scanSkillTree(dir, scope string, depth int) []SkillInfo {
	if name, desc, ok := parseSkillFrontmatter(filepath.Join(dir, "SKILL.md")); ok {
		return []SkillInfo{{Name: name, Description: desc, Scope: scope}}
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
		name, desc, ok := parseSkillFrontmatter(filepath.Join(dir, e.Name(), "SKILL.md"))
		if !ok {
			continue
		}
		out = append(out, SkillInfo{Name: name, Description: desc, Scope: scope})
	}
	return out
}

// parseSkillFrontmatter pulls name/description out of a SKILL.md's YAML
// frontmatter. Deliberately a simple line scan, not a real YAML parser -
// createSkill is the only thing writing this format for Switchboard-created
// skills, so this is good enough to display; it won't handle hand-edited
// multi-line YAML correctly.
func parseSkillFrontmatter(path string) (name, description string, ok bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	inFrontmatter := false
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
		if v, found := strings.CutPrefix(trimmed, "name:"); found {
			name = unquoteYAML(strings.TrimSpace(v))
		}
		if v, found := strings.CutPrefix(trimmed, "description:"); found {
			description = unquoteYAML(strings.TrimSpace(v))
		}
	}
	if name == "" {
		return "", "", false
	}
	return name, description, true
}

func unquoteYAML(s string) string {
	s = strings.TrimPrefix(s, `"`)
	s = strings.TrimSuffix(s, `"`)
	s = strings.ReplaceAll(s, `\"`, `"`)
	s = strings.ReplaceAll(s, `\\`, `\`)
	return s
}
