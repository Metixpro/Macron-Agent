package netclient

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultTimeout is applied to clients created via NewClient when neither
// Timeout nor NoTotalTimeout is set, preventing outbound link/API requests
// from hanging indefinitely.
const DefaultTimeout = 45 * time.Second

type Options struct {
	Timeout time.Duration

	// NoTotalTimeout disables the default total request timeout for long-lived
	// streams or large archive downloads (HeaderTimeout and BodyIdle still apply).
	NoTotalTimeout bool

	UseEnvProxy bool

	HeaderTimeout, BodyIdle time.Duration
}

func NewClient(opts Options) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	tr := &http.Transport{
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          50,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: cmp.Or(opts.HeaderTimeout, 30*time.Second),
		ExpectContinueTimeout: time.Second,
	}
	if opts.UseEnvProxy {
		tr.Proxy = http.ProxyFromEnvironment
	}
	timeout := opts.Timeout
	if timeout == 0 && !opts.NoTotalTimeout {
		timeout = DefaultTimeout
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: idleTransport{next: tr, idle: opts.BodyIdle},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("stopped after 5 redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to unsupported scheme %q", req.URL.Scheme)
			}
			return nil
		},
	}
}

// WithRequestTimeout clamps seconds into [1, maxSeconds] (falling back to
// defaultSeconds when seconds <= 0) and returns a derived context with that
// deadline, ensuring every link fetch has an explicit bounded timeout.
func WithRequestTimeout(ctx context.Context, seconds, defaultSeconds, maxSeconds int) (context.Context, context.CancelFunc) {
	if defaultSeconds <= 0 {
		defaultSeconds = int(DefaultTimeout / time.Second)
	}
	if maxSeconds <= 0 {
		maxSeconds = 120
	}
	if seconds <= 0 {
		seconds = defaultSeconds
	}
	if seconds > maxSeconds {
		seconds = maxSeconds
	}
	return context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
}

var bodyIdle = 60 * time.Second

type idleTransport struct {
	next http.RoundTripper
	idle time.Duration
}

func (t idleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil || resp.Body == nil {
		return resp, err
	}
	idle := cmp.Or(t.idle, bodyIdle)
	b := &idleBody{rc: resp.Body, idle: idle}
	b.timer = time.AfterFunc(idle, func() { b.stalled.Store(true); _ = b.rc.Close() })
	resp.Body = b
	return resp, nil
}

type idleBody struct {
	rc      io.ReadCloser
	idle    time.Duration
	mu      sync.Mutex
	timer   *time.Timer
	closed  bool
	stalled atomic.Bool
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 {
		b.mu.Lock()
		if !b.closed {
			b.timer.Reset(b.idle)
		}
		b.mu.Unlock()
	}
	if err != nil && b.stalled.Load() {
		err = fmt.Errorf("the server stopped sending for %s", b.idle)
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.mu.Lock()
	b.closed = true
	b.timer.Stop()
	b.mu.Unlock()
	return b.rc.Close()
}

func ValidateURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported URL scheme %q (only http/https)", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, errors.New("URL has no host")
	}
	return u, nil
}
