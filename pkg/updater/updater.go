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
	"github.com/Metixpro/macron-agent/pkg/netclient"
)

var (
	updaterAPIClient = netclient.NewClient(netclient.Options{
		Timeout:       30 * time.Second,
		HeaderTimeout: 20 * time.Second,
		UseEnvProxy:   true,
	})
	updaterDownloadClient = netclient.NewClient(netclient.Options{
		NoTotalTimeout: true,
		HeaderTimeout:  30 * time.Second,
		BodyIdle:       60 * time.Second,
		UseEnvProxy:    true,
	})
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
	client := updaterDownloadClient
	if !api {
		req.Header.Set("Accept", "application/octet-stream")
	}
	if api {
		client = updaterAPIClient
		req.Header.Set("Accept", "application/vnd.github+json")
		if t := Token(); t != "" {
			req.Header.Set("Authorization", "Bearer "+t)
		}
	}
	resp, err := client.Do(req)
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

type Progress struct {
	Phase      string
	Label      string
	Downloaded int64
	Total      int64
	Percent    int
	SpeedBps   int64
}

func downloadWithProgress(ctx context.Context, url string, limit, knownTotal int64, onBytes func(downloaded, total, speedBps int64)) ([]byte, error) {
	resp, err := get(ctx, url, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	total := resp.ContentLength
	if total <= 0 {
		total = knownTotal
	}
	var buf bytes.Buffer
	if total > 0 && total <= limit {
		buf.Grow(int(total))
	}
	chunk := make([]byte, 32<<10)
	started := time.Now()
	lastEmit := time.Time{}
	var downloaded int64

	for {
		n, rerr := resp.Body.Read(chunk)
		if n > 0 {
			downloaded += int64(n)
			if downloaded > limit {
				return nil, fmt.Errorf("%s is larger than %d MB", url, limit>>20)
			}
			_, _ = buf.Write(chunk[:n])
			if onBytes != nil && (lastEmit.IsZero() || time.Since(lastEmit) >= 100*time.Millisecond) {
				elapsed := time.Since(started).Seconds()
				var bps int64
				if elapsed > 0.05 {
					bps = int64(float64(downloaded) / elapsed)
				}
				onBytes(downloaded, total, bps)
				lastEmit = time.Now()
			}
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	if onBytes != nil {
		elapsed := time.Since(started).Seconds()
		var bps int64
		if elapsed > 0.02 {
			bps = int64(float64(downloaded) / elapsed)
		}
		if total <= 0 {
			total = downloaded
		}
		onBytes(downloaded, total, bps)
	}
	return buf.Bytes(), nil
}

func downloadAssetWithProgress(ctx context.Context, browserURL, apiURL string, limit, knownTotal int64, onBytes func(downloaded, total, speedBps int64)) ([]byte, error) {
	data, err := downloadWithProgress(ctx, browserURL, limit, knownTotal, onBytes)
	if err == nil || !strings.HasPrefix(apiURL, ReleaseAPI+"/assets/") {
		return data, err
	}
	data, fallbackErr := downloadWithProgress(ctx, apiURL, limit, knownTotal, onBytes)
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
	var last string
	return InstallWithProgress(ctx, rel, func(p Progress) {
		if step == nil || p.Label == "" || p.Label == last {
			return
		}
		last = p.Label
		step(p.Label)
	})
}

func InstallWithProgress(ctx context.Context, rel *Release, onProgress func(Progress)) error {
	emit := func(p Progress) {
		if onProgress != nil {
			onProgress(p)
		}
	}
	want := AssetName(rel.Version())
	var archiveURL, sumsURL, archiveAPI, sumsAPI string
	var archiveSize int64
	for _, a := range rel.Assets {
		switch a.Name {
		case want:
			archiveURL, archiveAPI, archiveSize = a.URL, a.APIURL, a.Size
		case "SHA256SUMS":
			sumsURL, sumsAPI = a.URL, a.APIURL
		}
	}
	if archiveURL == "" || sumsURL == "" {
		return fmt.Errorf("the release has no %s (or no SHA256SUMS): nothing for %s/%s", want, runtime.GOOS, runtime.GOARCH)
	}

	emit(Progress{Phase: "downloading", Label: "Downloading update", Total: archiveSize, Percent: 2})
	archive, err := downloadAssetWithProgress(ctx, archiveURL, archiveAPI, 300<<20, archiveSize, func(done, total, bps int64) {
		pct := 5
		if total > 0 {
			pct = 2 + int(done*80/total)
			if pct > 82 {
				pct = 82
			}
		}
		emit(Progress{
			Phase:      "downloading",
			Label:      "Downloading update",
			Downloaded: done,
			Total:      total,
			Percent:    pct,
			SpeedBps:   bps,
		})
	})
	if err != nil {
		return err
	}

	emit(Progress{Phase: "verifying", Label: "Verifying SHA-256", Downloaded: int64(len(archive)), Total: int64(len(archive)), Percent: 85})
	sums, err := downloadAssetWithProgress(ctx, sumsURL, sumsAPI, 1<<20, 0, nil)
	if err != nil {
		return err
	}
	wantSum, found := ChecksumOf(string(sums), want)
	got := sha256.Sum256(archive)
	if !found || hex.EncodeToString(got[:]) != wantSum {
		return errors.New("the download does not match its SHA-256 sum: it is NOT installed")
	}

	emit(Progress{Phase: "extracting", Label: "Extracting package", Downloaded: int64(len(archive)), Total: int64(len(archive)), Percent: 90})
	bin, err := ExtractBinary(archive)
	if err != nil {
		return err
	}
	self, err := SelfPath()
	if err != nil {
		return err
	}
	reportInstall := func(phase, label string, percent int) {
		size := int64(len(archive))
		emit(Progress{Phase: phase, Label: label, Downloaded: size, Total: size, Percent: percent})
	}
	if err := installBinaries(ctx, self, bin, archive, rel.Version(), reportInstall); err != nil {
		return err
	}
	reportInstall("done", "Ready to restart", 100)
	return nil
}

var verifyVersion = func(path, version string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	versionCmd := exec.CommandContext(ctx, path, "version")
	if runtime.GOOS == "windows" {
		versionCmd.SysProcAttr = detached()
	}
	out, err := versionCmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "macron "+version {
		return fmt.Errorf("the new program does not start as version %s (%v %q): it is NOT installed", version, err, strings.TrimSpace(string(out)))
	}
	return nil
}

var Preflight = func(path, version string) error {
	if err := verifyVersion(path, version); err != nil {
		return err
	}
	if _, err := os.Stat(config.EnvFilePath()); err != nil {
		return nil
	}
	cctx, ccancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer ccancel()
	check := exec.CommandContext(cctx, path, "--check")
	if runtime.GOOS == "windows" {
		check.SysProcAttr = detached()
	}
	check.Env = append(os.Environ(), "MACRON_HOME="+config.Home(), "MACRON_ENV_FILE="+config.EnvFilePath())
	out, err := check.CombinedOutput()
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
	stage, err := makeStage(filepath.Dir(self))
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	next := filepath.Join(stage, exeName("macron"))
	if err := os.WriteFile(next, bin, 0o755); err != nil {
		return err
	}
	if err := Preflight(next, version); err != nil {
		return err
	}
	return swapBinary(self, next)
}

func makeStage(dir string) (string, error) {
	stage, err := os.MkdirTemp(dir, ".macron-update-")
	if err != nil {
		return "", fmt.Errorf("cannot write next to the program (%s): %w — run the update as the user who owns it", dir, err)
	}
	return stage, nil
}
