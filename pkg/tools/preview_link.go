package tools

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

const ToolLivePreview = "live_preview"

type livePreviewServer struct {
	server *http.Server
	port   int
	dir    string
}

var (
	previewMu      sync.Mutex
	activePreviews = map[string]*livePreviewServer{}
)

type hideDotfiles struct{ fs http.FileSystem }

func (h hideDotfiles) Open(name string) (http.File, error) {
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") && part != "." {
			return nil, os.ErrNotExist
		}
	}
	return h.fs.Open(name)
}

func (r *Registry) StartLivePreview(sessionID string, port int, subDir string) (string, error) {
	sub := path.Clean("/" + strings.TrimSpace(subDir))[1:]
	dir, inside, err := r.Machine.HostPath(sessionID, sub)
	if sub == "" {
		subDir = ""
		dir, err = r.Machine.Workspace(sessionID)
		inside = true
	}
	if err != nil {
		return "", err
	}
	if !inside {
		return "", fmt.Errorf("%s is outside the workspace", subDir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s is not a folder in the workspace", subDir)
	}
	previewMu.Lock()
	defer previewMu.Unlock()
	if cur, ok := activePreviews[sessionID]; ok {
		if cur.dir == dir {
			return fmt.Sprintf("The preview already runs: http://127.0.0.1:%d/ (serving %s). From another computer: ssh -L %d:127.0.0.1:%d this-server", cur.port, subDirOr(subDir), cur.port, cur.port), nil
		}
		_ = cur.server.Shutdown(context.Background())
		delete(activePreviews, sessionID)
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(max(port, 0)))
	if err != nil {
		return "", fmt.Errorf("port %d: %w", port, err)
	}
	port = l.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: http.FileServer(hideDotfiles{http.Dir(dir)}), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			previewMu.Lock()
			delete(activePreviews, sessionID)
			previewMu.Unlock()
		}
	}()
	activePreviews[sessionID] = &livePreviewServer{server: srv, port: port, dir: dir}
	return fmt.Sprintf("Preview serving %s at http://127.0.0.1:%d/ on this server (hidden files are not served). From another computer: ssh -L %d:127.0.0.1:%d this-server. Stop it with action=stop.", subDirOr(subDir), port, port, port), nil
}

func subDirOr(s string) string {
	if strings.TrimSpace(s) == "" {
		return "the workspace"
	}
	return s
}

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
