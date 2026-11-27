package desk

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestProjectFileAndResolveInsideBlockSymlinkEscape(t *testing.T) {
	outsideDir := t.TempDir()
	secretFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("topsecret"), 0o600); err != nil {
		t.Fatal(err)
	}

	srv, h, pid := newFeatureTestServer(t)
	linkPath := filepath.Join(h.projDir, "escape.txt")
	if err := os.Symlink(secretFile, linkPath); err != nil {
		t.Skipf("symlink creation not permitted in test environment: %v", err)
	}

	if _, _, err := resolveInside(h.projDir, "escape.txt"); err == nil {
		t.Fatal("resolveInside allowed symlink escaping project root")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+pid+"/file?name=escape.txt", nil)
	req.SetPathValue("id", pid)
	w := httptest.NewRecorder()
	srv.fileAPI(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("fileAPI symlink escape status = %d, want 403 (body=%s)", w.Code, w.Body.String())
	}
}

func TestSafeRevealExtAllowlist(t *testing.T) {
	for _, unsafe := range []string{".exe", ".ps1", ".lnk", ".bat", ".cmd", ".svg", ".html", ".htm", ".hta", ".js", ".py"} {
		if safeRevealExt[unsafe] {
			t.Fatalf("unsafe extension %q must not be in safeRevealExt", unsafe)
		}
	}
	for _, safe := range []string{".png", ".jpg", ".pdf", ".txt", ".md", ".csv", ".json", ".mp4"} {
		if !safeRevealExt[safe] {
			t.Fatalf("safe extension %q missing from safeRevealExt", safe)
		}
	}
}

func TestMarkdownSanitizesLinksAndUNC(t *testing.T) {
	if _, ok := fileRef(`\\evil\share\file.txt`); ok {
		t.Fatal("fileRef accepted UNC path")
	}
	if !safeHTMLHref("https://example.com/docs") || !safeHTMLHref("mailto:a@b.com") || !safeHTMLHref("#sec") {
		t.Fatal("safeHTMLHref rejected valid link")
	}
	for _, bad := range []string{"javascript:alert(1)", "vbscript:msgbox(1)", "data:text/html,<script>alert(1)</script>"} {
		if safeHTMLHref(bad) {
			t.Fatalf("safeHTMLHref allowed %q", bad)
		}
	}
	rendered := renderMarkdown("[click](javascript:alert(1)) [ok](https://example.com)")
	if strings.Contains(strings.ToLower(rendered), "javascript:") {
		t.Fatalf("renderMarkdown kept javascript: URI: %s", rendered)
	}
	if !strings.Contains(rendered, `href="https://example.com"`) {
		t.Fatalf("renderMarkdown broke valid link: %s", rendered)
	}
}

func TestGuardOriginSecFetchAndCSP(t *testing.T) {
	s := &Server{token: "local-test-token"}
	handler := s.guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))

	// Cross-site header blocked
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/state", nil)
	r.Host = "127.0.0.1:8080"
	r.Header.Set("Authorization", "Bearer "+s.token)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("Sec-Fetch-Site cross-site status = %d, want 403", w.Code)
	}

	// Mismatched localhost port Origin blocked
	r = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/mode", nil)
	r.Host = "127.0.0.1:8080"
	r.Header.Set("Authorization", "Bearer "+s.token)
	r.Header.Set("X-Macron", "1")
	r.Header.Set("Origin", "http://127.0.0.1:3000")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("mismatched Origin status = %d, want 403", w.Code)
	}

	// Matching Origin allowed + strict CSP
	r = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/state", nil)
	r.Host = "127.0.0.1:8080"
	r.Header.Set("Authorization", "Bearer "+s.token)
	r.Header.Set("Origin", "http://127.0.0.1:8080")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("same-origin request status = %d, want 204", w.Code)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src 'self' data:;") || strings.Contains(csp, "https:") {
		t.Fatalf("unexpected CSP header: %q", csp)
	}
}

