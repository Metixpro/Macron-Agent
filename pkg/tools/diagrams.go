package tools

import (
	"context"
	"fmt"
	"html"
	"path"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

const ToolGenerateDiagram = "generate_diagram"

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
		format := strings.TrimPrefix(strings.ToLower(path.Ext(outFile)), ".")
		if format != "png" && format != "pdf" {
			format = "svg"
		}
		srcFile := strings.TrimSuffix(outFile, path.Ext(outFile)) + ".dot"
		if err := r.Machine.WriteFile(sessionID, srcFile, []byte(code)); err != nil {
			return "", err
		}
		if !r.Machine.Available("dot")["dot"] {
			return "", fmt.Errorf("Graphviz is not installed (apt install graphviz); the source is saved in %s", srcFile)
		}
		res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: []string{"dot", "-T" + format, "-o", outFile, srcFile}, Timeout: 30 * time.Second})
		if res.ExitCode != 0 {
			return "", fmt.Errorf("dot could not render it: %s", firstRunes(strings.TrimSpace(res.Stderr+" "+res.Error), 600))
		}
		return fmt.Sprintf("Rendered to %s (source %s); look at it with view_image before sending.", outFile, srcFile), nil

	case "mermaid":
		mmdFile := strings.TrimSuffix(outFile, path.Ext(outFile)) + ".mmd"
		if err := r.Machine.WriteFile(sessionID, mmdFile, []byte(code)); err != nil {
			return "", err
		}
		if r.Machine.Available("mmdc")["mmdc"] {
			res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: []string{"mmdc", "-i", mmdFile, "-o", outFile, "-b", "white"}, Timeout: 90 * time.Second})
			if res.ExitCode != 0 {
				return "", fmt.Errorf("mmdc could not render it: %s", firstRunes(strings.TrimSpace(res.Stderr+" "+res.Error), 600))
			}
			return fmt.Sprintf("Rendered to %s (source %s); look at it with view_image before sending.", outFile, mmdFile), nil
		}
		htmlFile := strings.TrimSuffix(outFile, path.Ext(outFile)) + ".html"
		page := `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Diagram</title>
<script src="https://cdn.jsdelivr.net/npm/mermaid@11.4.1/dist/mermaid.min.js"></script>
<script>mermaid.initialize({startOnLoad: true, theme: 'neutral'});</script></head>
<body style="display:flex;justify-content:center;padding:2rem"><pre class="mermaid">` + html.EscapeString(code) + `</pre></body></html>`
		if err := r.Machine.WriteFile(sessionID, htmlFile, []byte(page)); err != nil {
			return "", err
		}
		return fmt.Sprintf("Not rendered to an image: mermaid-cli is not installed (npm install -g @mermaid-js/mermaid-cli). Saved %s, and %s, a page that draws it in a browser.", mmdFile, htmlFile), nil
	}

	return "", fmt.Errorf("unknown diagram type: %s (use mermaid, graphviz, or svg)", diagramType)
}
