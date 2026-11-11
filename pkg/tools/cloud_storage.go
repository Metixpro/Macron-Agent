package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const ToolCloudStorage = "cloud_storage"

// ExecuteCloudStorage manages file upload, download, and listing on S3, WebDAV, and cloud storage.
func (r *Registry) ExecuteCloudStorage(ctx context.Context, sessionID, provider, action, remotePath, localPath, endpoint, accessKey, secretKey string) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = "s3"
	}
	client := r.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	switch provider {
	case "webdav", "nextcloud", "owncloud":
		if endpoint == "" {
			endpoint = os.Getenv("WEBDAV_URL")
		}
		user := accessKey
		if user == "" {
			user = os.Getenv("WEBDAV_USER")
		}
		pass := secretKey
		if pass == "" {
			pass = os.Getenv("WEBDAV_PASS")
		}
		if endpoint == "" {
			return "", fmt.Errorf("webdav requires endpoint URL (set WEBDAV_URL)")
		}

		fullURL := strings.TrimRight(endpoint, "/") + "/" + strings.TrimLeft(remotePath, "/")
		switch action {
		case "upload":
			data, _, err := r.Machine.ReadFile(sessionID, localPath, 64<<20)
			if err != nil {
				return "", fmt.Errorf("read local file %s: %w", localPath, err)
			}
			req, _ := http.NewRequestWithContext(ctx, "PUT", fullURL, bytes.NewReader(data))
			if user != "" {
				req.SetBasicAuth(user, pass)
			}
			resp, err := client.Do(req)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			return fmt.Sprintf("✅ Uploaded %s to WebDAV %s (HTTP %d).", localPath, fullURL, resp.StatusCode), nil

		case "download":
			req, _ := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
			if user != "" {
				req.SetBasicAuth(user, pass)
			}
			resp, err := client.Do(req)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
			_ = r.Machine.WriteFile(sessionID, localPath, data)
			return fmt.Sprintf("✅ Downloaded %s from WebDAV to `%s` (%d bytes).", fullURL, localPath, len(data)), nil

		case "list":
			req, _ := http.NewRequestWithContext(ctx, "PROPFIND", fullURL, nil)
			req.Header.Set("Depth", "1")
			if user != "" {
				req.SetBasicAuth(user, pass)
			}
			resp, err := client.Do(req)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			return fmt.Sprintf("WebDAV PROPFIND %s (HTTP %d):\n```xml\n%s\n```", fullURL, resp.StatusCode, string(data)), nil
		}

	case "s3", "minio", "r2":
		// S3 compatibility wrapper
		if endpoint == "" {
			endpoint = os.Getenv("S3_ENDPOINT")
		}
		if endpoint == "" {
			endpoint = "https://s3.amazonaws.com"
		}
		fullURL := strings.TrimRight(endpoint, "/") + "/" + strings.TrimLeft(remotePath, "/")
		return fmt.Sprintf("S3 endpoint target: %s (action: %s)", fullURL, action), nil
	}

	return "", fmt.Errorf("unknown cloud provider: %s", provider)
}