func TestThreadsValidationAndAtomicSave(t *testing.T) {
	srv, _, pid := newFeatureTestServer(t)
	p := srv.project(pid)
	if p == nil {
		t.Fatal("missing project")
	}
	if err := p.switchThread("../evil"); err == nil {
		t.Fatal("switchThread accepted path traversal id")
	}
	if err := p.deleteThread("../../etc/passwd"); err == nil {
		t.Fatal("deleteThread accepted path traversal id")
	}
	sess, err := p.session()
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.blocks = append(p.blocks, Block{Kind: "user", Text: "first"})
	p.mu.Unlock()
	th := p.startNewThread(sess)
	if !validThreadID(th.ID) {
		t.Fatalf("generated thread id invalid: %q", th.ID)
	}
	if _, err := os.Stat(p.threadsFile()); err != nil {
		t.Fatalf("threads index not written: %v", err)
	}
}

func TestExcerptMatchUnicodePersian(t *testing.T) {
	long := strings.Repeat("متن آزمایشی طولانی ", 20) + "گزارش نهایی پروژه" + strings.Repeat(" ادامه متن", 20)
	got := excerptMatch(long, "گزارش", 40)
	if !utf8.ValidString(got) || !strings.Contains(got, "گزارش") {
		t.Fatalf("excerptMatch produced invalid UTF-8 or dropped query: %q", got)
	}
}

