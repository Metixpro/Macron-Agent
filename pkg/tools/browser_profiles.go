package tools

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BrowserProxyConfig configures an optional per-profile HTTP/HTTPS/SOCKS5 proxy.
type BrowserProxyConfig struct {
	Scheme   string `json:"scheme"`             // "http", "https", "socks5"
	Host     string `json:"host"`               // proxy hostname or IP
	Port     int    `json:"port"`               // proxy TCP port
	Username string `json:"username,omitempty"` // optional auth username
	Password string `json:"password,omitempty"` // stored only on disk, masked in API responses
	Bypass   string `json:"bypass,omitempty"`   // comma-separated bypass hosts
}

// BrowserProfile describes an isolated persistent browser profile with color
// accent and optional proxy configuration.
type BrowserProfile struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	Color     string              `json:"color"`
	Scope     string              `json:"scope"` // "workspace", "shared", "named"
	Proxy     *BrowserProxyConfig `json:"proxy,omitempty"`
	BuiltIn   bool                `json:"builtIn,omitempty"`
	CreatedAt int64               `json:"createdAt,omitempty"`
}

// BrowserProfileView is the API-safe projection of BrowserProfile (password redacted).
type BrowserProfileView struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Color         string              `json:"color"`
	Scope         string              `json:"scope"`
	BuiltIn       bool                `json:"builtIn,omitempty"`
	HasProxy      bool                `json:"hasProxy,omitempty"`
	ProxyPassword bool                `json:"proxyPasswordSet,omitempty"`
	Proxy         *BrowserProxyConfig `json:"proxy,omitempty"`
}

var profileColorPalette = []string{
	"#60a5fa", // sky
	"#a78bfa", // violet
	"#34d399", // emerald
	"#f59e0b", // amber
	"#f472b6", // rose
	"#22d3ee", // cyan
}

var safeProfileIDRe = regexp.MustCompile(`[^a-z0-9_-]+`)

type browserProfileStore struct {
	mu       sync.Mutex
	baseDir  string
	path     string
	loaded   bool
	profiles []BrowserProfile
	// activeBySession maps browserSessionKey -> profile ID (default "workspace")
	activeBySession map[string]string
	// relays maps profile ID -> active local SOCKS5 auth relay
	relays map[string]*socks5Relay
}

func (r *Registry) profileStore() *browserProfileStore {
	r.profilesOnce.Do(func() {
		base := r.BrowserDataDir
		if base == "" {
			base = defaultMacronBrowserConfigDir()
		}
		r.browserProfiles = &browserProfileStore{
			baseDir:         base,
			path:            filepath.Join(base, "browser_profiles.json"),
			activeBySession: map[string]string{},
			relays:          map[string]*socks5Relay{},
		}
	})
	return r.browserProfiles
}

func builtInBrowserProfiles() []BrowserProfile {
	return []BrowserProfile{
		{ID: "workspace", Name: "Workspace", Color: "#60a5fa", Scope: "workspace", BuiltIn: true},
		{ID: "shared", Name: "Shared", Color: "#34d399", Scope: "shared", BuiltIn: true},
	}
}

func (s *browserProfileStore) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.profiles = builtInBrowserProfiles()
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var saved []BrowserProfile
	if json.Unmarshal(raw, &saved) != nil {
		return
	}
	byID := map[string]int{}
	for i, p := range s.profiles {
		byID[p.ID] = i
	}
	for _, p := range saved {
		p.ID = strings.TrimSpace(strings.ToLower(p.ID))
		if p.ID == "" {
			continue
		}
		if idx, ok := byID[p.ID]; ok {
			// Preserve built-in flags, allow customized color/proxy.
			s.profiles[idx].Color = pickProfileColor(p.Color, idx)
			s.profiles[idx].Proxy = p.Proxy
		} else {
			if p.Scope == "" {
				p.Scope = "named"
			}
			p.Color = pickProfileColor(p.Color, len(s.profiles))
			byID[p.ID] = len(s.profiles)
			s.profiles = append(s.profiles, p)
		}
	}
}

