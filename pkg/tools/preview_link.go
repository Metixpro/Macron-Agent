package tools

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const ToolLivePreview = "live_preview"

type livePreviewServer struct {
	server *http.Server
	port   int
	ws     string
}

var (
	previewMu      sync.Mutex
	activePreviews = make(map[string]*livePreviewServer)
)

// StartLivePreview starts a local background HTTP file server for instant browser previewing.
func (r *Registry) StartLivePreview(sessionID string, port int, subDir string) (string, error) {
	ws, err := r.Machine.Workspace(sessionID)
	if err != nil {
		return "", err
	}

	targetDir := ws
	if subDir != "" {
		targetDir = ws + "/" + subDir
	}

	previewMu.Lock()
	defer previewMu.Unlock()

	if existing, ok := activePreviews[sessionID]; ok {
		return fmt.Sprintf("🔗 Live preview is already running at: http://localhost:%d/ (root: %s)",
			existing.port, existing.ws), nil
	}

	if port <= 0 {
		// find free port
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", err
		}
		port = l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
	}

	addr := "127.0.0.1:" + strconv.Itoa(port)
	srv := &http.Server{
		Addr:    addr,
		Handler: http.FileServer(http.Dir(targetDir)),
	}

	go func() {
		_ = srv.ListenAndServe()
	}()

	time.Sleep(100 * time.Millisecond)

	activePreviews[sessionID] = &livePreviewServer{
		server: srv,
		port:   port,
		ws:     targetDir,
	}

	return fmt.Sprintf("🚀 Live preview server started!\n- URL: http://localhost:%d/\n- Serving directory: `%s`\n- Access locally or via port-forwarding.",
		port, targetDir), nil
}

// StopLivePreview shuts down the session's live preview server.
func StopLivePreview(sessionID string) bool {
	previewMu.Lock()
	defer previewMu.Unlock()

	if srv, ok := activePreviews[sessionID]; ok {
		_ = srv.server.Shutdown(context.Background())
		delete(activePreviews, sessionID)
		return true
	}
	return false
}
