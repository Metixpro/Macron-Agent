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

const localSendBudget = 45 * time.Minute

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