func (s *browserProfileStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.profiles, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func pickProfileColor(c string, idx int) string {
	c = strings.TrimSpace(c)
	if len(c) == 7 && c[0] == '#' {
		return c
	}
	return profileColorPalette[idx%len(profileColorPalette)]
}

func (p BrowserProfile) toView() BrowserProfileView {
	v := BrowserProfileView{
		ID:      p.ID,
		Name:    p.Name,
		Color:   p.Color,
		Scope:   p.Scope,
		BuiltIn: p.BuiltIn,
	}
	if p.Proxy != nil && strings.TrimSpace(p.Proxy.Host) != "" && p.Proxy.Port > 0 {
		v.HasProxy = true
		v.ProxyPassword = p.Proxy.Password != ""
		v.Proxy = &BrowserProxyConfig{
			Scheme:   p.Proxy.Scheme,
			Host:     p.Proxy.Host,
			Port:     p.Proxy.Port,
			Username: p.Proxy.Username,
			Bypass:   p.Proxy.Bypass,
		}
	}
	return v
}

func normalizeProxyConfig(in *BrowserProxyConfig) (*BrowserProxyConfig, error) {
	if in == nil {
		return nil, nil
	}
	host := strings.TrimSpace(in.Host)
	if host == "" && in.Port == 0 {
		return nil, nil
	}
	// Support pasting full URL into Host field (e.g. socks5://user:pass@1.2.3.4:1080).
	if strings.Contains(host, "://") {
		u, err := url.Parse(host)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy URL: %w", err)
		}
		cp := *in
		cp.Scheme = strings.ToLower(u.Scheme)
		cp.Host = u.Hostname()
		if p, err := strconv.Atoi(u.Port()); err == nil && p > 0 {
			cp.Port = p
		}
		if u.User != nil {
			if cp.Username == "" {
				cp.Username = u.User.Username()
			}
			if pw, ok := u.User.Password(); ok && cp.Password == "" {
				cp.Password = pw
			}
		}
		in = &cp
		host = strings.TrimSpace(in.Host)
	}
	scheme := strings.ToLower(strings.TrimSpace(in.Scheme))
	if scheme == "" {
		scheme = "http"
	}
	if scheme == "socks5h" {
		scheme = "socks5"
	}
	switch scheme {
	case "http", "https", "socks5":
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q (use http, https, or socks5)", scheme)
	}
	if host == "" {
		return nil, errors.New("proxy host is required")
	}
	if in.Port <= 0 || in.Port > 65535 {
		return nil, errors.New("proxy port must be between 1 and 65535")
	}
	return &BrowserProxyConfig{
		Scheme:   scheme,
		Host:     host,
		Port:     in.Port,
		Username: strings.TrimSpace(in.Username),
		Password: in.Password,
		Bypass:   strings.TrimSpace(in.Bypass),
	}, nil
}

func (s *browserProfileStore) list() []BrowserProfileView {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	out := make([]BrowserProfileView, len(s.profiles))
	for i, p := range s.profiles {
		out[i] = p.toView()
	}
	return out
}

func (s *browserProfileStore) get(id string) (BrowserProfile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	id = strings.TrimSpace(strings.ToLower(id))
	if id == "" {
		id = "workspace"
	}
	for _, p := range s.profiles {
		if p.ID == id {
			return p, true
		}
	}
	return s.profiles[0], false
}

