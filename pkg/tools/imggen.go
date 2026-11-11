package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const ToolGenerateImage = "generate_image"

func (r *Registry) GenerateImage(ctx context.Context, sessionID, prompt, size, outFile string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	key := firstEnv("MACRON_IMAGE_API_KEY", "OPENAI_API_KEY")
	if key == "" {
		return "", fmt.Errorf("image generation is not set up: macron config set MACRON_IMAGE_API_KEY <key> (OpenAI, or any OpenAI-compatible images API with MACRON_IMAGE_API_URL and MACRON_IMAGE_MODEL)")
	}
	endpoint := firstEnv("MACRON_IMAGE_API_URL")
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1/images/generations"
	}
	model := firstEnv("MACRON_IMAGE_MODEL")
	if model == "" {
		model = "gpt-image-1"
	}
	if size == "" {
		size = "1024x1024"
	}
	if outFile = strings.TrimSpace(outFile); outFile == "" {
		outFile = fmt.Sprintf("image-%s.png", time.Now().Format("20060102-150405"))
	}
	body := map[string]any{"model": model, "prompt": prompt, "n": 1, "size": size}
	out, err := apiCall(ctx, r.cloudClient(), http.MethodPost, endpoint, map[string]string{"Authorization": "Bearer " + key}, body, key)
	if err != nil {
		return "", fmt.Errorf("the image API refused: %w", err)
	}
	var res struct {
		Data []struct {
			B64     string `json:"b64_json"`
			URL     string `json:"url"`
			Revised string `json:"revised_prompt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &res); err != nil || len(res.Data) == 0 {
		return "", fmt.Errorf("the image API sent no image: %.300s", out)
	}
	img := res.Data[0]
	var data []byte
	switch {
	case img.B64 != "":
		if data, err = base64.StdEncoding.DecodeString(img.B64); err != nil {
			return "", fmt.Errorf("the image API sent a broken image: %w", err)
		}
	case img.URL != "":
		dctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		req, err := http.NewRequestWithContext(dctx, http.MethodGet, img.URL, nil)
		if err != nil {
			return "", err
		}
		resp, err := r.cloudClient().Do(req)
		if err != nil {
			return "", fmt.Errorf("downloading the image: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("downloading the image: %s", resp.Status)
		}
		if data, err = io.ReadAll(io.LimitReader(resp.Body, 20<<20)); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("the image API sent no image")
	}
	if !strings.HasPrefix(http.DetectContentType(data), "image/") {
		return "", fmt.Errorf("the image API sent something that is not an image")
	}
	if err := r.Machine.WriteFile(sessionID, outFile, data); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Image made with %s (%s) and saved to %s; look at it with view_image before sending it.", model, size, outFile)
	if img.Revised != "" {
		msg += "\nThe service rewrote the prompt as: " + img.Revised
	}
	return msg, nil
}
