package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	dlParallelMin int64 = 8 << 20
	dlPartBytes   int64 = 2 << 20
)

const (
	dlMaxConns    = 8
	dlPartTries   = 4
	dlPartBackoff = 500 * time.Millisecond
)

var (
	errNoRanges    = errors.New("the server does not serve byte ranges")
	contentRangeRe = regexp.MustCompile(`^bytes (\d+)-(\d+)/(\d+)$`)
)

func newGet(ctx context.Context, rawURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	ua := browserUA
	if v, ok := ctx.Value(plainUAKey{}).(bool); ok && v {
		ua = plainUA
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "*/*")
	return req, nil
}

const plainUA = "Wget/1.21.4"

type plainUAKey struct{}

func asDownloadTool(ctx context.Context) context.Context {
	return context.WithValue(ctx, plainUAKey{}, true)
}

func contentRange(h string) (start, end, total int64, ok bool) {
	m := contentRangeRe.FindStringSubmatch(strings.TrimSpace(h))
	if m == nil {
		return 0, 0, 0, false
	}
	start, _ = strconv.ParseInt(m[1], 10, 64)
	end, _ = strconv.ParseInt(m[2], 10, 64)
	total, _ = strconv.ParseInt(m[3], 10, 64)
	return start, end, total, start <= end && end < total
}

func validator(resp *http.Response) string {
	if et := resp.Header.Get("ETag"); et != "" && !strings.HasPrefix(et, "W/") {
		return et
	}
	return resp.Header.Get("Last-Modified")
}

func (r *Registry) parallelGet(ctx context.Context, f *os.File, rawURL string, first *http.Response, total int64) (int, error) {
	conns := int(min(max(total/dlPartBytes, 1), dlMaxConns))
	size := (total + int64(conns) - 1) / int64(conns)
	if err := f.Truncate(total); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	ifRange := validator(first)
	errs := make([]error, conns)
	var wg sync.WaitGroup
	for i := range conns {
		start := int64(i) * size
		end := min(start+size, total) - 1
		var body io.ReadCloser
		if i == 0 {
			body = first.Body
		}
		wg.Go(func() {
			if errs[i] = r.getRange(ctx, f, rawURL, start, end, ifRange, body); errs[i] != nil {
				cancel(errs[i])
			}
		})
	}
	wg.Wait()
	if err := context.Cause(ctx); err != nil {
		return conns, err
	}
	return conns, nil
}

func (r *Registry) getRange(ctx context.Context, f *os.File, rawURL string, start, end int64, ifRange string, body io.ReadCloser) error {
	w := counted(ctx, io.NewOffsetWriter(f, start))
	want := end - start + 1
	var (
		got     int64
		lastErr error
	)
	for try := 0; got < want; try++ {
		if body == nil {
			if try > dlPartTries {
				return fmt.Errorf("bytes %d-%d: %w", start+got, end, lastErr)
			}
			if try > 0 {
				select {
				case <-ctx.Done():
					return context.Cause(ctx)
				case <-time.After(time.Duration(try) * dlPartBackoff):
				}
			}
			req, err := newGet(ctx, rawURL)
			if err != nil {
				return err
			}
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start+got, end))
			if ifRange != "" {
				req.Header.Set("If-Range", ifRange)
			}
			resp, err := r.HTTP.Do(req)
			if err != nil {
				if ctx.Err() != nil {
					return context.Cause(ctx)
				}
				lastErr = err
				continue
			}
			if s, _, _, ok := contentRange(resp.Header.Get("Content-Range")); resp.StatusCode != http.StatusPartialContent || !ok || s != start+got {
				resp.Body.Close()
				if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
					lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
					continue
				}
				return errNoRanges
			}
			body = resp.Body
		}
		n, err := io.Copy(w, io.LimitReader(body, want-got))
		body.Close()
		body = nil
		got += n
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if err == nil && n == 0 {
			err = io.ErrUnexpectedEOF
		}
		if err != nil {
			lastErr = err
		}
	}
	return nil
}