func (s *browserProfileStore) upsert(in BrowserProfile, keepExistingPassword bool) (BrowserProfileView, error) {
	name := strings.TrimSpace(in.Name)
	id := strings.TrimSpace(strings.ToLower(in.ID))
	if id == "" {
		slug := safeProfileIDRe.ReplaceAllString(strings.ToLower(name), "-")
		slug = strings.Trim(slug, "-")
		if slug == "" {
			var b [4]byte
			_, _ = rand.Read(b[:])
			slug = "profile-" + hex.EncodeToString(b[:])
		}
		id = slug
	}
	if name == "" {
		name = strings.ToUpper(id[:1]) + id[1:]
	}
	proxy, err := normalizeProxyConfig(in.Proxy)
	if err != nil {
		return BrowserProfileView{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()

	for i := range s.profiles {
		if s.profiles[i].ID == id {
			if !s.profiles[i].BuiltIn {
				s.profiles[i].Name = name
			}
			if in.Color != "" {
				s.profiles[i].Color = pickProfileColor(in.Color, i)
			}
			if keepExistingPassword && proxy != nil && proxy.Password == "" && s.profiles[i].Proxy != nil {
				proxy.Password = s.profiles[i].Proxy.Password
			}
			s.profiles[i].Proxy = proxy
			// Stop stale SOCKS5 relay if proxy changed.
			if relay := s.relays[id]; relay != nil {
				relay.Close()
				delete(s.relays, id)
			}
			if err := s.saveLocked(); err != nil {
				return BrowserProfileView{}, err
			}
			return s.profiles[i].toView(), nil
		}
	}

	prof := BrowserProfile{
		ID:        id,
		Name:      name,
		Color:     pickProfileColor(in.Color, len(s.profiles)),
		Scope:     "named",
		Proxy:     proxy,
		CreatedAt: time.Now().Unix(),
	}
	s.profiles = append(s.profiles, prof)
	if err := s.saveLocked(); err != nil {
		return BrowserProfileView{}, err
	}
	return prof.toView(), nil
}

func (s *browserProfileStore) remove(id string) error {
	id = strings.TrimSpace(strings.ToLower(id))
	if id == "workspace" || id == "shared" {
		return errors.New("built-in profiles cannot be deleted")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	for i := range s.profiles {
		if s.profiles[i].ID == id {
			s.profiles = append(s.profiles[:i], s.profiles[i+1:]...)
			if relay := s.relays[id]; relay != nil {
				relay.Close()
				delete(s.relays, id)
			}
			for sessKey, activeID := range s.activeBySession {
				if activeID == id {
					s.activeBySession[sessKey] = "workspace"
				}
			}
			_ = os.RemoveAll(filepath.Join(s.baseDir, "browser-profiles", id))
			return s.saveLocked()
		}
	}
	return errors.New("browser profile not found")
}

func (s *browserProfileStore) activeProfileID(sessionKey string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id := s.activeBySession[sessionKey]; id != "" {
		return id
	}
	return "workspace"
}

func (s *browserProfileStore) setActiveProfile(sessionKey, profileID string) (BrowserProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	profileID = strings.TrimSpace(strings.ToLower(profileID))
	if profileID == "" {
		profileID = "workspace"
	}
	for _, p := range s.profiles {
		if p.ID == profileID {
			s.activeBySession[sessionKey] = p.ID
			return p, nil
		}
	}
	return BrowserProfile{}, fmt.Errorf("unknown browser profile %q", profileID)
}

// resolveLaunchConfig produces storage_dir and playwright proxy settings for
// the given profile, starting an authenticated local SOCKS5 relay when needed.
func (s *browserProfileStore) resolveLaunchConfig(profileID string) (BrowserProfile, map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()

	prof := s.profiles[0]
	for _, p := range s.profiles {
		if p.ID == profileID {
			prof = p
			break
		}
	}
	cfg := map[string]any{
		"profile_id":    prof.ID,
		"profile_name":  prof.Name,
		"profile_color": prof.Color,
	}
	if prof.Scope != "workspace" {
		dir := filepath.Join(s.baseDir, "browser-profiles", prof.ID)
		_ = os.MkdirAll(dir, 0o700)
		cfg["storage_dir"] = dir
	}

	if prof.Proxy != nil && strings.TrimSpace(prof.Proxy.Host) != "" && prof.Proxy.Port > 0 {
		px := prof.Proxy
		if px.Scheme == "socks5" && (px.Username != "" || px.Password != "") {
			relay := s.relays[prof.ID]
			if relay == nil || relay.upstream != net.JoinHostPort(px.Host, strconv.Itoa(px.Port)) ||
				relay.username != px.Username || relay.password != px.Password {
				if relay != nil {
					relay.Close()
				}
				var err error
				relay, err = startSOCKS5Relay(net.JoinHostPort(px.Host, strconv.Itoa(px.Port)), px.Username, px.Password)
				if err != nil {
					return prof, nil, fmt.Errorf("start local socks5 relay: %w", err)
				}
				s.relays[prof.ID] = relay
			}
			proxyCfg := map[string]any{
				"server": "socks5://" + relay.Addr(),
			}
			if px.Bypass != "" {
				proxyCfg["bypass"] = px.Bypass
			}
			cfg["proxy"] = proxyCfg
		} else {
			proxyCfg := map[string]any{
				"server": fmt.Sprintf("%s://%s", px.Scheme, net.JoinHostPort(px.Host, strconv.Itoa(px.Port))),
			}
			if px.Username != "" {
				proxyCfg["username"] = px.Username
			}
			if px.Password != "" {
				proxyCfg["password"] = px.Password
			}
			if px.Bypass != "" {
				proxyCfg["bypass"] = px.Bypass
			}
			cfg["proxy"] = proxyCfg
		}
		cfg["webrtc_disable_non_proxied_udp"] = true
	}

	return prof, cfg, nil
}

func (s *browserProfileStore) closeAllRelays() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, relay := range s.relays {
		relay.Close()
		delete(s.relays, id)
	}
}

// socks5Relay bridges unauthenticated loopback SOCKS5 from Chromium to an
// authenticated upstream SOCKS5 proxy (RFC 1928 + RFC 1929).
type socks5Relay struct {
	ln       net.Listener
	upstream string
	username string
	password string
	once     sync.Once
}

func startSOCKS5Relay(upstream, username, password string) (*socks5Relay, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &socks5Relay{
		ln:       ln,
		upstream: upstream,
		username: username,
		password: password,
	}
	go r.serve()
	return r, nil
}

func (r *socks5Relay) Addr() string {
	return r.ln.Addr().String()
}

func (r *socks5Relay) Close() {
	r.once.Do(func() {
		_ = r.ln.Close()
	})
}

func (r *socks5Relay) serve() {
	for {
		conn, err := r.ln.Accept()
		if err != nil {
			return
		}
		go r.handle(conn)
	}
}

func (r *socks5Relay) handle(client net.Conn) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(20 * time.Second))

	// 1. Client greeting: VER(1) NMETHODS(1) METHODS(N)
	var hdr [2]byte
	if _, err := io.ReadFull(client, hdr[:]); err != nil || hdr[0] != 0x05 {
		return
	}
	methods := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(client, methods); err != nil {
		return
	}
	// Accept NO AUTH (0x00) on loopback.
	if _, err := client.Write([]byte{0x05, 0x00}); err != nil {
		return
	}

	// 2. Read client CONNECT request so we can forward it verbatim after upstream auth.
	var reqHdr [4]byte
	if _, err := io.ReadFull(client, reqHdr[:]); err != nil || reqHdr[0] != 0x05 {
		return
	}
	var addrBytes []byte
	switch reqHdr[3] {
	case 0x01: // IPv4
		addrBytes = make([]byte, 4+2)
		if _, err := io.ReadFull(client, addrBytes); err != nil {
			return
		}
	case 0x03: // Domain
		var dlen [1]byte
		if _, err := io.ReadFull(client, dlen[:]); err != nil {
			return
		}
		rest := make([]byte, int(dlen[0])+2)
		if _, err := io.ReadFull(client, rest); err != nil {
			return
		}
		addrBytes = append([]byte{dlen[0]}, rest...)
	case 0x04: // IPv6
		addrBytes = make([]byte, 16+2)
		if _, err := io.ReadFull(client, addrBytes); err != nil {
			return
		}
	default:
		_, _ = client.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	fullReq := append(reqHdr[:], addrBytes...)

	// 3. Dial upstream SOCKS5 proxy and negotiate username/password auth (RFC 1929).
	up, err := net.DialTimeout("tcp", r.upstream, 15*time.Second)
	if err != nil {
		_, _ = client.Write([]byte{0x05, 0x04, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	_ = up.SetDeadline(time.Now().Add(20 * time.Second))

	if _, err := up.Write([]byte{0x05, 0x02, 0x00, 0x02}); err != nil {
		return
	}
	var upChoice [2]byte
	if _, err := io.ReadFull(up, upChoice[:]); err != nil || upChoice[0] != 0x05 {
		return
	}
	switch upChoice[1] {
	case 0x00:
		// Upstream requires no auth.
	case 0x02:
		u := []byte(r.username)
		p := []byte(r.password)
		if len(u) > 255 || len(p) > 255 {
			return
		}
		auth := make([]byte, 0, 3+len(u)+len(p))
		auth = append(auth, 0x01, byte(len(u)))
		auth = append(auth, u...)
		auth = append(auth, byte(len(p)))
		auth = append(auth, p...)
		if _, err := up.Write(auth); err != nil {
			return
		}
		var authRep [2]byte
		if _, err := io.ReadFull(up, authRep[:]); err != nil || authRep[1] != 0x00 {
			_, _ = client.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			return
		}
	default:
		_, _ = client.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// 4. Forward CONNECT request and relay reply.
	if _, err := up.Write(fullReq); err != nil {
		return
	}
	var repHdr [4]byte
	if _, err := io.ReadFull(up, repHdr[:]); err != nil {
		return
	}
	var repAddr []byte
	switch repHdr[3] {
	case 0x01:
		repAddr = make([]byte, 4+2)
		if _, err := io.ReadFull(up, repAddr); err != nil {
			return
		}
	case 0x03:
		var dlen [1]byte
		if _, err := io.ReadFull(up, dlen[:]); err != nil {
			return
		}
		rest := make([]byte, int(dlen[0])+2)
		if _, err := io.ReadFull(up, rest); err != nil {
			return
		}
		repAddr = append([]byte{dlen[0]}, rest...)
	case 0x04:
		repAddr = make([]byte, 16+2)
		if _, err := io.ReadFull(up, repAddr); err != nil {
			return
		}
	default:
		return
	}
	_ = binary.BigEndian // keep import clean if needed
	if _, err := client.Write(append(repHdr[:], repAddr...)); err != nil {
		return
	}
	if repHdr[1] != 0x00 {
		return
	}

	// Clear handshake deadlines and pipe bidirectionally.
	_ = client.SetDeadline(time.Time{})
	_ = up.SetDeadline(time.Time{})
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(up, client)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, up)
		done <- struct{}{}
	}()
	<-done
}

// Registry public Browser Profile methods.

func (r *Registry) ListBrowserProfiles() []BrowserProfileView {
	if r == nil {
		return nil
	}
	return r.profileStore().list()
}

func (r *Registry) SaveBrowserProfile(prof BrowserProfile, keepExistingPassword bool) (BrowserProfileView, error) {
	if r == nil {
		return BrowserProfileView{}, errors.New("registry is nil")
	}
	return r.profileStore().upsert(prof, keepExistingPassword)
}

func (r *Registry) DeleteBrowserProfile(id string) error {
	if r == nil {
		return errors.New("registry is nil")
	}
	return r.profileStore().remove(id)
}

func (r *Registry) ActiveBrowserProfile(sessionID string) BrowserProfileView {
	if r == nil {
		return builtInBrowserProfiles()[0].toView()
	}
	ps := r.profileStore()
	id := ps.activeProfileID(browserSessionKey(sessionID))
	p, _ := ps.get(id)
	return p.toView()
}

func (r *Registry) SetSessionBrowserProfile(sessionID, profileID string) (BrowserProfileView, error) {
	if r == nil {
		return BrowserProfileView{}, errors.New("registry is nil")
	}
	key := browserSessionKey(sessionID)
	ps := r.profileStore()
	prev := ps.activeProfileID(key)
	prof, err := ps.setActiveProfile(key, profileID)
	if err != nil {
		return BrowserProfileView{}, err
	}
	// If the profile changed while a daemon is running, restart daemon on next action
	// so the new profile's cookies & proxy take effect cleanly.
	if prev != prof.ID && r.browserPool != nil {
		if d := r.browserPool.getDaemon(key); d != nil {
			d.close()
		}
		r.browserPool.notifyObserver(key)
	}
	return prof.toView(), nil
}
