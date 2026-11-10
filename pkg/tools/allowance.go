package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

type Allowance struct {
	Images    int
	Downloads int

	mu                sync.Mutex
	images, downloads int
}

type allowanceKey struct{}

func WithAllowance(ctx context.Context, a *Allowance) context.Context {
	return context.WithValue(ctx, allowanceKey{}, a)
}

func allowanceOf(ctx context.Context) *Allowance {
	a, _ := ctx.Value(allowanceKey{}).(*Allowance)
	return a
}

const (
	kindImages    = "pictures"
	kindDownloads = "downloads"
)

func (a *Allowance) counter(kind string) (limit int, used *int) {
	if kind == kindImages {
		return a.Images, &a.images
	}
	return a.Downloads, &a.downloads
}

func take(ctx context.Context, kind string, n int) (granted int, note string) {
	a := allowanceOf(ctx)
	if a == nil || n <= 0 {
		return n, ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	limit, used := a.counter(kind)
	if limit <= 0 {
		return n, ""
	}
	granted = min(n, max(limit-*used, 0))
	*used += granted
	if granted < n {
		note = fmt.Sprintf("Limit: one request gets at most %d %s (the user's plan), and %d are used. Deliver what you have and tell the "+
			"user in one short line that this is the limit for one request; they can send another request for more. Don't try other "+
			"tools or code to get around it.", limit, kind, *used)
	}
	return granted, note
}

func giveBack(ctx context.Context, kind string, n int) {
	a := allowanceOf(ctx)
	if a == nil || n <= 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, used := a.counter(kind)
	*used = max(*used-n, 0)
}

func firstItems(items string, n int) string {
	var out []string
	for _, part := range strings.Split(items, ",") {
		if n <= 0 {
			break
		}
		a, b, ok := strings.Cut(part, "-")
		x, _ := strconv.Atoi(a)
		y := x
		if ok {
			y, _ = strconv.Atoi(b)
		}
		if y-x+1 > n {
			y = x + n - 1
		}
		if x == y {
			out = append(out, strconv.Itoa(x))
		} else {
			out = append(out, fmt.Sprintf("%d-%d", x, y))
		}
		n -= y - x + 1
	}
	return strings.Join(out, ",")
}

func oneDownload(h ToolHandler) ToolHandler {
	return func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
		if n, note := take(ctx, kindDownloads, 1); n == 0 {
			return "error: " + note, nil
		}
		out, err := h(ctx, sessionID, args)
		if first, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(out)), "\n"); err != nil || strings.Contains(first, "error") || strings.Contains(first, "failed") {
			giveBack(ctx, kindDownloads, 1)
		}
		return out, err
	}
}
