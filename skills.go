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