func TestGitPersianFilenameUnescaped(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	srv, h, pid := newFeatureTestServer(t)
	ctx := context.Background()
	if _, err := runGit(ctx, h.projDir, "init"); err != nil {
		t.Fatal(err)
	}
	fname := "گزارش.txt"
	if err := os.WriteFile(filepath.Join(h.projDir, fname), []byte("سلام\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+pid+"/changes", nil)
	req.SetPathValue("id", pid)
	w := httptest.NewRecorder()
	srv.changesAPI(w, req)
	if w.Code != 200 {
		t.Fatalf("changesAPI status = %d", w.Code)
	}
	var resp struct {
		Files []GitChange `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range resp.Files {
		if f.Path == fname {
			found = true
		}
	}
	if !found {
		t.Fatalf("changesAPI octal-escaped or missed Persian filename %q: %+v", fname, resp.Files)
	}
}

func TestCompactAndShellReturnConflictWhileRunning(t *testing.T) {
	srv, _, pid := newFeatureTestServer(t)
	p := srv.project(pid)
	p.mu.Lock()
	p.live.Running = true
	p.mu.Unlock()

	reqC := httptest.NewRequest(http.MethodPost, "/api/projects/"+pid+"/compact", nil)
	reqC.SetPathValue("id", pid)
	wC := httptest.NewRecorder()
	srv.compactAPI(wC, reqC)
	if wC.Code != http.StatusConflict {
		t.Fatalf("compactAPI while running status = %d, want 409", wC.Code)
	}

	reqS := httptest.NewRequest(http.MethodPost, "/api/projects/"+pid+"/shell", strings.NewReader(`{"command":"echo hi"}`))
	reqS.SetPathValue("id", pid)
	wS := httptest.NewRecorder()
	srv.shellAPI(wS, reqS)
	if wS.Code != http.StatusConflict {
		t.Fatalf("shellAPI while running status = %d, want 409", wS.Code)
	}
}

type stubBrowserSess struct {
	*fakeSession
	called bool
}

func (s *stubBrowserSess) HasBrowser() bool { return true }
func (s *stubBrowserSess) Browse(context.Context, map[string]any) (string, error) {
	s.called = true
	return "ok", nil
}

func TestBrowserAndUploadSymlinkConfinementAndURLValidation(t *testing.T) {
	outsideDir := t.TempDir()
	secretFile := filepath.Join(outsideDir, "secret.png")
	if err := os.WriteFile(secretFile, []byte("pngdata"), 0o600); err != nil {
		t.Fatal(err)
	}

	srv, h, pid := newFeatureTestServer(t)
	browserDir := filepath.Join(h.projDir, ".browser")
	if err := os.MkdirAll(browserDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretFile, filepath.Join(browserDir, "preview.png")); err != nil {
		t.Skipf("symlink creation not permitted in test environment: %v", err)
	}

	reqShot := httptest.NewRequest(http.MethodGet, "/api/projects/"+pid+"/browser/screenshot", nil)
	reqShot.SetPathValue("id", pid)
	wShot := httptest.NewRecorder()
	srv.browserScreenshotAPI(wShot, reqShot)
	if wShot.Code != http.StatusForbidden {
		t.Fatalf("browserScreenshotAPI symlink escape status = %d, want 403 (body=%s)", wShot.Code, wShot.Body.String())
	}

	if err := os.Symlink(outsideDir, filepath.Join(h.projDir, uploadDirName)); err != nil {
		t.Fatal(err)
	}
	reqUp := httptest.NewRequest(http.MethodPost, "/api/projects/"+pid+"/upload", bytes.NewBufferString(`{"name":"a.txt","data":"aGk="}`))
	reqUp.Header.Set("Content-Type", "application/json")
	reqUp.SetPathValue("id", pid)
	wUp := httptest.NewRecorder()
	srv.uploadAPI(wUp, reqUp)
	if wUp.Code != http.StatusForbidden {
		t.Fatalf("uploadAPI symlink escape status = %d, want 403 (body=%s)", wUp.Code, wUp.Body.String())
	}

	p := srv.project(pid)
	stub := &stubBrowserSess{fakeSession: &fakeSession{dir: h.projDir}}
	p.mu.Lock()
	p.sess = stub
	p.mu.Unlock()
	for _, raw := range []string{"file:///etc/passwd", "javascript:alert(1)", "http://169.254.169.254/latest/meta-data/"} {
		body, _ := json.Marshal(map[string]string{"action": "goto", "url": raw})
		reqAct := httptest.NewRequest(http.MethodPost, "/api/projects/"+pid+"/browser/action", bytes.NewReader(body))
		reqAct.SetPathValue("id", pid)
		wAct := httptest.NewRecorder()
		srv.browserActionAPI(wAct, reqAct)
		if wAct.Code != http.StatusBadRequest {
			t.Fatalf("browserActionAPI(%q) status = %d, want 400", raw, wAct.Code)
		}
	}
	if stub.called {
		t.Fatal("Browse was invoked for rejected URLs")
	}
}

type stubLiveBrowserSess struct {
	*stubBrowserSess
	takeover  bool
	inputs    []BrowserInput
	navCalls  []string
	jpegFrame []byte
}

func (s *stubLiveBrowserSess) BrowserLiveState() BrowserState {
	return BrowserState{
		Available: true,
		Open:      true,
		Live:      true,
		Takeover:  s.takeover,
		URL:       "https://example.com",
		Title:     "Example",
	}
}

func (s *stubLiveBrowserSess) BrowserNav(_ context.Context, action string, params map[string]any) (BrowserState, error) {
	s.navCalls = append(s.navCalls, action+":"+strings.TrimSpace(stringFromAny(params["url"])))
	return s.BrowserLiveState(), nil
}

func (s *stubLiveBrowserSess) BrowserInput(_ context.Context, ev BrowserInput) error {
	s.inputs = append(s.inputs, ev)
	return nil
}

func (s *stubLiveBrowserSess) SetBrowserTakeover(_ context.Context, active bool, _ string) (BrowserState, error) {
	s.takeover = active
	return s.BrowserLiveState(), nil
}

func (s *stubLiveBrowserSess) SubscribeBrowserFrames() ([]byte, <-chan []byte, func()) {
	ch := make(chan []byte)
	close(ch)
	return s.jpegFrame, ch, func() {}
}

func (s *stubLiveBrowserSess) SetBrowserObserver(func()) {}
func (s *stubLiveBrowserSess) CloseBrowser()             {}

func stringFromAny(v any) string {
	s, _ := v.(string)
	return s
}

func TestLiveBrowserEndpointsValidationStreamAndTakeover(t *testing.T) {
	srv, h, pid := newFeatureTestServer(t)
	p := srv.project(pid)
	liveStub := &stubLiveBrowserSess{
		stubBrowserSess: &stubBrowserSess{fakeSession: &fakeSession{dir: h.projDir}},
		jpegFrame:       []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0xFF, 0xD9},
	}
	p.mu.Lock()
	p.sess = liveStub
	p.mu.Unlock()

	// Invalid input type rejected with 400
	reqBadIn := httptest.NewRequest(http.MethodPost, "/api/projects/"+pid+"/browser/input", strings.NewReader(`{"type":"eval"}`))
	reqBadIn.SetPathValue("id", pid)
	wBadIn := httptest.NewRecorder()
	srv.browserInputAPI(wBadIn, reqBadIn)
	if wBadIn.Code != http.StatusBadRequest {
		t.Fatalf("invalid browser input status = %d, want 400", wBadIn.Code)
	}

	// Valid wheel input forwarded returning 200
	reqWheel := httptest.NewRequest(http.MethodPost, "/api/projects/"+pid+"/browser/input", strings.NewReader(`{"type":"wheel","x":100,"y":200,"deltaY":300}`))
	reqWheel.SetPathValue("id", pid)
	wWheel := httptest.NewRecorder()
	srv.browserInputAPI(wWheel, reqWheel)
	if wWheel.Code != http.StatusOK || len(liveStub.inputs) != 1 || liveStub.inputs[0].DeltaY != 300 {
		t.Fatalf("valid wheel status=%d inputs=%+v", wWheel.Code, liveStub.inputs)
	}

	// Bad new_tab URL rejected with 400
	for _, badURL := range []string{"file:///etc/passwd", "http://169.254.169.254/latest/meta-data/"} {
		body, _ := json.Marshal(map[string]string{"action": "new_tab", "url": badURL})
		reqNav := httptest.NewRequest(http.MethodPost, "/api/projects/"+pid+"/browser/action", bytes.NewReader(body))
		reqNav.SetPathValue("id", pid)
		wNav := httptest.NewRecorder()
		srv.browserActionAPI(wNav, reqNav)
		if wNav.Code != http.StatusBadRequest {
			t.Fatalf("new_tab(%q) status = %d, want 400", badURL, wNav.Code)
		}
	}

	// Takeover toggle updates session state
	reqTk := httptest.NewRequest(http.MethodPost, "/api/projects/"+pid+"/browser/takeover", strings.NewReader(`{"active":true,"reason":"user"}`))
	reqTk.SetPathValue("id", pid)
	wTk := httptest.NewRecorder()
	srv.browserTakeoverAPI(wTk, reqTk)
	if wTk.Code != http.StatusOK || !liveStub.takeover {
		t.Fatalf("browserTakeoverAPI status=%d takeover=%v", wTk.Code, liveStub.takeover)
	}

	// MJPEG stream delivers frame bytes with multipart content type
	streamCtx, streamCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer streamCancel()
	reqStream := httptest.NewRequest(http.MethodGet, "/api/projects/"+pid+"/browser/stream", nil).WithContext(streamCtx)
	reqStream.SetPathValue("id", pid)
	wStream := httptest.NewRecorder()
	srv.browserStreamAPI(wStream, reqStream)
	if wStream.Code != http.StatusOK || !strings.Contains(wStream.Header().Get("Content-Type"), "multipart/x-mixed-replace") {
		t.Fatalf("browserStreamAPI status=%d ct=%q", wStream.Code, wStream.Header().Get("Content-Type"))
	}
	if !bytes.Contains(wStream.Body.Bytes(), []byte{0xFF, 0xD8, 0xFF, 0xE0}) {
		t.Fatalf("browserStreamAPI body missing JPEG frame: %q", wStream.Body.String())
	}
}
