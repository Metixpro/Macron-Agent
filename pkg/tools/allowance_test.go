package tools

import (
	"context"
	"strings"
	"testing"
)

func TestAllowance(t *testing.T) {
	ctx := WithAllowance(context.Background(), &Allowance{Images: 4, Downloads: 2})
	if n, note := take(ctx, kindImages, 3); n != 3 || note != "" {
		t.Fatalf("within the limit: %d %q", n, note)
	}
	n, note := take(ctx, kindImages, 6)
	if n != 1 || !strings.Contains(note, "at most 4 pictures") || !strings.Contains(note, "limit for one request") {
		t.Fatalf("over the limit: %d %q", n, note)
	}
	if n, note := take(ctx, kindImages, 1); n != 0 || note == "" {
		t.Fatalf("spent: %d %q", n, note)
	}
	giveBack(ctx, kindImages, 2) // a search found fewer
	if n, _ := take(ctx, kindImages, 5); n != 2 {
		t.Errorf("given back: %d", n)
	}
	if n, _ := take(context.Background(), kindImages, 50); n != 50 {
		t.Error("no allowance: no limit")
	}
	if n, _ := take(WithAllowance(context.Background(), &Allowance{}), kindDownloads, 9); n != 9 {
		t.Error("a zero limit is no limit")
	}

	// A tool that makes one picture: refused once spent, given back when it made none.
	calls := 0
	made := oneImage(func(context.Context, string, map[string]any) (string, error) {
		calls++
		return "Image created and queued for delivery.\n- File: a.png", nil
	})
	failed := oneImage(func(context.Context, string, map[string]any) (string, error) {
		return "image generation failed: down", nil
	})
	ctx = WithAllowance(context.Background(), &Allowance{Images: 2})
	_, _ = failed(ctx, "s", nil)
	_, _ = made(ctx, "s", nil)
	_, _ = made(ctx, "s", nil)
	if out, _ := made(ctx, "s", nil); calls != 2 || !strings.HasPrefix(out, "error: Limit") {
		t.Errorf("the third picture is refused before any work: %d calls, %q", calls, out)
	}

	for in, want := range map[[2]any]string{{"1-5,8", 3}: "1-3", {"2,4,6", 2}: "2,4", {"1-3,7-9", 5}: "1-3,7-8", {"5", 1}: "5"} {
		if got := firstItems(in[0].(string), in[1].(int)); got != want {
			t.Errorf("firstItems(%v) = %q, want %q", in, got, want)
		}
	}
}
