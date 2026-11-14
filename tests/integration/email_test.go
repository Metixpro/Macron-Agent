package integration

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/Metixpro/macron-agent/pkg/tools"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var cert tls.Certificate

func TestMain(m *testing.M) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	cert = s.TLS.Certificates[0]
	s.Close()
	dir, _ := os.MkdirTemp("", "macron-ca-")
	path := filepath.Join(dir, "ca.pem")
	roots := os.Getenv("SSL_CERT_FILE")
	if roots == "" {
		roots = "/etc/ssl/certs/ca-certificates.crt"
	}
	existing, _ := os.ReadFile(roots)
	_ = os.WriteFile(path, append(existing, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})...), 0600)
	os.Setenv("SSL_CERT_FILE", path)
	code := m.Run()
	os.RemoveAll(dir)
	os.RemoveAll(binaryDir)
	os.Exit(code)
}
func smtpServer(t *testing.T, mode string) (int, <-chan string) {
	t.Helper()
	if runtime.GOOS == "windows" && (mode == "implicit" || mode == "starttls") {
		t.Skip("fixture CA via SSL_CERT_FILE is Unix-only; TLS scenarios run in Linux CI")
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { ln.Close() })
	out := make(chan string, 1)
	go func() {
		c, e := ln.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(3 * time.Second))
		secure := mode == "implicit"
		if secure {
			c = tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		}
		if mode == "silent" {
			b := make([]byte, 1)
			_, _ = c.Read(b)
			return
		}
		fmt.Fprint(c, "220 localhost ESMTP\r\n")
		r := bufio.NewReader(c)
		var data strings.Builder
		for {
			line, e := r.ReadString('\n')
			if e != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
				if mode == "starttls" && !secure {
					fmt.Fprint(c, "250-localhost\r\n250 STARTTLS\r\n")
				} else {
					fmt.Fprint(c, "250-localhost\r\n250 AUTH PLAIN\r\n")
				}
			case line == "STARTTLS":
				fmt.Fprint(c, "220 ready\r\n")
				c = tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
				r = bufio.NewReader(c)
				secure = true
			case strings.HasPrefix(line, "AUTH "):
				fmt.Fprint(c, "235 authenticated\r\n")
			case line == "DATA":
				fmt.Fprint(c, "354 data\r\n")
				for {
					line, e := r.ReadString('\n')
					if e != nil {
						return
					}
					if line == ".\r\n" {
						break
					}
					data.WriteString(line)
				}
				out <- data.String()
				fmt.Fprint(c, "250 accepted\r\n")
			case line == "QUIT":
				fmt.Fprint(c, "221 bye\r\n")
				return
			default:
				fmt.Fprint(c, "250 OK\r\n")
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, out
}
func TestSMTPTransportsAndPersian(t *testing.T) {
	for _, mode := range []string{"implicit", "starttls"} {
		t.Run(mode, func(t *testing.T) {
			port, received := smtpServer(t, mode)
			t.Setenv("MACRON_SMTP_TLS", "")
			if mode == "implicit" {
				t.Setenv("MACRON_SMTP_TLS", "implicit")
			}
			err := tools.SendEmail(context.Background(), "127.0.0.1", port, "owner@example.invalid", "test-password", "", "receiver@example.invalid", "آزمایش فارسی", "سلام دنیا")
			if err != nil {
				t.Fatal(err)
			}
			raw := <-received
			msg, e := mail.ReadMessage(strings.NewReader(raw))
			if e != nil {
				t.Fatal(e)
			}
			subject, e := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
			if e != nil || subject != "آزمایش فارسی" {
				t.Fatal(subject, e)
			}
			body, _ := io.ReadAll(quotedprintable.NewReader(msg.Body))
			if string(body) != "سلام دنیا\r\n" {
				t.Fatalf("unexpected body %q", body)
			}
		})
	}
}
func TestSMTPCancellation(t *testing.T) {
	port, _ := smtpServer(t, "silent")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := tools.SendEmail(ctx, "127.0.0.1", port, "", "", "sender@example.invalid", "receiver@example.invalid", "test", "test")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancellation was ignored")
	}
}
func TestSMTPHeaderInjection(t *testing.T) {
	err := tools.SendEmail(context.Background(), "127.0.0.1", 1, "", "", "sender@example.invalid", "receiver@example.invalid", "subject\r\nBcc: other@example.invalid", "test")
	if err == nil || !strings.Contains(err.Error(), "headers") {
		t.Fatal(err)
	}
}
