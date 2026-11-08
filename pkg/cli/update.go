package cli

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
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
)

// Repository is where the releases are published.
const Repository = "Metixpro/macron-agent"

// releaseAPI answers with the newest release (replaced in tests).
var releaseAPI = "https://api.github.com/repos/" + Repository + "/releases"

type release struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// selfPath is the running program (replaced in tests).
var selfPath = func() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	return self, nil
}

// assetName is the archive of a release for this machine.
func assetName(version string) string {
	return fmt.Sprintf("macron-%s-%s-%s.tar.gz", strings.TrimPrefix(version, "v"), runtime.GOOS, runtime.GOARCH)
}

// newer reports whether version a is newer than b (numbers split at . and -).
func newer(a, b string) bool {
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
	var out []int
	for _, f := range strings.FieldsFunc(strings.TrimPrefix(v, "v"), func(r rune) bool { return r == '.' || r == '-' || r == '+' }) {
		n, err := strconv.Atoi(f)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}

func fetchJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "macron-agent/"+app.Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", url, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v)
}

func download(ctx context.Context, url string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "macron-agent/"+app.Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is larger than %d MB", url, max>>20)
	}
	return data, nil
}

// checksumOf finds the sum of file in a SHA256SUMS listing ("<sum>  <file>" lines).
func checksumOf(sums, file string) (string, bool) {
	for _, line := range strings.Split(sums, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == file && len(f[0]) == 64 {
			return strings.ToLower(f[0]), true
		}
	}
	return "", false
}

// extractBinary reads the program out of a release archive.
func extractBinary(archive []byte) ([]byte, error) {
	zr, err := gzip.NewReader(strings.NewReader(string(archive)))
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

func update(io *IO, args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	check := fs.Bool("check", false, "only say whether a newer release exists")
	tag := fs.String("tag", "", "install this release (e.g. v1.2.0) instead of the newest")
	force := fs.Bool("force", false, "install even when this is the newest release")
	noRestart := fs.Bool("no-restart", false, "do not restart the service afterwards")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var rel release
	url := releaseAPI + "/latest"
	if *tag != "" {
		url = releaseAPI + "/tags/" + *tag
	}
	if err := fetchJSON(ctx, url, &rel); err != nil {
		io.printf("❌ cannot look for releases: %v\n", err)
		return 1
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	if !newer(latest, app.Version) && !*force && *tag == "" {
		io.printf("✅ You have the newest release (%s).\n", app.Version)
		return 0
	}
	io.printf("A new release: %s (you have %s) %s\n", latest, app.Version, rel.HTMLURL)
	if *check {
		return 0
	}
	want := assetName(latest)
	var archiveURL, sumsURL string
	var size int64
	for _, a := range rel.Assets {
		switch a.Name {
		case want:
			archiveURL, size = a.URL, a.Size
		case "SHA256SUMS":
			sumsURL = a.URL
		}
	}
	if archiveURL == "" || sumsURL == "" {
		io.printf("❌ the release has no %s (or no SHA256SUMS): nothing for %s/%s\n", want, runtime.GOOS, runtime.GOARCH)
		return 1
	}
	io.printf("Downloading %s (%s)…\n", want, humanBytes(size))
	archive, err := download(ctx, archiveURL, 300<<20)
	if err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	sums, err := download(ctx, sumsURL, 1<<20)
	if err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	wantSum, found := checksumOf(string(sums), want)
	got := sha256.Sum256(archive)
	if !found || hex.EncodeToString(got[:]) != wantSum {
		io.println("❌ the download does not match its SHA-256 sum: it is NOT installed")
		return 1
	}
	bin, err := extractBinary(archive)
	if err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	self, err := selfPath()
	if err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	if err := swapBinary(self, bin, latest); err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	io.printf("✅ Updated to %s. (The old program is kept next to it as macron.old.)\n", latest)
	if !*noRestart && serviceSupported() {
		u, _ := currentUnit()
		if _, err := os.Stat(unitPath(u)); err == nil || runtime.GOOS == "darwin" {
			io.println("Restarting the service…")
			return serviceControl(io, "restart")
		}
	}
	io.println("Restart the agent to use it (macron service restart, or stop and run macron again).")
	return 0
}

// swapBinary puts a new program in place of the running one, after checking
// that it starts and says the version it should be.
func swapBinary(self string, bin []byte, wantVersion string) error {
	next := self + ".new"
	if err := os.WriteFile(next, bin, 0o755); err != nil {
		return fmt.Errorf("cannot write next to the program (%s): %w — run the update as the user who owns it", filepath.Dir(self), err)
	}
	defer os.Remove(next)
	out, err := exec.Command(next, "version").Output()
	if err != nil || !strings.Contains(string(out), wantVersion) {
		return fmt.Errorf("the new program does not start as version %s (%v %q): it is NOT installed", wantVersion, err, strings.TrimSpace(string(out)))
	}
	old := self + ".old"
	_ = os.Remove(old)
	if err := os.Rename(self, old); err != nil {
		return err
	}
	if err := os.Rename(next, self); err != nil {
		_ = os.Rename(old, self) // put the old one back
		return err
	}
	return nil
}
