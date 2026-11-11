package plugins

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Metixpro/macron-agent/pkg/guard"
	"github.com/Metixpro/macron-agent/pkg/machine"
)

const (
	mcpProtocol     = "2025-06-18"
	maxMCPMessage   = 16 << 20
	maxMCPTools     = 64
	maxMCPPages     = 10
	mcpStartTimeout = 3 * time.Minute
	mcpCallTimeout  = 2 * time.Minute
)

type ServerConfig struct {
	ID      string            `json:"id"`
	Name    string            `json:"name,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	Transport string `json:"transport,omitempty"`
	Enabled   *bool  `json:"enabled,omitempty"`
	Access    string `json:"access,omitempty"`

	TimeoutSeconds int   `json:"timeout_seconds,omitempty"`
	RequiresNet    *bool `json:"requires_net,omitempty"`
}

func (c *ServerConfig) Check() error {
	c.ID = strings.ToLower(strings.TrimSpace(c.ID))
	if !idRe.MatchString(c.ID) {
		return fmt.Errorf("server id %q: use 1-40 lowercase letters, digits, - or _", c.ID)
	}
	c.Name = oneLine(c.Name, 80)
	c.Command, c.URL = strings.TrimSpace(c.Command), strings.TrimSpace(c.URL)
	switch {
	case c.Command == "" && c.URL == "":
		return errors.New("an MCP server needs a command or a url")
	case c.Command != "" && c.URL != "":
		return errors.New("an MCP server has a command or a url, not both")
	case c.URL != "":
		u, err := url.Parse(c.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("url %q must look like https://host/mcp", c.URL)
		}
	}
	if len(c.Args) > 64 {
		return errors.New("too many args")
	}
	switch c.Access {
	case "":
		c.Access = AccessAdmins
	case AccessEveryone, AccessAdmins:
	default:
		return fmt.Errorf("access must be %q or %q", AccessEveryone, AccessAdmins)
	}
	for k := range c.Env {
		if !envNameRe.MatchString(k) || reservedEnv(k) {
			return fmt.Errorf("env %q: use UPPER_CASE names (not PATH, HOME, PYTHON…, MACRON…)", k)
		}
	}
	if c.TimeoutSeconds < 0 || c.TimeoutSeconds > 900 {
		return errors.New("timeout_seconds must be between 0 and 900")
	}
	return nil
}

func (c *ServerConfig) callTimeout() time.Duration {
	if c.TimeoutSeconds > 0 {
		return time.Duration(c.TimeoutSeconds) * time.Second
	}
	return mcpCallTimeout
}

func ParseServers(data []byte) ([]ServerConfig, error) {
	data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte("\ufeff")))
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	if raw, ok := top["mcpServers"]; ok {
		top = nil
		if err := json.Unmarshal(raw, &top); err != nil {
			return nil, fmt.Errorf("mcpServers: %w", err)
		}
	} else if _, one := top["command"]; one || top["url"] != nil {
		var c ServerConfig
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, err
		}
		if c.ID == "" {
			c.ID = serverID(c)
		}
		return []ServerConfig{c}, c.Check()
	}
	var out []ServerConfig
	for id, raw := range top {
		var c ServerConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", id, err)
		}
		if c.ID == "" {
			c.ID = slug(id)
		}
		if c.Name == "" {
			c.Name = id
		}
		if err := c.Check(); err != nil {
			return nil, fmt.Errorf("%s: %w", id, err)
		}
		out = append(out, c)
	}
	return out, nil
}

func ParseServerLine(text string) ([]ServerConfig, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "{") {
		return ParseServers([]byte(text))
	}
	var c ServerConfig
	if u, err := url.Parse(text); err == nil && (u.Scheme == "http" || u.Scheme == "https") && !strings.ContainsAny(text, " \n") {
		c.URL = text
	} else if first, _, _ := strings.Cut(text, " "); strings.Contains(first, "://") {
		return nil, errors.New("an MCP server's address must start with http:// or https://")
	} else {
		f, err := splitCommand(text)
		if err != nil {
			return nil, err
		}
		if len(f) == 0 {
			return nil, errors.New("send a command or an address")
		}
		c.Command, c.Args = f[0], f[1:]
	}
	c.ID = serverID(c)
	return []ServerConfig{c}, c.Check()
}

func serverID(c ServerConfig) string {
	if c.URL != "" {
		if u, err := url.Parse(c.URL); err == nil {
			return slug(strings.TrimPrefix(u.Hostname(), "www."))
		}
	}
	name := c.Command
	for _, a := range c.Args {
		if !strings.HasPrefix(a, "-") {
			name = a
			break
		}
	}
	name = name[strings.LastIndex(name, "/")+1:]
	name = strings.TrimPrefix(strings.TrimPrefix(name, "server-"), "mcp-server-")
	if i := strings.Index(name, "@"); i > 0 {
		name = name[:i]
	}
	return slug(strings.TrimSuffix(strings.TrimPrefix(name, "mcp-"), "-mcp"))
}

func splitCommand(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	quote, has := rune(0), false
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			quote, has = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if cur.Len() > 0 || has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, errors.New("a quote is not closed")
	}
	if cur.Len() > 0 || has {
		out = append(out, cur.String())
	}
	return out, nil
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message) }

type rpcReply struct {
	result json.RawMessage
	err    error
}

type transport interface {
	request(ctx context.Context, method string, params any) (json.RawMessage, error)
	notify(ctx context.Context, method string, params any) error
	alive() bool
	close()
}

type waiters struct {
	mu      sync.Mutex
	next    atomic.Int64
	pending map[string]chan rpcReply
	dead    error
}

func (w *waiters) add() (string, chan rpcReply, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dead != nil {
		return "", nil, w.dead
	}
	if w.pending == nil {
		w.pending = map[string]chan rpcReply{}
	}
	id := strconv.FormatInt(w.next.Add(1), 10)
	ch := make(chan rpcReply, 1)
	w.pending[id] = ch
	return id, ch, nil
}

func (w *waiters) drop(id string) {
	w.mu.Lock()
	delete(w.pending, id)
	w.mu.Unlock()
}

func (w *waiters) deliver(m rpcMessage) {
	id := strings.Trim(string(m.ID), `"`)
	w.mu.Lock()
	ch, ok := w.pending[id]
	delete(w.pending, id)
	w.mu.Unlock()
	if !ok {
		return
	}
	if m.Error != nil {
		ch <- rpcReply{err: m.Error}
		return
	}
	ch <- rpcReply{result: m.Result}
}

