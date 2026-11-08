package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

// apply_patch: a unified diff (git diff's format) changes many files in one
// call, new and deleted ones too, all or none (git apply, as a plain patch
// tool: the paths are relative to the folder given, whatever repository it
// is in). The code files it touched get a syntax check, as after edit_file.

const ToolApplyPatch = "apply_patch"

var patchPathRe = regexp.MustCompile(`(?m)^(?:---|\+\+\+) (\S+)`)

func (r *Registry) applyPatch(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	patch := rawStrArg(args, "patch")
	if strings.TrimSpace(patch) == "" {
		return "error: patch is required (a unified diff)", nil
	}
	if !strings.HasSuffix(patch, "\n") {
		patch += "\n"
	}
	dir := strings.Trim(strings.TrimPrefix(strArg(args, "path"), machine.MountPoint), "/")
	if dir == "" {
		dir = "."
	}
	if err := checkPatchDir(dir); err != nil {
		return "error: " + err.Error(), nil
	}
	if info, err := r.Machine.Stat(sessionID, dir); err != nil || !info.IsDir() {
		return "error: no folder " + dir, nil
	}
	strip, files := patchFiles(patch)
	if len(files) == 0 {
		return "error: no file headers (--- a/file, +++ b/file) in the patch", nil
	}
	id := make([]byte, 6)
	_, _ = rand.Read(id)
	name := scratchDir + "/patch-" + hex.EncodeToString(id) + ".diff"
	if err := r.Machine.WriteFile(sessionID, name, []byte(patch)); err != nil {
		return "error: " + friendlyFSError(err), nil
	}
	defer func() { _ = r.Machine.Remove(sessionID, name) }()
	// As written first; then with the line counts taken from the lines
	// themselves (a model's are often off), then hunks without context
	// lines too. When none applies, the errors are the strictest one's.
	script := `cd "$1" || exit 1
export GIT_DIR=/nonexistent
for how in "" "--recount" "--recount --unidiff-zero"; do
	if git apply --check $how -p"$2" "$3" 2>/dev/null; then exec git apply --verbose --whitespace=nowarn $how -p"$2" "$3"; fi
done
exec git apply --verbose -p"$2" "$3"`
	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: []string{"bash", "-c", script, "patch", dir, strip, machine.MountPoint + "/" + name},
		Timeout: 60 * time.Second, MergeOutput: true})
	out := terminalTranscript("git apply "+path.Base(name), res, false)
	if res.ExitCode != 0 {
		return out + "\nNothing was changed (a patch applies whole or not at all). Read the files again and make the context lines match, " +
			"or use " + ToolEditFile + ".", nil
	}
	var notes []string
	for _, f := range files {
		if note := r.syntaxNote(ctx, sessionID, path.Join(dir, f)); note != "" {
			notes = append(notes, note)
		}
	}
	return fmt.Sprintf("%s\nApplied to %d files: %s%s", out, len(files), strings.Join(files, ", "), strings.Join(notes, "")), nil
}

// patchFiles reads a patch's files and its strip level: 1 when every path
// has git's a/ and b/, else 0.
func patchFiles(patch string) (strip string, files []string) {
	strip = "1"
	seen := map[string]bool{}
	for _, m := range patchPathRe.FindAllStringSubmatch(patch, -1) {
		p := m[1]
		if p == "/dev/null" {
			continue
		}
		if !strings.HasPrefix(p, "a/") && !strings.HasPrefix(p, "b/") {
			strip = "0"
		}
	}
	for _, m := range patchPathRe.FindAllStringSubmatch(patch, -1) {
		p := m[1]
		if p == "/dev/null" {
			continue
		}
		if strip == "1" {
			p = p[2:]
		}
		if !seen[p] {
			seen[p] = true
			files = append(files, p)
		}
	}
	return strip, files
}

func checkPatchDir(dir string) error {
	for _, part := range strings.Split(dir, "/") {
		if part == ".." {
			return fmt.Errorf("the folder must be inside the workspace")
		}
	}
	return nil
}
