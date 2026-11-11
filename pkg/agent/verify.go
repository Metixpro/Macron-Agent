package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var verifiableExts = map[string]bool{
	".go":   true,
	".py":   true,
	".sh":   true,
	".bash": true,
	".js":   true,
	".ts":   true,
	".tsx":  true,
	".jsx":  true,
	".c":    true,
	".cpp":  true,
	".rs":   true,
	".sql":  true,
	".php":  true,
	".rb":   true,
}

var doneClaimRe = regexp.MustCompile(`(?i)(?:^|[^\w])(?:done|finished|completed|all set|fixed|resolved|ready|implemented|created|written|انجام شد|تمام شد|حل شد|ساخته شد|نوشته شد|آماده است|پیاده‌سازی شد)(?:[^\w]|$)`)

type VerifyGate struct {
	modifiedFiles map[string]int
	verifiedFiles map[string]bool
	lastExecRound int
	round         int
	nudges        int
}

func newVerifyGate() *VerifyGate {
	return &VerifyGate{
		modifiedFiles: make(map[string]int),
		verifiedFiles: make(map[string]bool),
	}
}

func (v *VerifyGate) SetRound(round int) {
	v.round = round
}

func (v *VerifyGate) RecordFileModification(path string) {
	ext := strings.ToLower(filepath.Ext(path))
	if verifiableExts[ext] {
		v.modifiedFiles[path] = v.round
		delete(v.verifiedFiles, path)
		v.nudges = 0
	}
}

func (v *VerifyGate) ResetNudges() {
	v.nudges = 0
}

func (v *VerifyGate) RecordExecution(toolName, cmd string, exitCode int) {
	if exitCode == 0 {
		v.lastExecRound = v.round
		for path := range v.modifiedFiles {
			base := filepath.Base(path)
			if strings.Contains(cmd, path) || strings.Contains(cmd, base) {
				v.verifiedFiles[path] = true
			}
		}
	}
}

func (v *VerifyGate) NeedsVerification(answer string) (bool, string) {
	if len(v.modifiedFiles) == 0 || v.nudges >= 2 {
		return false, ""
	}
	if !doneClaimRe.MatchString(answer) && !claimsDone(answer) {
		return false, ""
	}

	var unverified []string
	for path, modRound := range v.modifiedFiles {
		if !v.verifiedFiles[path] && v.lastExecRound < modRound {
			unverified = append(unverified, filepath.Base(path))
		}
	}
	if len(unverified) == 0 {
		return false, ""
	}

	v.nudges++
	prompt := fmt.Sprintf("[check before you finish] You changed %s but nothing has run since. Check it the safe way first (its tests, a build or syntax check, or a run that changes nothing the user did not ask to change), fix what fails, then answer. If it cannot be checked here, say so in the answer instead.",
		strings.Join(unverified, ", "))
	return true, prompt
}

func extractFilePath(args string) string {
	var m map[string]any
	if json.Unmarshal([]byte(args), &m) == nil {
		for _, k := range []string{"path", "file", "filename", "name", "target"} {
			if s, ok := m[k].(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}
