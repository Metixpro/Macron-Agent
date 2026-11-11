package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const ToolOpenAPICall = "openapi_call"

type OpenAPISpec struct {
	OpenAPI string `json:"openapi"`
	Swagger string `json:"swagger"`
	Servers []struct {
		URL string `json:"url"`
	} `json:"servers"`
	Host     string `json:"host"`
	BasePath string `json:"basePath"`
	Paths    map[string]map[string]struct {
		OperationID string `json:"operationId"`
		Summary     string `json:"summary"`
	} `json:"paths"`
}

func (r *Registry) CallOpenAPIEndpoint(ctx context.Context, sessionID, specFile, opID, endpoint, method, baseURL, authEnv string, params, body map[string]any) (string, error) {
	specBytes, _, err := r.Machine.ReadFile(sessionID, specFile, 1<<20)
	if err != nil {
		return "", fmt.Errorf("read openapi spec %s: %w", specFile, err)
	}

	var spec OpenAPISpec
	if err := json.Unmarshal(specBytes, &spec); err != nil {
		return "", fmt.Errorf("parse openapi spec JSON: %w", err)
	}

	if baseURL == "" {
		if len(spec.Servers) > 0 && spec.Servers[0].URL != "" {
			baseURL = strings.TrimRight(spec.Servers[0].URL, "/")
		} else if spec.Host != "" {
			scheme := "https"
			baseURL = fmt.Sprintf("%s://%s%s", scheme, spec.Host, spec.BasePath)
			baseURL = strings.TrimRight(baseURL, "/")
		}
	}

	targetPath := endpoint
	targetMethod := strings.ToUpper(method)

	if opID != "" {
		for p, methods := range spec.Paths {
			for m, op := range methods {
				if op.OperationID == opID {
					targetPath = p
					targetMethod = strings.ToUpper(m)
					break
				}
			}
		}
	}

	if targetPath == "" {
		return "", fmt.Errorf("could not resolve endpoint from spec")
	}
	if targetMethod == "" {
		targetMethod = "GET"
	}

	finalPath := targetPath
	queryParams := url.Values{}
	for k, v := range params {
		placeholder := "{" + k + "}"
		if strings.Contains(finalPath, placeholder) {
			finalPath = strings.ReplaceAll(finalPath, placeholder, url.PathEscape(fmt.Sprint(v)))
		} else {
			queryParams.Set(k, fmt.Sprint(v))
		}
	}

	fullURL := baseURL + finalPath
	if len(queryParams) > 0 {
		if strings.Contains(fullURL, "?") {
			fullURL += "&" + queryParams.Encode()
		} else {
			fullURL += "?" + queryParams.Encode()
		}
	}

	if baseURL == "" || !strings.HasPrefix(fullURL, "http") {
		return "", fmt.Errorf("no server URL: the spec names none, so pass base_url")
	}
	var reqBody io.Reader
	var bData []byte
	if body != nil && (targetMethod == "POST" || targetMethod == "PUT" || targetMethod == "PATCH") {
		bData, _ = json.Marshal(body)
		reqBody = bytes.NewReader(bData)
	}
	if targetMethod != "GET" && targetMethod != "HEAD" {
		host := fullURL
		if u, err := url.Parse(fullURL); err == nil {
			host = u.Host
		}
		if refused := r.gateSend(ctx, sessionID, targetMethod+" "+host, fullURL+"\n"+string(bData)); refused != "" {
			return refused, nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, targetMethod, fullURL, reqBody)
	if err != nil {
		return "", fmt.Errorf("create http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Macron-Agent (OpenAPI)")
	if authEnv = strings.TrimSpace(authEnv); authEnv != "" {
		token := os.Getenv(authEnv)
		if token == "" {
			return "", fmt.Errorf("%s is not set: put the API token in config.env as %s=...", authEnv, authEnv)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := r.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := client.Do(req.WithContext(sctx))
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("HTTP %s %s → %s\n%s", targetMethod, fullURL, resp.Status, string(respBytes)), nil
}
