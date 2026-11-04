package main

import (
	"fmt"
	"strings"
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
	detail, search, reference string
}

func (s *skillOption) prepare() {
	s.detail = s.Interface.ShortDescription
	if s.detail == "" {
		s.detail = s.Description
	}
	s.search = strings.ToLower(s.Name + "\n" + s.Interface.DisplayName + "\n" + s.Description + "\n" + s.detail)
	s.reference = markdownReference(s.Name, s.Path)
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