func (w *waiters) fail(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dead == nil {
		w.dead = err
	}
	for id, ch := range w.pending {
		ch <- rpcReply{err: err}
		delete(w.pending, id)
	}
}

func (w *waiters) wait(ctx context.Context, id string, ch chan rpcReply) (json.RawMessage, error) {
	select {
	case r := <-ch:
		return r.result, r.err
	case <-ctx.Done():
		w.drop(id)
		return nil, ctx.Err()
	}
}

func encodeRequest(id, method string, params any) ([]byte, error) {
	m := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != "" {
		m["id"], _ = strconv.ParseInt(id, 10, 64)
	}
	if params != nil {
		m["params"] = params
	}
	return json.Marshal(m)
}

func answerServer(m rpcMessage) []byte {
	reply := map[string]any{"jsonrpc": "2.0", "id": m.ID}
	if m.Method == "ping" {
		reply["result"] = map[string]any{}
	} else {
		reply["error"] = map[string]any{"code": -32601, "message": "not supported by this client"}
	}
	data, _ := json.Marshal(reply)
	return data
}

type stdioConn struct {
	p  *machine.Process
	wr sync.Mutex
	w  waiters
}

func startStdio(ctx context.Context, sb *machine.Machine, session string, opts machine.RunOptions) (*stdioConn, error) {
	p, err := sb.Start(ctx, session, opts)
	if err != nil {
		return nil, err
	}
	c := &stdioConn{p: p}
	go c.read()
	return c, nil
}

