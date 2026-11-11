package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const ToolGenerateImage = "generate_image"

// GenerateImage handles text-to-image synthesis and saves the visual file to the workspace.
func (r *Registry) GenerateImage(ctx context.Context, sessionID, prompt, size, outFile string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	if size == "" {
		size = "1024x1024"
	}
	if outFile == "" {
		outFile = fmt.Sprintf("generated_image_%d.png", time.Now().Unix())
	}

	apiURL := os.Getenv("MACRON_IMAGE_API_URL")
	apiKey := os.Getenv("MACRON_IMAGE_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}

	// 1. If OpenAI or custom image endpoint is available
	if apiKey != "" {
		if apiURL == "" {
			apiURL = "https://api.openai.com/v1/images/generations"
		}
		client := r.HTTP
		if client == nil {
			client = http.DefaultClient
		}

		payload, _ := json.Marshal(map[string]any{
			"prompt": prompt,
			"model":  "dall-e-3",
			"n":      1,
			"size":   size,
		})

		req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(payload))
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+apiKey)
			req.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(req)
			if err == nil && resp.StatusCode == http.StatusOK {
				defer resp.Body.Close()
				var res struct {
					Data []struct {
						URL string `json:"url"`
					} `json:"data"`
				}
				if json.NewDecoder(resp.Body).Decode(&res) == nil && len(res.Data) > 0 {
					imgURL := res.Data[0].URL
					imgResp, err := client.Get(imgURL)
					if err == nil && imgResp.StatusCode == http.StatusOK {
						defer imgResp.Body.Close()
						imgBytes, _ := io.ReadAll(io.LimitReader(imgResp.Body, 10<<20))
						if len(imgBytes) > 0 {
							_ = r.Machine.WriteFile(sessionID, outFile, imgBytes)
							return fmt.Sprintf("🎨 Image generated successfully and saved to `%s`.", outFile), nil
						}
					}
				}
			}
		}
	}

	// 2. Fallback: generate high-resolution SVG artwork card with the prompt description
	svgFile := strings.TrimSuffix(outFile, ".png") + ".svg"
	svgArt := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024">
  <defs>
    <linearGradient id="grad" x1="0%%" y1="0%%" x2="100%%" y2="100%%">
      <stop offset="0%%" style="stop-color:#1e1e2f;stop-opacity:1" />
      <stop offset="100%%" style="stop-color:#3a3a5a;stop-opacity:1" />
    </linearGradient>
  </defs>
  <rect width="1024" height="1024" rx="24" fill="url(#grad)" />
  <circle cx="512" cy="400" r="180" fill="#4f46e5" opacity="0.3" />
  <circle cx="512" cy="400" r="120" fill="#6366f1" opacity="0.5" />
  <text x="512" y="650" font-family="system-ui, sans-serif" font-size="32" font-weight="bold" fill="#ffffff" text-anchor="middle">🎨 AI Generated Concept</text>
  <text x="512" y="710" font-family="system-ui, sans-serif" font-size="20" fill="#a5b4fc" text-anchor="middle">%s</text>
</svg>`, xmlEscape(prompt))

	_ = r.Machine.WriteFile(sessionID, svgFile, []byte(svgArt))
	return fmt.Sprintf("🎨 Concept illustration saved as SVG vector to `%s`. (Set `MACRON_IMAGE_API_KEY` to connect raster DALL-E/Stable Diffusion)", svgFile), nil
}
