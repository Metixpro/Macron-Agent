package desk

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const remoteScript = `for m in "$(command -v macron 2>/dev/null)" "$HOME/.local/bin/macron" /usr/local/bin/macron /usr/bin/macron; do ` +
	`if [ -n "$m" ] && [ -x "$m" ]; then exec "$m" desktop --serve --addr 127.0.0.1:0 --json; fi; done; ` +
	`echo "Macron is not installed on this machine" >&2; exit 127`

type Secrets struct {
	Password   string `json:"password,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	Trust      string `json:"trust,omitempty"`
}

type NeedError struct {
	Need        string
	Fingerprint string
	Message     string
}

func (e *NeedError) Error() string { return e.Message }

type transport interface {
	start(ctx context.Context) (io.WriteCloser, io.Reader, func() string, error)
	dial(ctx context.Context, addr string) (net.Conn, error)
	done() <-chan struct{}
	close()
}

type sshTransport struct {
	client *ssh.Client
	sess   *ssh.Session
	ended  chan struct{}
	once   sync.Once
}

func sshDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ssh")
}

func knownHostsFile() string { return filepath.Join(sshDir(), "known_hosts") }

var preferredHostKeys = []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}

func hostKeyAlgos(known ssh.HostKeyCallback, addr string, remote net.Addr) []string {
	_, probe, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return preferredHostKeys
	}
	signer, err := ssh.NewSignerFromKey(probe)
	if err != nil {
		return preferredHostKeys
	}
	var ke *knownhosts.KeyError
	if err := known(addr, remote, signer.PublicKey()); err == nil || !errors.As(err, &ke) || len(ke.Want) == 0 {
		return preferredHostKeys
	}
	have := map[string]bool{}
	for _, k := range ke.Want {
		have[k.Key.Type()] = true
	}
	var out []string
	for _, a := range preferredHostKeys {
		t := a
		if a == ssh.KeyAlgoRSASHA512 || a == ssh.KeyAlgoRSASHA256 {
			t = ssh.KeyAlgoRSA
		}
		if have[t] {
			out = append(out, a)
		}
	}
	if have[ssh.KeyAlgoRSA] {
		out = append(out, ssh.KeyAlgoRSA)
	}
	if len(out) == 0 {
		return preferredHostKeys
	}
	return out
}

func knownHosts() (ssh.HostKeyCallback, string, error) {
	path := knownHostsFile()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, "", err
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			return nil, "", err
		}
	}
	known, err := knownhosts.New(path)
	if err != nil {
		return nil, "", fmt.Errorf("could not read %s: %w", path, err)
	}
	return known, path, nil
}

func hostKeyCallback(known ssh.HostKeyCallback, path, trust string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := known(hostname, remote, key)
		var ke *knownhosts.KeyError
		if err == nil || !errors.As(err, &ke) {
			return err
		}
		fp := ssh.FingerprintSHA256(key)
		if len(ke.Want) > 0 {
			return fmt.Errorf("the server's host key changed (now %s); if you did not reinstall it, someone may be in the middle. Check ~/.ssh/known_hosts", fp)
		}
		if trust != fp {
			return &NeedError{Need: "trust", Fingerprint: fp, Message: "first connection to this server: check its key fingerprint"}
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = fmt.Fprintln(f, knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key))
		return err
	}
}

func keySigners(spec ServerSpec, passphrase string) ([]ssh.Signer, bool) {
	paths := []string{spec.Key}
	if spec.Key == "" {
		for _, n := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			paths = append(paths, filepath.Join(sshDir(), n))
		}
	}
	var out []ssh.Signer
	locked := false
	for _, p := range paths {
		if p == "" {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s, err := ssh.ParsePrivateKey(raw)
		var pe *ssh.PassphraseMissingError
		if errors.As(err, &pe) {
			if passphrase == "" {
				locked = true
				continue
			}
			s, err = ssh.ParsePrivateKeyWithPassphrase(raw, []byte(passphrase))
		}
		if err == nil {
			out = append(out, s)
		}
	}
	return out, locked
}

func dialSSH(ctx context.Context, spec ServerSpec, sec Secrets) (*sshTransport, error) {
	known, khPath, err := knownHosts()
	if err != nil {
		return nil, err
	}
	var methods []ssh.AuthMethod
	if ag := sshAgent(); ag != nil {
		methods = append(methods, ssh.PublicKeysCallback(ag.Signers))
	}
	signers, locked := keySigners(spec, sec.Passphrase)
	if len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}
	if sec.Password != "" {
		pw := sec.Password
		methods = append(methods, ssh.Password(pw), ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			out := make([]string, len(qs))
			for i := range out {
				out[i] = pw
			}
			return out, nil
		}))
	}
	user := spec.User
	if user == "" {
		user = "root"
	}
	port := spec.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(spec.Host, strconv.Itoa(port))
	d := net.Dialer{Timeout: 20 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s: %w", addr, err)
	}
	cfg := &ssh.ClientConfig{User: user, Auth: methods, HostKeyCallback: hostKeyCallback(known, khPath, sec.Trust),
		HostKeyAlgorithms: hostKeyAlgos(known, addr, conn.RemoteAddr()), Timeout: 20 * time.Second}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		var need *NeedError
		if errors.As(err, &need) {
			return nil, need
		}
		if strings.Contains(err.Error(), "unable to authenticate") {
			switch {
			case locked && sec.Passphrase == "":
				return nil, &NeedError{Need: "passphrase", Message: "your SSH key is locked with a passphrase"}
			case sec.Password == "":
				return nil, &NeedError{Need: "password", Message: "the server wants a password"}
			}
			return nil, errors.New("the server refused the login: check the user name and password")
		}
		return nil, errors.New(strings.TrimPrefix(err.Error(), "ssh: handshake failed: "))
	}
	_ = conn.SetDeadline(time.Time{})
	t := &sshTransport{client: ssh.NewClient(c, chans, reqs), ended: make(chan struct{})}
	go func() {
		_ = t.client.Wait()
		t.close()
	}()
	go t.keepalive()
	return t, nil
}

func (t *sshTransport) keepalive() {
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-t.ended:
			return
		case <-tick.C:
			if _, _, err := t.client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				t.close()
				return
			}
		}
	}
}

func (t *sshTransport) start(context.Context) (io.WriteCloser, io.Reader, func() string, error) {
	sess, err := t.client.NewSession()
	if err != nil {
		return nil, nil, nil, err
	}
	stdin, err1 := sess.StdinPipe()
	stdout, err2 := sess.StdoutPipe()
	if err := errors.Join(err1, err2); err != nil {
		sess.Close()
		return nil, nil, nil, err
	}
	errs := &tailBuffer{max: 4096}
	sess.Stderr = errs
	if err := sess.Start("sh -lc '" + remoteScript + "'"); err != nil {
		sess.Close()
		return nil, nil, nil, err
	}
	t.sess = sess
	go func() {
		_ = sess.Wait()
		t.close()
	}()
	return stdin, stdout, errs.String, nil
}

const installScript = `url=https://raw.githubusercontent.com/Metixpro/Macron-Agent/main/install.sh; ` +
	`if command -v curl >/dev/null 2>&1; then curl -fsSL "$url"; elif command -v wget >/dev/null 2>&1; then wget -qO- "$url"; ` +
	`else echo "this machine has neither curl nor wget" >&2; exit 1; fi | bash -s -- --yes --no-setup --no-service`

func (t *sshTransport) run(ctx context.Context, command string) (string, error) {
	sess, err := t.client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out := &tailBuffer{max: 16384}
	sess.Stdout, sess.Stderr = out, out
	done := make(chan error, 1)
	go func() { done <- sess.Run(command) }()
	select {
	case err := <-done:
		return out.String(), err
	case <-ctx.Done():
		_ = sess.Close()
		return out.String(), ctx.Err()
	}
}

func (t *sshTransport) dial(ctx context.Context, addr string) (net.Conn, error) {
	type result struct {
		c   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := t.client.Dial("tcp", addr)
		ch <- result{c, err}
	}()
	select {
	case r := <-ch:
		return r.c, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *sshTransport) done() <-chan struct{} { return t.ended }

func (t *sshTransport) close() {
	t.once.Do(func() {
		if t.sess != nil {
			_ = t.sess.Close()
		}
		_ = t.client.Close()
		close(t.ended)
	})
}

type execTransport struct {
	argv  []string
	cmd   *exec.Cmd
	ended chan struct{}
	once  sync.Once
}

func wslArgv(distro string) []string {
	argv := []string{"wsl.exe"}
	if distro != "" {
		argv = append(argv, "-d", distro)
	}
	return append(argv, "--", "sh", "-lc", remoteScript)
}

func (t *execTransport) start(context.Context) (io.WriteCloser, io.Reader, func() string, error) {
	cmd := exec.Command(t.argv[0], t.argv[1:]...)
	hideWindow(cmd)
	stdin, err1 := cmd.StdinPipe()
	stdout, err2 := cmd.StdoutPipe()
	if err := errors.Join(err1, err2); err != nil {
		return nil, nil, nil, err
	}
	errs := &tailBuffer{max: 4096}
	cmd.Stderr = errs
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, err
	}
	t.cmd = cmd
	go func() {
		_ = cmd.Wait()
		t.close()
	}()
	return stdin, stdout, errs.String, nil
}

func (t *execTransport) dial(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}

func (t *execTransport) done() <-chan struct{} { return t.ended }

func (t *execTransport) close() {
	t.once.Do(func() {
		if t.cmd != nil && t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
		close(t.ended)
	})
}

type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.b))
}
