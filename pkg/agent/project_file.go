package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

const (
	ProjectFileName = "PROJECT.md"
	ProjectNoteTag  = "<!-- macron:project:pinned -->"
)

type ProjectFileEntry struct {
	Path        string `json:"path"`
	Description string `json:"description"`
}

// ProjectFile holds structured project architecture, status, and decisions that must survive context compaction.
type ProjectFile struct {
	Title         string             `json:"title"`
	Goal          string             `json:"goal"`
	TechStack     string             `json:"tech_stack,omitempty"`
	Architecture  []string           `json:"architecture,omitempty"`
	Files         []ProjectFileEntry `json:"files,omitempty"`
	Milestones    []PlanItem         `json:"milestones,omitempty"`
	VerifiedFacts []string           `json:"verified_facts,omitempty"`
}

func projectFilePath(ws string) string {
	return filepath.Join(ws, ProjectFileName)
}

func (p *ProjectFile) Render() string {
	var sb strings.Builder
	title := p.Title
	if title == "" {
		title = "Current Project"
	}
	sb.WriteString(fmt.Sprintf("# Project: %s\n\n", title))
	if p.Goal != "" {
		sb.WriteString("## Goal & Overview\n" + p.Goal + "\n\n")
	}
	if p.TechStack != "" {
		sb.WriteString("## Tech Stack & Environment\n" + p.TechStack + "\n\n")
	}
	if len(p.Architecture) > 0 {
		sb.WriteString("## Architecture & Key Decisions\n")
		for _, dec := range p.Architecture {
			sb.WriteString("- " + dec + "\n")
		}
		sb.WriteString("\n")
	}
	if len(p.Files) > 0 {
		sb.WriteString("## File & Component Registry\n")
		for _, f := range p.Files {
			sb.WriteString(fmt.Sprintf("- `%s`: %s\n", f.Path, f.Description))
		}
		sb.WriteString("\n")
	}
	if len(p.Milestones) > 0 {
		sb.WriteString("## Milestones & Tasks\n")
		for _, m := range p.Milestones {
			mark := " "
			if m.Status == PlanDone {
				mark = "x"
			} else if m.Status == PlanInProgress {
				mark = "/"
			}
			sb.WriteString(fmt.Sprintf("- [%s] %s\n", mark, m.Text))
		}
		sb.WriteString("\n")
	}
	if len(p.VerifiedFacts) > 0 {
		sb.WriteString("## Verified Specifications\n")
		for _, fact := range p.VerifiedFacts {
			sb.WriteString("- " + fact + "\n")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func SaveProjectFile(ws string, pf *ProjectFile) error {
	if ws == "" || pf == nil {
		return nil
	}
	content := ProjectNoteTag + "\n" + pf.Render()
	return os.WriteFile(projectFilePath(ws), []byte(content), 0o644)
}

func LoadProjectFile(ws string) (*ProjectFile, error) {
	if ws == "" {
		return nil, nil
	}
	b, err := os.ReadFile(projectFilePath(ws))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return ParseProjectFile(string(b)), nil
}

func ParseProjectFile(content string) *ProjectFile {
	pf := &ProjectFile{}
	lines := strings.Split(content, "\n")
	currentSection := ""

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || line == ProjectNoteTag {
			continue
		}
		if strings.HasPrefix(line, "# Project:") {
			pf.Title = strings.TrimSpace(strings.TrimPrefix(line, "# Project:"))
			continue
		}
		if strings.HasPrefix(line, "## ") {
			currentSection = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "## ")))
			continue
		}
		switch {
		case strings.Contains(currentSection, "goal"):
			if pf.Goal == "" {
				pf.Goal = line
			} else {
				pf.Goal += "\n" + line
			}
		case strings.Contains(currentSection, "tech"):
			if pf.TechStack == "" {
				pf.TechStack = line
			} else {
				pf.TechStack += "\n" + line
			}
		case strings.Contains(currentSection, "architecture") || strings.Contains(currentSection, "decision"):
			if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
				pf.Architecture = append(pf.Architecture, strings.TrimSpace(line[2:]))
			}
		case strings.Contains(currentSection, "file") || strings.Contains(currentSection, "registry"):
			if strings.HasPrefix(line, "- `") {
				rem := line[3:]
				if idx := strings.Index(rem, "`"); idx > 0 {
					p := rem[:idx]
					desc := strings.TrimSpace(strings.TrimPrefix(rem[idx+1:], ":"))
					pf.Files = append(pf.Files, ProjectFileEntry{Path: p, Description: desc})
				}
			}
		case strings.Contains(currentSection, "milestone") || strings.Contains(currentSection, "task"):
			if strings.HasPrefix(line, "- [") && len(line) >= 6 {
				mark := line[3:4]
				text := strings.TrimSpace(line[5:])
				status := PlanPending
				if mark == "x" || mark == "X" {
					status = PlanDone
				} else if mark == "/" {
					status = PlanInProgress
				}
				pf.Milestones = append(pf.Milestones, PlanItem{Text: text, Status: status})
			}
		case strings.Contains(currentSection, "verified") || strings.Contains(currentSection, "spec"):
			if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
				pf.VerifiedFacts = append(pf.VerifiedFacts, strings.TrimSpace(line[2:]))
			}
		}
	}
	return pf
}

// PreserveProjectAcrossCompaction checks if PROJECT.md exists in ws and injects its core architecture
// and status into the compacted prompt, ensuring knowledge survives context compaction.
func PreserveProjectAcrossCompaction(ws string, messages []llm.Message) []llm.Message {
	pf, err := LoadProjectFile(ws)
	if err != nil || pf == nil {
		return messages
	}
	summary := pf.Render()
	if strings.TrimSpace(summary) == "" {
		return messages
	}

	note := llm.Message{
		Role:    "user",
		Content: "[Pinned Project State — Preserved across context compaction]\n" + summary + "\n(Continue executing according to this project architecture and milestones.)",
	}

	// Insert right after the system prompt or after the compaction summary
	if len(messages) > 1 {
		res := make([]llm.Message, 0, len(messages)+1)
		res = append(res, messages[0])
		res = append(res, note)
		res = append(res, messages[1:]...)
		return res
	}
	return append(messages, note)
}
