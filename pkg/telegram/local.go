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

const (
	MaxDownloadBytes = 20 << 20

	LocalMaxBytes = 2000 << 20

	localHeaderWait = 30 * time.Minute
)

func (b *Bot) SetLocal(on bool) {
	b.local = on
	if t, ok := b.UploadClient.Transport.(*http.Transport); ok && on {
		t.ResponseHeaderTimeout = localHeaderWait
	}
}

func (b *Bot) Local() bool { return b.local }

func (b *Bot) UploadLimit() int64 {
	if b.local {
		return LocalMaxBytes
	}
	return MaxUploadBytes
}

func (b *Bot) DownloadLimit() int64 {
	if b.local {
		return LocalMaxBytes
	}
	return MaxDownloadBytes
}

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
	return !strings.Contains(host, ".")
}

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
