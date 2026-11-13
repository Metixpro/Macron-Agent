package updater

import (
	"archive/tar"
	"archive/zip"
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

const Repository = "Metixpro/Macron-Agent"

var ReleaseAPI = "https://api.github.com/repos/" + Repository + "/releases"

var Token = func() string { return strings.TrimSpace(os.Getenv("GITHUB_TOKEN")) }

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

type Release struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Body       string `json:"body"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		APIURL string `json:"url"`
		Size   int64  `json:"size"`
	} `json:"assets"`
}

func (r Release) Version() string { return strings.TrimPrefix(r.TagName, "v") }

func AssetName(version string) string {
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("macron-%s-%s-%s%s", strings.TrimPrefix(version, "v"), runtime.GOOS, runtime.GOARCH, ext)
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

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
			break
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
	if !api {
		req.Header.Set("Accept", "application/octet-stream")
	}
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

func downloadAsset(ctx context.Context, browserURL, apiURL string, limit int64) ([]byte, error) {
	data, err := download(ctx, browserURL, limit)
	if err == nil || !strings.HasPrefix(apiURL, ReleaseAPI+"/assets/") {
		return data, err
	}
	data, fallbackErr := download(ctx, apiURL, limit)
	if fallbackErr != nil {
		return nil, fmt.Errorf("download failed (%v); asset API: %w", err, fallbackErr)
	}
	return data, nil
}

func ChecksumOf(sums, file string) (string, bool) {
	for _, line := range strings.Split(sums, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == file && len(f[0]) == 64 {
			return strings.ToLower(f[0]), true
		}
	}
	return "", false
}

func ExtractBinary(archive []byte) ([]byte, error) {
	return extractFile(archive, exeName("macron"))
}

func extractFile(archive []byte, name string) ([]byte, error) {
	if zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive))); err == nil {
		for _, f := range zr.File {
			if !f.FileInfo().IsDir() && filepath.Base(filepath.FromSlash(f.Name)) == name {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, 200<<20))
			}
		}
		return nil, fmt.Errorf("no %s in the archive", name)
	}
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
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == name {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
}

func Install(ctx context.Context, rel *Release, step func(string)) error {
	if step == nil {
		step = func(string) {}
	}
	want := AssetName(rel.Version())
	var archiveURL, sumsURL, archiveAPI, sumsAPI string
	for _, a := range rel.Assets {
		switch a.Name {
		case want:
			archiveURL, archiveAPI = a.URL, a.APIURL
		case "SHA256SUMS":
			sumsURL, sumsAPI = a.URL, a.APIURL
		}
	}
	if archiveURL == "" || sumsURL == "" {
		return fmt.Errorf("the release has no %s (or no SHA256SUMS): nothing for %s/%s", want, runtime.GOOS, runtime.GOARCH)
	}
	step("download " + want)
	archive, err := downloadAsset(ctx, archiveURL, archiveAPI, 300<<20)
	if err != nil {
		return err
	}
	sums, err := downloadAsset(ctx, sumsURL, sumsAPI, 1<<20)
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
	if err := Swap(self, bin, rel.Version()); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		desktop := filepath.Join(filepath.Dir(self), "macron-desktop.exe")
		if _, err := os.Stat(desktop); err == nil {
			step("update the desktop app")
			data, err := extractFile(archive, "macron-desktop.exe")
			if err != nil {
				return err
			}
			return replace(desktop, data)
		}
	}
	return nil
}

func replace(target string, data []byte) error {
	next := target + ".new"
	if err := os.WriteFile(next, data, 0o755); err != nil {
		return err
	}
	old := target + ".old"
	_ = os.Remove(old)
	if err := os.Rename(target, old); err != nil {
		_ = os.Remove(next)
		return err
	}
	if err := os.Rename(next, target); err != nil {
		_ = os.Rename(old, target)
		return err
	}
	return nil
}

var Preflight = func(path, version string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil || !strings.Contains(string(out), version) {
		return fmt.Errorf("the new program does not start as version %s (%v %q): it is NOT installed", version, err, strings.TrimSpace(string(out)))
	}
	if _, err := os.Stat(config.EnvFilePath()); err != nil {
		return nil
	}
	cctx, ccancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer ccancel()
	check := exec.CommandContext(cctx, path, "--check")
	check.Env = append(os.Environ(), "MACRON_HOME="+config.Home(), "MACRON_ENV_FILE="+config.EnvFilePath())
	out, err = check.CombinedOutput()
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

func Swap(self string, bin []byte, version string) error {
	dir := filepath.Dir(self)
	stage, err := os.MkdirTemp(dir, ".macron-update-")
	if err != nil {
		return fmt.Errorf("cannot write next to the program (%s): %w — run the update as the user who owns it", dir, err)
	}
	defer os.RemoveAll(stage)
	next := filepath.Join(stage, exeName("macron"))
	if err := os.WriteFile(next, bin, 0o755); err != nil {
		return err
	}
	if err := Preflight(next, version); err != nil {
		return err
	}
	old := self + ".old"
	if runtime.GOOS == "windows" {
		_ = os.Remove(old)
		if err := os.Rename(self, old); err != nil {
			return err
		}
		if err := os.Rename(next, self); err != nil {
			_ = os.Rename(old, self)
			return err
		}
		return nil
	}
	_ = os.Remove(old)
	if err := os.Link(self, old); err != nil {
		if data, rerr := os.ReadFile(self); rerr == nil {
			_ = os.WriteFile(old, data, 0o755)
		}
	}
	if err := os.Rename(next, self); err != nil {
		return err
	}
	return nil
}

func RestartLater(self string, system bool) (string, error) {
	scope := []string{"--user"}
	if system {
		scope = nil
	}
	if p, err := exec.LookPath("systemd-run"); err == nil {
		args := append(append([]string{}, scope...), "--on-active=3", "--timer-property=AccuracySec=1s", "systemctl")
		args = append(args, scope...)
		args = append(args, "restart", config.ServiceName()+".service")
		if out, err := exec.Command(p, args...).CombinedOutput(); err == nil {
			return "systemd-run", nil
		} else {
			_ = out
		}
	}

	cmd := exec.Command("sh", "-c", "sleep 3; exec \"$0\" service restart", self)
	cmd.SysProcAttr = detached()
	if err := cmd.Start(); err != nil {
		return "", err
	}
	_ = cmd.Process.Release()
	return "detached", nil
}