func (c *stdioConn) read() {
	sc := bufio.NewScanner(c.p.Stdout)
	sc.Buffer(make([]byte, 64<<10), maxMCPMessage)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var m rpcMessage
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		switch {
		case m.Method != "" && len(m.ID) > 0:
			c.write(answerServer(m))
		case m.Method != "":
		case len(m.ID) > 0:
			c.w.deliver(m)
		}
	}
	<-c.p.Done()
	why := "the server ended"
	if e := c.p.Stderr(); e != "" {
		why += ": " + tail(e, 400)
	}
	c.w.fail(errors.New(why))
}

func (c *stdioConn) write(data []byte) error {
	c.wr.Lock()
	defer c.wr.Unlock()
	_, err := c.p.Stdin.Write(append(data, '\n'))
	return err
}

func (c *stdioConn) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id, ch, err := c.w.add()
	if err != nil {
		return nil, err
	}
	data, err := encodeRequest(id, method, params)
	if err != nil {
		c.w.drop(id)
		return nil, err
	}
	if err := c.write(data); err != nil {
		c.w.drop(id)
		return nil, fmt.Errorf("the server is not running: %w", err)
	}
	return c.w.wait(ctx, id, ch)
}

func (c *stdioConn) notify(_ context.Context, method string, params any) error {
	data, err := encodeRequest("", method, params)
	if err != nil {
		return err
	}
	return c.write(data)
}

func (c *stdioConn) alive() bool {
	select {
	case <-c.p.Done():
		return false
	default:
		return true
	}
}

func (c *stdioConn) close() { c.p.Close() }

type httpConn struct {
	url     string
	headers map[string]string
	client  *http.Client
	session atomic.Value
	proto   atomic.Value
	next    atomic.Int64
	gone    atomic.Bool
}

func (c *httpConn) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", "macron-agent")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if s, _ := c.session.Load().(string); s != "" {
		req.Header.Set("Mcp-Session-Id", s)
	}
	if p, _ := c.proto.Load().(string); p != "" {
		req.Header.Set("MCP-Protocol-Version", p)
	}
	return c.client.Do(req)
}

func (c *httpConn) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := strconv.FormatInt(c.next.Add(1), 10)
	body, err := encodeRequest(id, method, params)
	if err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		c.session.Store(s)
	}
	if resp.StatusCode == http.StatusNotFound && c.session.Load() != nil {
		c.gone.Store(true)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, &httpStatusError{status: resp.StatusCode, body: strings.TrimSpace(string(msg))}
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if ct == "text/event-stream" {
		var out json.RawMessage
		var rerr error
		found := false
		err := readSSE(io.LimitReader(resp.Body, maxMCPMessage), func(event, data string) bool {
			var m rpcMessage
			if json.Unmarshal([]byte(data), &m) != nil || strings.Trim(string(m.ID), `"`) != id || m.Method != "" {
				return true
			}
			found = true
			if m.Error != nil {
				rerr = m.Error
			} else {
				out = m.Result
			}
			return false
		})
		if found {
			return out, rerr
		}
		if err != nil {
			return nil, err
		}
		return nil, errors.New("the server's stream ended without an answer")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMCPMessage))
	if err != nil {
		return nil, err
	}
	data = bytes.TrimSpace(data)
	var msgs []rpcMessage
	if len(data) > 0 && data[0] == '[' {
		_ = json.Unmarshal(data, &msgs)
	} else {
		var m rpcMessage
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("not a JSON-RPC answer: %s", tail(string(data), 120))
		}
		msgs = []rpcMessage{m}
	}
	for _, m := range msgs {
		if strings.Trim(string(m.ID), `"`) == id && m.Method == "" {
			if m.Error != nil {
				return nil, m.Error
			}
			return m.Result, nil
		}
	}
	return nil, errors.New("the server sent no answer to the request")
}

