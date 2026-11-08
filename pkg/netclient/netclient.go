package netclient

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

type Options struct {
	Timeout time.Duration

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
	return &http.Client{
		Timeout:   opts.Timeout,
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
	b.timer = time.AfterFunc(idle, func() { b.stalled.Store(true); b.rc.Close() })
	resp.Body = b
	return resp, nil
}

type idleBody struct {
	rc      io.ReadCloser
	idle    time.Duration
	timer   *time.Timer
	stalled atomic.Bool
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 {
		b.timer.Reset(b.idle)
	}
	if err != nil && b.stalled.Load() {
		err = fmt.Errorf("the server stopped sending for %s", b.idle)
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
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
