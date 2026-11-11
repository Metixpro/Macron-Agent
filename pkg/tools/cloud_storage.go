package tools

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

const (
	ToolCloudStorage = "cloud_storage"
	cloudMaxBytes    = 256 << 20
	cloudListMax     = 200
)

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func httpProblem(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 600))
	return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
}

func (r *Registry) cloudClient() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return http.DefaultClient
}

func (r *Registry) ExecuteCloudStorage(ctx context.Context, sessionID, provider, action, remotePath, localPath, endpoint, _, _ string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "webdav", "nextcloud", "owncloud":
		return r.webdav(ctx, sessionID, action, remotePath, localPath, endpoint)
	case "s3", "minio", "r2", "":
		return r.s3(ctx, sessionID, action, remotePath, localPath, endpoint)
	}
	return "", fmt.Errorf("unknown provider %q: s3 (also MinIO and R2) or webdav", provider)
}

func (r *Registry) readUpload(sessionID, localPath string) ([]byte, error) {
	if strings.TrimSpace(localPath) == "" {
		return nil, fmt.Errorf("local_path is required for upload")
	}
	data, _, err := r.Machine.ReadFile(sessionID, localPath, cloudMaxBytes)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", localPath, err)
	}
	return data, nil
}

func downloadName(localPath, remotePath string) string {
	if strings.TrimSpace(localPath) != "" {
		return localPath
	}
	if b := path.Base(strings.TrimRight(remotePath, "/")); b != "." && b != "/" && b != "" {
		return b
	}
	return "download"
}

func (r *Registry) webdav(ctx context.Context, sessionID, action, remotePath, localPath, endpoint string) (string, error) {
	if endpoint == "" {
		endpoint = firstEnv("WEBDAV_URL")
	}
	if endpoint == "" {
		return "", fmt.Errorf("WebDAV is not set up: macron config set WEBDAV_URL https://cloud.example.com/remote.php/dav/files/USER (and WEBDAV_USER, WEBDAV_PASS)")
	}
	user, pass := firstEnv("WEBDAV_USER"), firstEnv("WEBDAV_PASS")
	full := strings.TrimRight(endpoint, "/") + "/" + strings.TrimLeft(remotePath, "/")
	do := func(method string, body io.Reader, depth string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, full, body)
		if err != nil {
			return nil, err
		}
		if user != "" {
			req.SetBasicAuth(user, pass)
		}
		if depth != "" {
			req.Header.Set("Depth", depth)
		}
		return r.cloudClient().Do(req)
	}
	switch action {
	case "upload":
		data, err := r.readUpload(sessionID, localPath)
		if err != nil {
			return "", err
		}
		resp, err := do(http.MethodPut, bytes.NewReader(data), "")
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return "", fmt.Errorf("upload refused: %w", httpProblem(resp))
		}
		return fmt.Sprintf("Uploaded %s (%s) to %s (%s).", localPath, humanSize(int64(len(data))), full, resp.Status), nil
	case "download":
		resp, err := do(http.MethodGet, nil, "")
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("download failed: %w", httpProblem(resp))
		}
		return r.saveDownload(sessionID, downloadName(localPath, remotePath), resp.Body, full)
	case "list", "":
		resp, err := do("PROPFIND", nil, "1")
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusMultiStatus && resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("listing failed: %w", httpProblem(resp))
		}
		var ms struct {
			Responses []struct {
				Href  string `xml:"href"`
				Props []struct {
					Size     int64     `xml:"prop>getcontentlength"`
					Modified string    `xml:"prop>getlastmodified"`
					Folder   *xml.Name `xml:"prop>resourcetype>collection"`
				} `xml:"propstat"`
			} `xml:"response"`
		}
		if err := xml.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&ms); err != nil {
			return "", fmt.Errorf("unreadable listing: %w", err)
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "%s:", full)
		for i, it := range ms.Responses {
			if i == cloudListMax {
				fmt.Fprintf(&sb, "\n… and %d more", len(ms.Responses)-cloudListMax)
				break
			}
			name, _ := url.PathUnescape(it.Href)
			line := "\n" + name
			if len(it.Props) > 0 {
				p := it.Props[0]
				if p.Folder != nil {
					line += " (folder)"
				} else {
					line += fmt.Sprintf(" (%s, %s)", humanSize(p.Size), p.Modified)
				}
			}
			sb.WriteString(line)
		}
		return sb.String(), nil
	}
	return "", fmt.Errorf("unknown action %q: upload, download or list", action)
}