func (c *httpConn) notify(ctx context.Context, method string, params any) error {
	body, err := encodeRequest("", method, params)
	if err != nil {
		return err
	}
	resp, err := c.post(ctx, body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *httpConn) alive() bool { return !c.gone.Load() }

func (c *httpConn) close() {
	s, _ := c.session.Load().(string)
	if s == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Mcp-Session-Id", s)
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if resp, err := c.client.Do(req); err == nil {
		resp.Body.Close()
	}
}

type httpStatusError struct {
	status int
	body   string
}

func (e *httpStatusError) Error() string {
	if e.body != "" {
		return fmt.Sprintf("HTTP %d: %s", e.status, tail(e.body, 200))
	}
	return fmt.Sprintf("HTTP %d", e.status)
}

func readSSE(r io.Reader, each func(event, data string) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxMCPMessage)
	event, data := "", strings.Builder{}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if data.Len() > 0 {
				if !each(event, data.String()) {
					return nil
				}
			}
			event = ""
			data.Reset()
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if data.Len() > 0 {
		each(event, data.String())
	}
	return sc.Err()
}

type sseConn struct {
	endpoint string
	headers  map[string]string
	client   *http.Client
	body     io.Closer
	w        waiters
	done     chan struct{}
}

func startSSE(ctx context.Context, client *http.Client, raw string, headers map[string]string) (*sseConn, error) {
	base, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, raw, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	type opened struct {
		resp *http.Response
		err  error
	}
	got := make(chan opened, 1)
	go func() { resp, err := client.Do(req); got <- opened{resp, err} }()
	var resp *http.Response
	select {
	case o := <-got:
		if o.err != nil {
			cancel()
			return nil, o.err
		}
		resp = o.resp
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return nil, &httpStatusError{status: resp.StatusCode}
	}
	c := &sseConn{headers: headers, client: client, body: closerFunc(func() error { cancel(); return resp.Body.Close() }), done: make(chan struct{})}
	endpoint := make(chan string, 1)
	go func() {
		defer close(c.done)
		defer guard.Recover("an MCP server's event stream")
		err := readSSE(resp.Body, func(event, data string) bool {
			switch event {
			case "endpoint":
				select {
				case endpoint <- strings.TrimSpace(data):
				default:
				}
			case "", "message":
				var m rpcMessage
				if json.Unmarshal([]byte(data), &m) != nil {
					return true
				}
				switch {
				case m.Method != "" && len(m.ID) > 0:
					go c.postRaw(context.Background(), answerServer(m))
				case m.Method == "" && len(m.ID) > 0:
					c.w.deliver(m)
				}
			}
			return true
		})
		if err == nil {
			err = errors.New("the server closed its event stream")
		}
		c.w.fail(err)
	}()
	select {
	case e := <-endpoint:
		u, err := base.Parse(e)
		if err != nil || u.Scheme != base.Scheme || u.Host != base.Host {
			c.close()
			return nil, errors.New("the server's message endpoint is on another site")
		}
		c.endpoint = u.String()
		return c, nil
	case <-c.done:
		return nil, errors.New("the server sent no message endpoint")
	case <-ctx.Done():
		c.close()
		return nil, ctx.Err()
	}
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

func (c *sseConn) postRaw(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return &httpStatusError{status: resp.StatusCode, body: strings.TrimSpace(string(msg))}
	}
	return nil
}

func (c *sseConn) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id, ch, err := c.w.add()
	if err != nil {
		return nil, err
	}
	body, err := encodeRequest(id, method, params)
	if err != nil {
		c.w.drop(id)
		return nil, err
	}
	if err := c.postRaw(ctx, body); err != nil {
		c.w.drop(id)
		return nil, err
	}
	return c.w.wait(ctx, id, ch)
}

