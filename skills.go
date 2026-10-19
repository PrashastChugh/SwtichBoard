package main

import (
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
	Scope       string `json:"scope"` // "global" or a project path
}

// listSkills scans the global skills folder plus every known project's
// .claude/skills folder - the same "known projects" universe the Rules and
// Skills dialogs already use, so this never needs to scan the filesystem
// for folders Switchboard has no other reason to know about.
func listSkills(projects []string) []SkillInfo {
	out := []SkillInfo{}
	h, _ := os.UserHomeDir()
	out = append(out, scanSkillDir(filepath.Join(h, ".claude", "skills"), "global")...)
	for _, p := range projects {
		if p == "" {
			continue
		}
		out = append(out, scanSkillDir(filepath.Join(p, ".claude", "skills"), p)...)
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
