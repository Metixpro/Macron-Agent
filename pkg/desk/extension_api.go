package desk

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

// ExtensionTab mirrors a browser tab inside the Chrome Extension's "Macron" Tab Group.
type ExtensionTab struct {
	ID     int    `json:"id"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Active bool   `json:"active"`
}

// ExtensionStatus is returned to the Desktop UI so the user can pair and monitor
// the Macron Chrome Extension (Manifest V3).
type ExtensionStatus struct {
	Connected       bool           `json:"connected"`
	BrowserName     string         `json:"browserName,omitempty"`
	ExtensionVer    string         `json:"extensionVersion,omitempty"`
	ApprovedOrigins []string       `json:"approvedOrigins,omitempty"`
	Tabs            []ExtensionTab `json:"tabs,omitempty"`
	PairingToken    string         `json:"pairingToken"`
	Endpoint        string         `json:"endpoint"`
	ConnectedAt     int64          `json:"connectedAt,omitempty"`
}

type extensionWireMsg struct {
	ID              int64           `json:"id,omitempty"`
	Type            string          `json:"type"`
	OK              bool            `json:"ok,omitempty"`
	Error           string          `json:"error,omitempty"`
	Result          json.RawMessage `json:"result,omitempty"`
	BrowserName     string          `json:"browserName,omitempty"`
	ExtensionVer    string          `json:"extensionVersion,omitempty"`
	ApprovedOrigins []string        `json:"approvedOrigins,omitempty"`
	Tabs            []ExtensionTab  `json:"tabs,omitempty"`
}

type extensionBridge struct {
	mu              sync.Mutex
	token           string
	conn            *websocket.Conn
	connectedAt     int64
	browserName     string
	extensionVer    string
	approvedOrigins []string
	tabs            []ExtensionTab
	pending         map[int64]chan extensionWireMsg
	nextID          atomic.Int64
}

func newExtensionBridge() *extensionBridge {
	var raw [24]byte
	_, _ = rand.Read(raw[:])
	return &extensionBridge{
		token:   hex.EncodeToString(raw[:]),
		pending: map[int64]chan extensionWireMsg{},
	}
}

func (s *Server) extBridge() *extensionBridge {
	s.extOnce.Do(func() {
		s.ext = newExtensionBridge()
	})
	return s.ext
}

func (b *extensionBridge) validToken(candidate string) bool {
	c := strings.TrimSpace(candidate)
	if c == "" {
		return false
	}
	b.mu.Lock()
	tok := b.token
	b.mu.Unlock()
	return len(c) == len(tok) && subtle.ConstantTimeCompare([]byte(c), []byte(tok)) == 1
}

func (b *extensionBridge) rotateToken() string {
	var raw [24]byte
	_, _ = rand.Read(raw[:])
	next := hex.EncodeToString(raw[:])
	b.mu.Lock()
	b.token = next
	old := b.conn
	b.conn = nil
	b.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return next
}

func (b *extensionBridge) status(serverAddr string) ExtensionStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	ep := "ws://127.0.0.1/api/browser/extension/ws"
	if serverAddr != "" {
		ep = "ws://" + serverAddr + "/api/browser/extension/ws"
	}
	return ExtensionStatus{
		Connected:       b.conn != nil,
		BrowserName:     b.browserName,
		ExtensionVer:    b.extensionVer,
		ApprovedOrigins: append([]string(nil), b.approvedOrigins...),
		Tabs:            append([]ExtensionTab(nil), b.tabs...),
		PairingToken:    b.token,
		Endpoint:        ep,
		ConnectedAt:     b.connectedAt,
	}
}

// Call sends an RPC command to the connected Chrome Extension and awaits reply.
func (b *extensionBridge) Call(ctx context.Context, cmd string, params map[string]any) (json.RawMessage, error) {
	b.mu.Lock()
	conn := b.conn
	if conn == nil {
		b.mu.Unlock()
		return nil, errors.New("Macron Chrome Extension is not connected")
	}
	id := b.nextID.Add(1)
	ch := make(chan extensionWireMsg, 1)
	b.pending[id] = ch
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()

	req := map[string]any{"id": id, "type": "command", "command": cmd, "params": params}
	if err := websocket.JSON.Send(conn, req); err != nil {
		return nil, err
	}
	select {
	case rep := <-ch:
		if !rep.OK {
			if rep.Error != "" {
				return nil, errors.New(rep.Error)
			}
			return nil, errors.New("extension command failed")
		}
		return rep.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Server) extensionStatusAPI(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.extBridge().status(s.addr))
}

func (s *Server) extensionRotateTokenAPI(w http.ResponseWriter, _ *http.Request) {
	b := s.extBridge()
	b.rotateToken()
	if s.hub != nil {
		s.hub.publish(map[string]any{"type": "extension", "extension": b.status(s.addr)})
	}
	writeJSON(w, b.status(s.addr))
}

func (s *Server) extensionCommandAPI(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Command string         `json:"command"`
		Params  map[string]any `json:"params"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cmd := strings.TrimSpace(in.Command)
	if cmd == "" {
		writeErr(w, http.StatusBadRequest, errors.New("command is required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := s.extBridge().Call(ctx, cmd, in.Params)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var decoded any
	if len(res) > 0 {
		_ = json.Unmarshal(res, &decoded)
	}
	writeJSON(w, map[string]any{"ok": true, "result": decoded})
}

// extensionWSAPI accepts authenticated WebSocket connections from the local
// Macron Chrome Extension (Manifest V3) using the pairing token (`?ext_token=...`).
func (s *Server) extensionWSAPI(w http.ResponseWriter, r *http.Request) {
	b := s.extBridge()
	tok := r.URL.Query().Get("ext_token")
	if tok == "" {
		tok = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if !b.validToken(tok) {
		http.Error(w, "invalid extension pairing token", http.StatusUnauthorized)
		return
	}

	wsSrv := websocket.Server{
		Handshake: func(cfg *websocket.Config, req *http.Request) error {
			// Allow chrome-extension:// or loopback origins once paired with token.
			origin := strings.ToLower(req.Header.Get("Origin"))
			if origin != "" && !strings.HasPrefix(origin, "chrome-extension://") &&
				!strings.HasPrefix(origin, "moz-extension://") &&
				!strings.HasPrefix(origin, "http://127.0.0.1") &&
				!strings.HasPrefix(origin, "http://localhost") {
				return errors.New("disallowed extension origin")
			}
			return nil
		},
		Handler: func(ws *websocket.Conn) {
			b.mu.Lock()
			if b.conn != nil {
				_ = b.conn.Close()
			}
			b.conn = ws
			b.connectedAt = time.Now().UnixMilli()
			b.mu.Unlock()

			defer func() {
				b.mu.Lock()
				if b.conn == ws {
					b.conn = nil
					b.tabs = nil
					b.connectedAt = 0
					for id, ch := range b.pending {
						select {
						case ch <- extensionWireMsg{ID: id, OK: false, Error: "extension disconnected"}:
						default:
						}
						delete(b.pending, id)
					}
				}
				b.mu.Unlock()
				if s.hub != nil {
					s.hub.publish(map[string]any{"type": "extension", "extension": b.status(s.addr)})
				}
			}()

			if s.hub != nil {
				s.hub.publish(map[string]any{"type": "extension", "extension": b.status(s.addr)})
			}

			for {
				var msg extensionWireMsg
				if err := websocket.JSON.Receive(ws, &msg); err != nil {
					return
				}
				switch msg.Type {
				case "hello", "state":
					b.mu.Lock()
					if msg.BrowserName != "" {
						b.browserName = msg.BrowserName
					}
					if msg.ExtensionVer != "" {
						b.extensionVer = msg.ExtensionVer
					}
					if msg.ApprovedOrigins != nil {
						b.approvedOrigins = msg.ApprovedOrigins
					}
					if msg.Tabs != nil {
						b.tabs = msg.Tabs
					}
					b.mu.Unlock()
					if s.hub != nil {
						s.hub.publish(map[string]any{"type": "extension", "extension": b.status(s.addr)})
					}
				case "reply":
					b.mu.Lock()
					ch := b.pending[msg.ID]
					delete(b.pending, msg.ID)
					b.mu.Unlock()
					if ch != nil {
						select {
						case ch <- msg:
						default:
						}
					}
				case "ping":
					_ = websocket.JSON.Send(ws, map[string]any{"type": "pong", "at": time.Now().UnixMilli()})
				}
			}
		},
	}
	wsSrv.ServeHTTP(w, r)
}