func (c *sseConn) notify(ctx context.Context, method string, params any) error {
	body, err := encodeRequest("", method, params)
	if err != nil {
		return err
	}
	return c.postRaw(ctx, body)
}

func (c *sseConn) alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

func (c *sseConn) close() { _ = c.body.Close() }

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations struct {
		ReadOnlyHint bool `json:"readOnlyHint"`
	} `json:"annotations"`
}

func handshake(ctx context.Context, t transport, version string) ([]mcpTool, string, error) {
	res, err := t.request(ctx, "initialize", map[string]any{
		"protocolVersion": mcpProtocol,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "macron-agent", "version": version},
	})
	if err != nil {
		return nil, "", fmt.Errorf("initialize: %w", err)
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Instructions string `json:"instructions"`
	}
	_ = json.Unmarshal(res, &init)
	if hc, ok := t.(*httpConn); ok && init.ProtocolVersion != "" {
		hc.proto.Store(init.ProtocolVersion)
	}
	if err := t.notify(ctx, "notifications/initialized", nil); err != nil {
		return nil, "", err
	}
	var tools []mcpTool
	cursor := ""
	for page := 0; page < maxMCPPages && len(tools) < maxMCPTools; page++ {
		var params any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		res, err := t.request(ctx, "tools/list", params)
		if err != nil {
			return nil, "", fmt.Errorf("tools/list: %w", err)
		}
		var list struct {
			Tools      []mcpTool `json:"tools"`
			NextCursor string    `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &list); err != nil {
			return nil, "", fmt.Errorf("tools/list: %w", err)
		}
		tools = append(tools, list.Tools...)
		if cursor = list.NextCursor; cursor == "" {
			break
		}
	}
	if len(tools) > maxMCPTools {
		tools = tools[:maxMCPTools]
	}
	name := strings.TrimSpace(init.ServerInfo.Name + " " + init.ServerInfo.Version)
	return tools, name, nil
}

func callResult(raw json.RawMessage, save func(ext string, data []byte) (string, error)) string {
	var res struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
			Resource *struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
				Blob string `json:"blob"`
			} `json:"resource"`
			URI  string `json:"uri"`
			Name string `json:"name"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return string(raw)
	}
	var parts []string
	for _, c := range res.Content {
		switch c.Type {
		case "text":
			parts = append(parts, c.Text)
		case "image", "audio":
			data, err := base64.StdEncoding.DecodeString(c.Data)
			if err != nil || len(data) == 0 || len(data) > 20<<20 || save == nil {
				parts = append(parts, fmt.Sprintf("[%s %s, not saved]", c.Type, c.MimeType))
				continue
			}
			ext := ".bin"
			if exts, _ := mime.ExtensionsByType(c.MimeType); len(exts) > 0 {
				ext = exts[len(exts)-1]
			}
			if name, err := save(ext, data); err == nil {
				parts = append(parts, fmt.Sprintf("[%s saved in the workspace as %s]", c.Type, name))
			} else {
				parts = append(parts, fmt.Sprintf("[%s not saved: %v]", c.Type, err))
			}
		case "resource":
			if c.Resource != nil {
				if c.Resource.Text != "" {
					parts = append(parts, c.Resource.Text)
				} else {
					parts = append(parts, "[resource "+c.Resource.URI+"]")
				}
			}
		case "resource_link":
			parts = append(parts, fmt.Sprintf("[link %s %s]", c.Name, c.URI))
		}
	}
	if len(parts) == 0 && len(res.StructuredContent) > 0 {
		parts = append(parts, string(res.StructuredContent))
	}
	out := strings.TrimSpace(strings.Join(parts, "\n"))
	if res.IsError {
		return "Error from the tool: " + out
	}
	if out == "" {
		return "(no output)"
	}
	return out
}

func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}
