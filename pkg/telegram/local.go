package telegram

import (
	"io"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

// A local Bot API server (github.com/tdlib/telegram-bot-api, started with
// --local) lifts Telegram's file limits: the bot sends files up to 2000 MB
// and receives any size, and getFile names the file on the server's disk,
// which the bot reads directly instead of downloading it again. Without
// --local (or through a proxy to api.telegram.org) the usual limits stay.

const (
	// MaxDownloadBytes is what bots can download from Telegram's own servers.
	MaxDownloadBytes = 20 << 20
	// LocalMaxBytes is the file limit through a local Bot API server (both ways).
	LocalMaxBytes = 2000 << 20

	// localHeaderWait is how long an upload may wait for the answer through a
	// local server: it answers once it has sent the file on to Telegram,
	// which takes minutes for a big file on a slow line.
	localHeaderWait = 30 * time.Minute
)

// SetLocal tells the bot that its API server runs in local mode.
func (b *Bot) SetLocal(on bool) {
	b.local = on
	if t, ok := b.UploadClient.Transport.(*http.Transport); ok && on {
		t.ResponseHeaderTimeout = localHeaderWait
	}
}

// Local reports a local Bot API server.
func (b *Bot) Local() bool { return b.local }

// UploadLimit is the largest file the bot can send.
func (b *Bot) UploadLimit() int64 {
	if b.local {
		return LocalMaxBytes
	}
	return MaxUploadBytes
}

// DownloadLimit is the largest file the bot can receive.
func (b *Bot) DownloadLimit() int64 {
	if b.local {
		return LocalMaxBytes
	}
	return MaxDownloadBytes
}

// LocalServer guesses whether a Bot API address is a local server: on this
// machine, a private network or a container name ("telegram-bot-api:8081").
// A public address is taken for a proxy of api.telegram.org.
func LocalServer(base string) bool {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return true
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast()
	}
	return !strings.Contains(host, ".") // a single-label name: a container or a host on the LAN
}

// readLocal copies a file a local server named by its path on disk; ok is
// false when it cannot be read here (another machine or container, or no
// permission), and the file is then downloaded over HTTP.
func (b *Bot) readLocal(path string, w io.Writer, max int64) (n int64, ok bool, err error) {
	if !b.local || !strings.HasPrefix(path, "/") {
		return 0, false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		b.localWarn.Do(func() {
			log.Printf("telegram: the local Bot API server's files are not readable here (%v); they are downloaded over HTTP. "+
				"Run the bot on the same machine as the server, with its folder at the same path and readable by the bot.", err)
		})
		return 0, false, nil
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return 0, false, nil
	} else if fi.Size() > max {
		return 0, true, ErrFileTooLarge
	}
	n, err = io.Copy(w, io.LimitReader(f, max+1))
	if err == nil && n > max {
		err = ErrFileTooLarge
	}
	return n, true, err
}
