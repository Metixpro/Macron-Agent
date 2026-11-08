package main

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// localSendBudget is the time an answer may take to deliver through a local
// Bot API server: files up to 2000 MB go up on slow lines too.
const localSendBudget = 45 * time.Minute

// telegramLocal decides whether the Bot API server runs in local mode: as
// WAZE_TELEGRAM_LOCAL says, or, when unset, when it lives on this machine
// (by address, or a domain that points here) or a private network. Another
// public address is taken for a proxy of api.telegram.org, which keeps
// Telegram's limits.
func telegramLocal(base, setting string) bool {
	if on, err := strconv.ParseBool(strings.TrimSpace(setting)); err == nil {
		return on
	}
	if telegram.LocalServer(base) {
		return true
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || u.Hostname() == "api.telegram.org" {
		return false
	}
	return pointsHere(u.Hostname(), ownAddrs())
}

// pointsHere reports a domain whose every address is this machine's.
func pointsHere(host string, own map[netip.Addr]bool) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		if a = a.Unmap(); !own[a] && !a.IsLoopback() && !a.IsPrivate() {
			return false
		}
	}
	return true
}

// ownAddrs are the addresses of this machine's interfaces.
func ownAddrs() map[netip.Addr]bool {
	own := map[netip.Addr]bool{}
	ifs, err := net.InterfaceAddrs()
	if err != nil {
		return own
	}
	for _, a := range ifs {
		if p, err := netip.ParsePrefix(a.String()); err == nil {
			own[p.Addr().Unmap()] = true
		}
	}
	return own
}
