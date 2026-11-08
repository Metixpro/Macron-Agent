// Package updater finds, checks and installs new releases of Macron Agent:
// «macron update» in the terminal and /update in the chat use it alike.
// Nothing is replaced until the download matches its SHA-256 sum and the new
// program starts as the version it should be; a failed step leaves the
// installed program untouched.
package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/config"
)

// Repository is where the releases are.
const Repository = "Metixpro/macron-agent"

// ReleaseAPI is the releases' API (replaced in tests).
var ReleaseAPI = "https://api.github.com/repos/" + Repository + "/releases"

// Token is a GitHub token for a higher rate limit ("" none).
var Token = func() string { return strings.TrimSpace(os.Getenv("GITHUB_TOKEN")) }

// SelfPath is the running program (replaced in tests).
var SelfPath = func() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	return self, nil
}

// Release is a published release.
type Release struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Body       string `json:"body"` // the changelog
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// Version is the release's version without its v.
func (r Release) Version() string { return strings.TrimPrefix(r.TagName, "v") }

// AssetName is the archive of a release for this machine.
func AssetName(version string) string {
	return fmt.Sprintf("macron-%s-%s-%s.tar.gz", strings.TrimPrefix(version, "v"), runtime.GOOS, runtime.GOARCH)
}

// Newer reports whether version a is newer than b (numbers split at . and -).
func Newer(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func versionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	var out []int
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '-' || r == '+' }) {
		n, err := strconv.Atoi(p)
		if err != nil {
			break // "dev", "rc1": the numbers before it decide
		}
		out = append(out, n)
	}
	return out
}

func get(ctx context.Context, url string, api bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "macron-agent/"+app.Version)
	if api {
		req.Header.Set("Accept", "application/vnd.github+json")
		if t := Token(); t != "" {
			req.Header.Set("Authorization", "Bearer "+t)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) && resp.Header.Get("X-RateLimit-Remaining") == "0" {
			when := ""
			if s, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
				when = " until " + time.Unix(s, 0).Format("15:04")
			}
			return nil, fmt.Errorf("GitHub's rate limit is spent%s (set GITHUB_TOKEN for more)", when)
		}
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	return resp, nil
}

// Latest is the newest release (tag "" ) or the release of a tag.
func Latest(ctx context.Context, tag string) (*Release, error) {
	url := ReleaseAPI + "/latest"
	if tag != "" {
		url = ReleaseAPI + "/tags/" + tag
	}
	resp, err := get(ctx, url, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var rel Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("an unreadable release: %w", err)
	}
	if rel.TagName == "" {
		return nil, errors.New("the release has no tag")
	}
	return &rel, nil
}

func download(ctx context.Context, url string, limit int64) ([]byte, error) {
	resp, err := get(ctx, url, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d MB", url, limit>>20)
	}
	return data, nil
}

// ChecksumOf finds the sum of file in a SHA256SUMS listing ("<sum>  <file>" lines).
func ChecksumOf(sums, file string) (string, bool) {
	for _, line := range strings.Split(sums, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == file && len(f[0]) == 64 {
			return strings.ToLower(f[0]), true
		}
	}
	return "", false
}

// ExtractBinary reads the program out of a release archive.
func ExtractBinary(archive []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("no macron program in the archive")
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == "macron" {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
}

// Install downloads a release, checks it against SHA256SUMS, tries the new
// program and puts it in place of the running one. step reports progress.
func Install(ctx context.Context, rel *Release, step func(string)) error {
	if step == nil {
		step = func(string) {}
	}
	want := AssetName(rel.Version())
	var archiveURL, sumsURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case want:
			archiveURL = a.URL
		case "SHA256SUMS":
			sumsURL = a.URL
		}
	}
	if archiveURL == "" || sumsURL == "" {
		return fmt.Errorf("the release has no %s (or no SHA256SUMS): nothing for %s/%s", want, runtime.GOOS, runtime.GOARCH)
	}
	step("download " + want)
	archive, err := download(ctx, archiveURL, 300<<20)
	if err != nil {
		return err
	}
	sums, err := download(ctx, sumsURL, 1<<20)
	if err != nil {
		return err
	}
	step("check SHA256SUMS")
	wantSum, found := ChecksumOf(string(sums), want)
	got := sha256.Sum256(archive)
	if !found || hex.EncodeToString(got[:]) != wantSum {
		return errors.New("the download does not match its SHA-256 sum: it is NOT installed")
	}
	bin, err := ExtractBinary(archive)
	if err != nil {
		return err
	}
	self, err := SelfPath()
	if err != nil {
		return err
	}
	step("preflight the new program")
	return Swap(self, bin, rel.Version())
}

// Preflight is run on the staged program before it replaces the running one
// (a variable for the tests): it must start as the version it should be and,
// where the agent is set up, pass «macron --check».
var Preflight = func(path, version string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil || !strings.Contains(string(out), version) {
		return fmt.Errorf("the new program does not start as version %s (%v %q): it is NOT installed", version, err, strings.TrimSpace(string(out)))
	}
	if _, err := os.Stat(config.EnvFilePath()); err != nil {
		return nil // not set up here: nothing more to try
	}
	cctx, ccancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer ccancel()
	out, err = exec.CommandContext(cctx, path, "--check").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ok "+version) {
		return fmt.Errorf("the new program does not pass «macron --check» (%v): it is NOT installed\n%s", err, lastLines(string(out), 5))
	}
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Swap puts a new program in place of the running one, after Preflight.
func Swap(self string, bin []byte, version string) error {
	dir := filepath.Dir(self)
	stage, err := os.MkdirTemp(dir, ".macron-update-")
	if err != nil {
		return fmt.Errorf("cannot write next to the program (%s): %w — run the update as the user who owns it", dir, err)
	}
	defer os.RemoveAll(stage)
	next := filepath.Join(stage, "macron")
	if err := os.WriteFile(next, bin, 0o755); err != nil {
		return err
	}
	if err := Preflight(next, version); err != nil {
		return err
	}
	old := self + ".old"
	_ = os.Remove(old)
	if err := os.Link(self, old); err != nil { // keep the old one; the running program is not moved
		if data, rerr := os.ReadFile(self); rerr == nil {
			_ = os.WriteFile(old, data, 0o755)
		}
	}
	if err := os.Rename(next, self); err != nil { // atomic on the same filesystem
		return err
	}
	return nil
}

// RestartLater restarts the service a few seconds from now, outside this
// process, so a running agent can finish its answer first and come back as
// the new version. It tells how.
func RestartLater(self string, system bool) (string, error) {
	scope := []string{"--user"}
	if system {
		scope = nil
	}
	if p, err := exec.LookPath("systemd-run"); err == nil {
		args := append(append([]string{}, scope...), "--on-active=3", "--timer-property=AccuracySec=1s", "systemctl")
		args = append(args, scope...)
		args = append(args, "restart", "macron.service")
		if out, err := exec.Command(p, args...).CombinedOutput(); err == nil {
			return "systemd-run", nil
		} else {
			_ = out
		}
	}
	// No systemd-run: a detached shell restarts it after the answer.
	cmd := exec.Command("sh", "-c", "sleep 3; exec \"$0\" service restart", self)
	cmd.SysProcAttr = detached()
	if err := cmd.Start(); err != nil {
		return "", err
	}
	_ = cmd.Process.Release()
	return "detached", nil
}
