package main

import (
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

type skillOption struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Enabled     bool   `json:"enabled"`
	Interface   struct {
		DisplayName      string `json:"displayName"`
		ShortDescription string `json:"shortDescription"`
	} `json:"interface"`
	detail, search, reference, display string
}

func (s *skillOption) prepare() {
	s.display = s.Interface.DisplayName
	if s.display == "" {
		var b strings.Builder
		upper := true
		for _, r := range s.Name {
			if r == '-' || r == '_' {
				r = ' '
			}
			if upper {
				r = unicode.ToUpper(r)
			}
			b.WriteRune(r)
			upper = unicode.IsSpace(r)
		}
		s.display = b.String()
	}
	s.detail = s.Interface.ShortDescription
	if s.detail == "" {
		s.detail = s.Description
	}
	s.search = strings.ToLower(s.Name + "\n" + s.Interface.DisplayName + "\n" + s.Description + "\n" + s.detail)
	path := strings.ReplaceAll(url.PathEscape(filepath.ToSlash(s.Path)), "%2F", "/")
	s.reference = "[$" + s.Name + "](" + path + ")"
}

// Keep only selection metadata, independent of the cached skill catalog.
type selectedSkill struct{ name, path, label, reference string }

func (a *agent) addSkill(skill *skillOption) {
	key := a.attachmentScope()
	for _, selected := range a.skills[key] {
		if selected.path == skill.Path {
			return
		}
	}
	if a.skills == nil {
		a.skills = make(map[attachmentScope][]selectedSkill)
	}
	a.skills[key] = append(a.skills[key], selectedSkill{skill.Name, skill.Path, skill.display, skill.reference})
	a.workspaceDirty = true
}

func (a *agent) removeSkill(index string) {
	n, err := strconv.Atoi(index)
	key := a.attachmentScope()
	skills := a.skills[key]
	if err != nil || n < 0 || n >= len(skills) {
		return
	}
	skills = slices.Delete(skills, n, n+1)
	if len(skills) == 0 {
		delete(a.skills, key)
	} else {
		a.skills[key] = skills
	}
	a.workspaceDirty = true
}

func skillPrompt(prompt string, skills []selectedSkill) string {
	if len(skills) == 0 {
		return prompt
	}
	var b strings.Builder
	for _, skill := range skills {
		b.WriteString(skill.reference)
		b.WriteByte('\n')
	}
	b.WriteString(prompt)
	return b.String()
}

func (a *agent) loadSkills() error {
	if err := a.connect(); err != nil {
		return err
	}
	root := a.context.ProjectPath
	result, err := call[struct {
		Data []struct {
			Cwd    string        `json:"cwd"`
			Skills []skillOption `json:"skills"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		} `json:"data"`
	}](a.client, "skills/list", struct {
		Cwds [1]string `json:"cwds"`
	}{[1]string{root}})
	if err != nil {
		return err
	}
	p := &a.completion
	if p.kind != '/' || a.context.ProjectPath != root {
		return nil
	}
	clear(p.skills)
	p.skills = p.skills[:0]
	for _, entry := range result.Data {
		if entry.Cwd != root {
			continue
		}
		for _, skill := range entry.Skills {
			if !skill.Enabled || skill.Name == "" || skill.Path == "" {
				continue
			}
			skill.prepare()
			p.skills = append(p.skills, skill)
		}
		if len(p.skills) == 0 && len(entry.Errors) > 0 {
			return fmt.Errorf("%s", entry.Errors[0].Message)
		}
	}
	p.skillsLoaded = true
	return nil
}

// Send canonical references together with explicit skill inputs.
func appendSkillInputs(dst []input, prompt string) []input {
	for {
		_, rest, found := strings.Cut(prompt, "[$")
		if !found {
			return dst
		}
		prompt = rest
		name, rest, found := strings.Cut(rest, "](")
		if !found || name == "" || strings.IndexFunc(name, unicode.IsSpace) >= 0 || strings.ContainsAny(name, "[]") {
			continue
		}
		path, rest, found := strings.Cut(rest, ")")
		if !found {
			continue
		}
		prompt = rest
		path, err := url.PathUnescape(path)
		path = filepath.FromSlash(path)
		if err != nil || !filepath.IsAbs(path) || filepath.Base(path) != "SKILL.md" {
			continue
		}
		duplicate := false
		for _, item := range dst {
			if item.Type == "skill" && item.Path == path {
				duplicate = true
				break
			}
		}
		if !duplicate {
			dst = append(dst, input{Type: "skill", Name: name, Path: path})
		}
	}
}
