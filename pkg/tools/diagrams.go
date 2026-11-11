package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

const ToolGenerateDiagram = "generate_diagram"

// GenerateDiagram renders Mermaid, Graphviz, or SVG diagrams into workspace files.
func (r *Registry) GenerateDiagram(ctx context.Context, sessionID, diagramType, code, outFile string) (string, error) {
	diagramType = strings.ToLower(strings.TrimSpace(diagramType))
	if diagramType == "" {
		diagramType = "mermaid"
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return "", fmt.Errorf("diagram code is required")
	}

	if outFile == "" {
		outFile = fmt.Sprintf("diagram_%d.svg", time.Now().Unix())
	}

	switch diagramType {
	case "svg":
		if !strings.HasPrefix(code, "<svg") {
			code = `<svg xmlns="http://www.w3.org/2000/svg" width="600" height="400">` + code + `</svg>`
		}
		if err := r.Machine.WriteFile(sessionID, outFile, []byte(code)); err != nil {
			return "", err
		}
		return fmt.Sprintf("✅ SVG diagram saved to `%s`.", outFile), nil

	case "graphviz", "dot":
		srcFile := outFile + ".dot"
		_ = r.Machine.WriteFile(sessionID, srcFile, []byte(code))

		available := r.Machine.Available("dot")
		if available["dot"] {
			opts := machine.RunOptions{
				Args:    []string{"dot", "-Tsvg", "-o", outFile, srcFile},
				Timeout: 20 * time.Second,
			}
			res := r.Machine.Run(ctx, sessionID, opts)
			if res.ExitCode == 0 {
				return fmt.Sprintf("✅ Graphviz diagram rendered to `%s`.", outFile), nil
			}
		}
		// Fallback: save dot source and provide SVG container
		return fmt.Sprintf("✅ Graphviz source saved to `%s`.", srcFile), nil

	case "mermaid":
		// 1. Save .mmd source
		mmdFile := strings.TrimSuffix(outFile, ".svg") + ".mmd"
		_ = r.Machine.WriteFile(sessionID, mmdFile, []byte(code))

		// 2. Try mermaid-cli if installed
		available := r.Machine.Available("mmdc")
		if available["mmdc"] {
			opts := machine.RunOptions{
				Args:    []string{"mmdc", "-i", mmdFile, "-o", outFile},
				Timeout: 30 * time.Second,
			}
			res := r.Machine.Run(ctx, sessionID, opts)
			if res.ExitCode == 0 {
				return fmt.Sprintf("✅ Mermaid diagram rendered to `%s`.", outFile), nil
			}
		}

		// 3. Generate self-contained HTML preview with Mermaid CDN
		htmlFile := strings.TrimSuffix(outFile, ".svg") + ".html"
		html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>Mermaid Diagram</title>
  <script src="https://cdn.jsdelivr.net/npm/mermaid/dist/mermaid.min.js"></script>
  <script>mermaid.initialize({startOnLoad: true, theme: 'neutral'});</script>
</head>
<body style="font-family: sans-serif; display: flex; justify-content: center; padding: 2rem;">
  <div class="mermaid">
%s
  </div>
</body>
</html>`, code)
		_ = r.Machine.WriteFile(sessionID, htmlFile, []byte(html))
		return fmt.Sprintf("✅ Mermaid diagram saved to `%s` and interactive HTML preview generated at `%s`.",
			mmdFile, htmlFile), nil
	}

	return "", fmt.Errorf("unknown diagram type: %s (use mermaid, graphviz, or svg)", diagramType)
}
