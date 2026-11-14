package desk

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"
)

type ServerSpec struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Host string `json:"host,omitempty"`
	User string `json:"user,omitempty"`
	Port int    `json:"port,omitempty"`
	Key  string `json:"key,omitempty"`
}

type ServerView struct {
	ServerSpec
	State   string `json:"state"`
	Error   string `json:"error,omitempty"`
	Version string `json:"version,omitempty"`
}

type remote struct {
	spec    ServerSpec
	mu      sync.Mutex
	state   string
	err     string
	version string
	addr    string
	token   string
	tr      transport
	stdin   io.WriteCloser
	proxy   *httputil.ReverseProxy
	secrets Secrets
	busy    bool
}

type remotes struct {
	srv  *Server
	mu   sync.Mutex
	list map[string]*remote
}

func (rs *remotes) get(id string) *remote {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.list[id]
}

func (r *remote) view() ServerView {
	r.mu.Lock()
	defer r.mu.Unlock()
	return ServerView{ServerSpec: r.spec, State: r.state, Error: r.err, Version: r.version}
}

func (rs *remotes) load() {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.list = map[string]*remote{}
	for _, s := range rs.srv.host.Servers() {
		rs.list[s.ID] = &remote{spec: s, state: "off"}
	}
}

func (rs *remotes) save() error {
	rs.mu.Lock()
	specs := make([]ServerSpec, 0, len(rs.list))
	for _, r := range rs.list {
		specs = append(specs, r.spec)
	}
	rs.mu.Unlock()
	return rs.srv.host.SaveServers(specs)
}

func (rs *remotes) views() []ServerView {
	rs.mu.Lock()
	list := make([]*remote, 0, len(rs.list))
	for _, r := range rs.list {
		list = append(list, r)
	}
	rs.mu.Unlock()
	out := make([]ServerView, 0, len(list))
	for _, r := range list {
		out = append(out, r.view())
	}
	sortViews(out)
	return out
}

func sortViews(v []ServerView) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && strings.ToLower(v[j].Name) < strings.ToLower(v[j-1].Name); j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

func (rs *remotes) changed() {
	rs.srv.hub.publish(map[string]any{"type": "servers"})
}

func (r *remote) set(state, msg string) {
	r.mu.Lock()
	r.state, r.err = state, msg
	r.mu.Unlock()
}

func (r *remote) shutdown() {
	r.mu.Lock()
	tr, stdin := r.tr, r.stdin
	r.tr, r.stdin, r.proxy, r.addr, r.token = nil, nil, nil, "", ""
	r.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if tr != nil {
		go func() {
			select {
			case <-tr.done():
			case <-time.After(3 * time.Second):
			}
			tr.close()
		}()
	}
}

func (rs *remotes) connect(ctx context.Context, r *remote, sec Secrets) error {
	r.mu.Lock()
	if r.state == "ready" {
		r.mu.Unlock()
		return nil
	}
	if r.busy {
		r.mu.Unlock()
		return errors.New("already connecting")
	}
	r.busy, r.state, r.err = true, "connecting", ""
	if sec.Password == "" {
		sec.Password = r.secrets.Password
	}
	if sec.Passphrase == "" {
		sec.Passphrase = r.secrets.Passphrase
	}
	spec := r.spec
	r.mu.Unlock()
	rs.changed()
	err := rs.open(ctx, r, spec, sec)
	r.mu.Lock()
	r.busy = false
	if err == nil {
		r.secrets = Secrets{Password: sec.Password, Passphrase: sec.Passphrase}
	}
	r.mu.Unlock()
	if err != nil {
		var need *NeedError
		if errors.As(err, &need) {
			r.set("off", "")
		} else {
			r.set("failed", err.Error())
		}
	}
	rs.changed()
	return err
}

func (rs *remotes) open(ctx context.Context, r *remote, spec ServerSpec, sec Secrets) error {
	var tr transport
	switch spec.Kind {
	case "wsl":
		if !wslAvailable() {
			return errors.New("WSL is not installed on this PC")
		}
		tr = &execTransport{argv: wslArgv(spec.Host), ended: make(chan struct{})}
	case "ssh":
		t, err := dialSSH(ctx, spec, sec)
		if err != nil {
			return err
		}
		tr = t
	default:
		return fmt.Errorf("unknown connection kind %q", spec.Kind)
	}
	stdin, stdout, stderr, err := tr.start(ctx)
	if err != nil {
		tr.close()
		return err
	}
	type hello struct {
		Addr    string `json:"addr"`
		Token   string `json:"token"`
		Version string `json:"version"`
	}
	got := make(chan hello, 1)
	go func() {
		br := bufio.NewReader(stdout)
		for {
			line, err := br.ReadBytes('\n')
			var h hello
			if json.Unmarshal(line, &h) == nil && h.Addr != "" && h.Token != "" {
				got <- h
				_, _ = io.Copy(io.Discard, br)
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var h hello
	select {
	case h = <-got:
	case <-tr.done():
		tr.close()
		log.Printf("desktop: macron on %s stopped while starting: %s", spec.Name, strings.TrimSpace(stderr()))
		return remoteStartError(stderr())
	case <-time.After(90 * time.Second):
		tr.close()
		return errors.New("Macron on the server did not start in time" + tail(stderr()))
	case <-ctx.Done():
		tr.close()
		return ctx.Err()
	}
	if _, port, err := net.SplitHostPort(h.Addr); err != nil || port == "" {
		tr.close()
		return errors.New("Macron on the server gave a bad address")
	} else {
		h.Addr = net.JoinHostPort("127.0.0.1", port)
	}
	proxy := rs.proxyFor(r.spec.ID, h.Addr, h.Token, tr)
	r.mu.Lock()
	r.tr, r.stdin, r.addr, r.token, r.version, r.proxy = tr, stdin, h.Addr, h.Token, h.Version, proxy
	r.state, r.err = "ready", ""
	r.mu.Unlock()
	go func() {
		<-tr.done()
		r.mu.Lock()
		same := r.tr == tr
		if same {
			r.tr, r.stdin, r.proxy = nil, nil, nil
			r.state, r.err = "failed", "the connection was lost"
		}
		r.mu.Unlock()
		if same {
			log.Printf("desktop: the connection to %s was lost", spec.Name)
			rs.changed()
		}
	}()
	return nil
}

func tail(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	return ": " + strings.TrimSpace(lines[len(lines)-1])
}

func remoteStartError(stderr string) error {
	low := strings.ToLower(stderr)
	switch {
	case strings.Contains(low, "not installed"):
		return errors.New("Macron is not installed on that machine. Install it there first")
	case strings.Contains(low, "desktop") && (strings.Contains(low, "unknown") || strings.Contains(low, "usage") || strings.Contains(low, "flag provided")):
		return errors.New("Macron on that machine is too old for the desktop app. Run «macron update» there")
	}
	return errors.New("Macron on that machine stopped while starting" + tail(stderr))
}

func (rs *remotes) proxyFor(id, addr, token string, tr transport) *httputil.ReverseProxy {
	prefix := "/remote/" + id
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = addr
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, prefix)
			pr.Out.URL.RawPath = ""
			pr.Out.Host = addr
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Set("Authorization", "Bearer "+token)
		},
		Transport: &http.Transport{
			DialContext:           func(ctx context.Context, _, _ string) (net.Conn, error) { return tr.dial(ctx, addr) },
			MaxIdleConns:          16,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 5 * time.Minute,
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeErr(w, http.StatusBadGateway, fmt.Errorf("the server is not answering: %w", err))
		},
	}
}

