package desk

import (
	"bytes"
	"html"
	"regexp"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

type codeRenderer struct{}

func (codeRenderer) RegisterFuncs(r renderer.NodeRendererFuncRegisterer) {
	r.Register(ast.KindFencedCodeBlock, renderCode)
	r.Register(ast.KindCodeBlock, renderCode)
}

var codeFormatter = chromahtml.New(chromahtml.WithClasses(true), chromahtml.PreventSurroundingPre(true))

func renderCode(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	lang := ""
	if f, ok := node.(*ast.FencedCodeBlock); ok {
		lang = strings.ToLower(string(f.Language(source)))
	}
	var code strings.Builder
	lines := node.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		code.Write(seg.Value(source))
	}
	text := code.String()
	lexer := lexers.Get(lang)
	if lexer == nil {
		lexer = lexers.Analyse(text)
	}
	label := lang
	if label == "" {
		label = "text"
	}
	_, _ = w.WriteString(`<div class="code"><div class="code-head"><span>` + html.EscapeString(label) + `</span><button class="copy" type="button">Copy</button></div><pre class="chroma"><code>`)
	if lexer != nil {
		lexer = chroma.Coalesce(lexer)
		if it, err := lexer.Tokenise(nil, text); err == nil && codeFormatter.Format(w, styles.Fallback, it) == nil {
			_, _ = w.WriteString(`</code></pre></div>`)
			return ast.WalkSkipChildren, nil
		}
	}
	_, _ = w.WriteString(html.EscapeString(text) + `</code></pre></div>`)
	return ast.WalkSkipChildren, nil
}

var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(renderer.WithNodeRenderers(util.Prioritized(codeRenderer{}, 100))),
)

var linkRe = regexp.MustCompile(`<a href="`)

func renderMarkdown(text string) string {
	var buf bytes.Buffer
	if err := md.Convert([]byte(text), &buf); err != nil {
		return "<p>" + html.EscapeString(text) + "</p>"
	}
	return linkRe.ReplaceAllString(buf.String(), `<a target="_blank" rel="noopener noreferrer" href="`)
}

var (
	codeCSSOnce sync.Once
	codeCSS     []byte
)

func chromaCSS() []byte {
	codeCSSOnce.Do(func() {
		var buf bytes.Buffer
		for _, theme := range []struct{ scope, style string }{{"[data-theme=dark]", "github-dark"}, {"[data-theme=light]", "github"}} {
			var part bytes.Buffer
			_ = chromahtml.New(chromahtml.WithClasses(true)).WriteCSS(&part, styles.Get(theme.style))
			for _, line := range strings.Split(part.String(), "\n") {
				if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "/*") && !strings.Contains(line, "{") {
					continue
				}
				if i := strings.Index(line, "*/ "); i >= 0 && strings.HasPrefix(strings.TrimSpace(line), "/*") {
					line = line[i+3:]
				}
				buf.WriteString(theme.scope + " " + strings.TrimSpace(line) + "\n")
			}
		}
		codeCSS = buf.Bytes()
	})
	return codeCSS
}
