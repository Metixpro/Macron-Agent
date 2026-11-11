package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const ToolOpenAPICall = "openapi_call"

type OpenAPISpec struct {
	OpenAPI string                         `json:"openapi"`
	Swagger string                         `json:"swagger"`
	Servers []struct {
		URL string `json:"url"`
	} `json:"servers"`
	Host     string                                           `json:"host"`
	BasePath string                                           `json:"basePath"`
	Paths    map[string]map[string]struct {
		OperationID string `json:"operationId"`
		Summary     string `json:"summary"`
	} `json:"paths"`
}

// CallOpenAPIEndpoint executes an API call defined in an OpenAPI specification.
func (r *Registry) CallOpenAPIEndpoint(ctx context.Context, sessionID, specFile, opID, endpoint, method, baseURL string, params, body map[string]any) (string, error) {
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

	// If operationId provided, resolve endpoint and method from spec
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

	// Substitute path parameters like {id}
	finalPath := targetPath
	queryParams := url.Values{}
	for k, v := range params {
		placeholder := "{" + k + "}"
		if strings.Contains(finalPath, placeholder) {
			finalPath = strings.ReplaceAll(finalPath, placeholder, fmt.Sprint(v))
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

	var reqBody io.Reader
	if body != nil && (targetMethod == "POST" || targetMethod == "PUT" || targetMethod == "PATCH") {
		bData, _ := json.Marshal(body)
		reqBody = bytes.NewReader(bData)
	}

	req, err := http.NewRequestWithContext(ctx, targetMethod, fullURL, reqBody)
	if err != nil {
		return "", fmt.Errorf("create http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Macron-Agent/1.5 (OpenAPI)")

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

	return fmt.Sprintf("HTTP %s %s -> Status %d (%s):\n```json\n%s\n```",
		targetMethod, fullURL, resp.StatusCode, resp.Status, string(respBytes)), nil
}