func (r *Registry) saveDownload(sessionID, name string, body io.Reader, from string) (string, error) {
	data, err := io.ReadAll(io.LimitReader(body, cloudMaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > cloudMaxBytes {
		return "", fmt.Errorf("%s is larger than %s", from, humanSize(cloudMaxBytes))
	}
	if err := r.Machine.WriteFile(sessionID, name, data); err != nil {
		return "", err
	}
	return fmt.Sprintf("Downloaded %s to %s (%s).", from, name, humanSize(int64(len(data)))), nil
}

type s3Account struct {
	endpoint, region, key, secret string
}

func s3FromEnv(endpoint string) (s3Account, error) {
	a := s3Account{endpoint: endpoint, region: firstEnv("S3_REGION", "AWS_REGION", "AWS_DEFAULT_REGION"),
		key: firstEnv("S3_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID"), secret: firstEnv("S3_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY")}
	if a.region == "" {
		a.region = "us-east-1"
	}
	if a.endpoint == "" {
		a.endpoint = firstEnv("S3_ENDPOINT")
	}
	if a.endpoint == "" {
		a.endpoint = "https://s3." + a.region + ".amazonaws.com"
	}
	if a.key == "" || a.secret == "" {
		return a, fmt.Errorf("S3 is not set up: macron config set S3_ACCESS_KEY_ID …, S3_SECRET_ACCESS_KEY …, and for MinIO or R2 S3_ENDPOINT (R2: S3_REGION auto)")
	}
	a.endpoint = strings.TrimRight(a.endpoint, "/")
	return a, nil
}

func awsEscape(s string, keepSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && keepSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func signS3(req *http.Request, a s3Account, payloadHash string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	day := amzDate[:8]
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	host := req.URL.Host
	names := []string{"host"}
	values := map[string]string{"host": host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if lk == "x-amz-date" || lk == "x-amz-content-sha256" || lk == "range" || lk == "content-type" || strings.HasPrefix(lk, "x-amz-") {
			names = append(names, lk)
			values[lk] = strings.TrimSpace(strings.Join(v, ","))
		}
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, n := range names {
		canonHeaders.WriteString(n + ":" + values[n] + "\n")
	}
	signed := strings.Join(names, ";")
	q := req.URL.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var query []string
	for _, k := range keys {
		vs := q[k]
		sort.Strings(vs)
		for _, v := range vs {
			query = append(query, awsEscape(k, false)+"="+awsEscape(v, false))
		}
	}
	uri := req.URL.EscapedPath()
	if p, err := url.PathUnescape(uri); err == nil {
		uri = awsEscape(p, true)
	}
	if uri == "" {
		uri = "/"
	}
	canonical := strings.Join([]string{req.Method, uri, strings.Join(query, "&"), canonHeaders.String(), signed, payloadHash}, "\n")
	sum := sha256.Sum256([]byte(canonical))
	scope := day + "/" + a.region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	k := hmacSHA256([]byte("AWS4"+a.secret), day)
	k = hmacSHA256(k, a.region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+a.key+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func (r *Registry) s3(ctx context.Context, sessionID, action, remotePath, localPath, endpoint string) (string, error) {
	a, err := s3FromEnv(endpoint)
	if err != nil {
		return "", err
	}
	remote := strings.TrimLeft(strings.TrimSpace(remotePath), "/")
	if b := firstEnv("S3_BUCKET"); b != "" && !strings.HasPrefix(remote, b+"/") && remote != b {
		remote = b + "/" + remote
	}
	bucket, key, _ := strings.Cut(remote, "/")
	if bucket == "" {
		return "", fmt.Errorf("remote_path must start with the bucket: BUCKET/path/to/file (or set S3_BUCKET)")
	}
	send := func(method, rawURL string, body []byte) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		req.ContentLength = int64(len(body))
		signS3(req, a, hex.EncodeToString(sum[:]), time.Now())
		return r.cloudClient().Do(req)
	}
	object := a.endpoint + "/" + awsEscape(bucket, false) + "/" + awsEscape(key, true)
	switch action {
	case "upload":
		if key == "" {
			return "", fmt.Errorf("remote_path needs the object key after the bucket: %s/name", bucket)
		}
		data, err := r.readUpload(sessionID, localPath)
		if err != nil {
			return "", err
		}
		resp, err := send(http.MethodPut, object, data)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return "", fmt.Errorf("upload refused: %w", httpProblem(resp))
		}
		return fmt.Sprintf("Uploaded %s (%s) to s3://%s/%s.", localPath, humanSize(int64(len(data))), bucket, key), nil
	case "download":
		if key == "" {
			return "", fmt.Errorf("remote_path needs the object key after the bucket")
		}
		resp, err := send(http.MethodGet, object, nil)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("download failed: %w", httpProblem(resp))
		}
		return r.saveDownload(sessionID, downloadName(localPath, key), resp.Body, "s3://"+bucket+"/"+key)
	case "list", "":
		u := a.endpoint + "/" + awsEscape(bucket, false) + "?list-type=2&max-keys=" + fmt.Sprint(cloudListMax)
		if key != "" {
			u += "&prefix=" + awsEscape(key, false)
		}
		resp, err := send(http.MethodGet, u, nil)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("listing failed: %w", httpProblem(resp))
		}
		var lr struct {
			Truncated bool `xml:"IsTruncated"`
			Contents  []struct {
				Key      string `xml:"Key"`
				Size     int64  `xml:"Size"`
				Modified string `xml:"LastModified"`
			} `xml:"Contents"`
		}
		if err := xml.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&lr); err != nil {
			return "", fmt.Errorf("unreadable listing: %w", err)
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "s3://%s/%s: %d objects", bucket, key, len(lr.Contents))
		for _, c := range lr.Contents {
			fmt.Fprintf(&sb, "\n%s (%s, %s)", c.Key, humanSize(c.Size), c.Modified)
		}
		if lr.Truncated {
			sb.WriteString("\n… more objects: narrow the prefix")
		}
		return sb.String(), nil
	}
	return "", fmt.Errorf("unknown action %q: upload, download or list", action)
}