func newServerID() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) serversAPI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"servers": s.remotes.views(), "wsl": wslAvailable()})
}

func (s *Server) addServerAPI(w http.ResponseWriter, r *http.Request) {
	var in ServerSpec
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	in.Kind = strings.ToLower(strings.TrimSpace(in.Kind))
	in.Host, in.User, in.Name, in.Key = strings.TrimSpace(in.Host), strings.TrimSpace(in.User), strings.TrimSpace(in.Name), strings.TrimSpace(in.Key)
	switch in.Kind {
	case "ssh":
		if in.Host == "" {
			writeErr(w, http.StatusBadRequest, errors.New("the server's address is needed"))
			return
		}
		if in.Port < 0 || in.Port > 65535 {
			writeErr(w, http.StatusBadRequest, errors.New("the port must be between 1 and 65535"))
			return
		}
		if in.Name == "" {
			in.Name = in.Host
		}
	case "wsl":
		if in.Name == "" {
			in.Name = "WSL"
			if in.Host != "" {
				in.Name = in.Host
			}
		}
	default:
		writeErr(w, http.StatusBadRequest, errors.New("the connection is ssh or wsl"))
		return
	}
	in.ID = newServerID()
	s.remotes.mu.Lock()
	s.remotes.list[in.ID] = &remote{spec: in, state: "off"}
	s.remotes.mu.Unlock()
	if err := s.remotes.save(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.remotes.changed()
	writeJSON(w, s.remotes.get(in.ID).view())
}

func (s *Server) removeServerAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.remotes.mu.Lock()
	rm := s.remotes.list[id]
	delete(s.remotes.list, id)
	s.remotes.mu.Unlock()
	if rm == nil {
		writeErr(w, http.StatusNotFound, errors.New("no such server"))
		return
	}
	rm.shutdown()
	_ = s.remotes.save()
	s.remotes.changed()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) connectServerAPI(w http.ResponseWriter, r *http.Request) {
	rm := s.remotes.get(r.PathValue("id"))
	if rm == nil {
		writeErr(w, http.StatusNotFound, errors.New("no such server"))
		return
	}
	var sec Secrets
	_ = readJSON(r, &sec)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	err := s.remotes.connect(ctx, rm, sec)
	var need *NeedError
	if err != nil && !errors.As(err, &need) {
		v := rm.view()
		log.Printf("desktop: connecting to %s (%s@%s:%d) failed: %v", v.Name, v.User, v.Host, v.Port, err)
	}
	switch {
	case errors.As(err, &need):
		writeJSON(w, map[string]any{"server": rm.view(), "need": need.Need, "fingerprint": need.Fingerprint, "message": need.Message})
	case err != nil:
		writeErr(w, http.StatusBadGateway, err)
	default:
		writeJSON(w, map[string]any{"server": rm.view()})
	}
}

func (s *Server) disconnectServerAPI(w http.ResponseWriter, r *http.Request) {
	rm := s.remotes.get(r.PathValue("id"))
	if rm == nil {
		writeErr(w, http.StatusNotFound, errors.New("no such server"))
		return
	}
	rm.shutdown()
	rm.set("off", "")
	s.remotes.changed()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) remoteProxy(w http.ResponseWriter, r *http.Request) {
	rm := s.remotes.get(r.PathValue("id"))
	if rm == nil {
		writeErr(w, http.StatusNotFound, errors.New("no such server"))
		return
	}
	rm.mu.Lock()
	proxy := rm.proxy
	rm.mu.Unlock()
	if proxy == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("not connected to that server"))
		return
	}
	proxy.ServeHTTP(w, r)
}

func (s *Server) Close() {
	s.remotes.mu.Lock()
	list := make([]*remote, 0, len(s.remotes.list))
	for _, r := range s.remotes.list {
		list = append(list, r)
	}
	s.remotes.mu.Unlock()
	for _, r := range list {
		r.shutdown()
	}
}
