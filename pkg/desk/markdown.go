package desk

import (
	"bytes"
	"html"
	"net/url"
	"path"
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

var (
	linkRe     = regexp.MustCompile(`<a href="`)
	imgTagRe   = regexp.MustCompile(`<img src="([^"]*)" alt="([^"]*)"(?: title="[^"]*")?\s*/?>`)
	fileLinkRe = regexp.MustCompile(`<a href="([^"]*)"`)
	schemeRe   = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]+:`)
	imageExt   = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true, ".avif": true, ".svg": true}
)

func fileRef(src string) (string, bool) {
	src = html.UnescapeString(src)
	if u, err := url.PathUnescape(src); err == nil {
		src = u
	}
	lower := strings.ToLower(src)
	switch {
	case src == "", strings.HasPrefix(src, "#"), strings.HasPrefix(src, "//"):
		return "", false
	case strings.HasPrefix(lower, "file://"):
		src = src[len("file://"):]
		if len(src) > 2 && src[0] == '/' && src[2] == ':' {
			src = src[1:]
		}
	case schemeRe.MatchString(src):
		return "", false
	}
	return src, true
}

func fileTags(out string) string {
	out = imgTagRe.ReplaceAllStringFunc(out, func(tag string) string {
		m := imgTagRe.FindStringSubmatch(tag)
		name, ok := fileRef(m[1])
		if !ok {
			return tag
		}
		attr := html.EscapeString(name)
		if imageExt[strings.ToLower(path.Ext(strings.ReplaceAll(name, "\\", "/")))] {
			return `<img data-file="` + attr + `" alt="` + m[2] + `">`
		}
		label := m[2]
		if strings.TrimSpace(label) == "" {
			label = html.EscapeString(path.Base(strings.ReplaceAll(name, "\\", "/")))
		}
		return `<a class="file-chip" data-file="` + attr + `">` + label + `</a>`
	})
	return fileLinkRe.ReplaceAllStringFunc(out, func(tag string) string {
		name, ok := fileRef(fileLinkRe.FindStringSubmatch(tag)[1])
		if !ok {
			return tag
		}
		return `<a class="file-link" data-file="` + html.EscapeString(name) + `"`
	})
}

var targetRe = regexp.MustCompile(`(!?\[[^\]\n]*\]\()([^)\n]*)(\))`)

func fixTargets(text string) string {
	if !strings.Contains(text, "](") {
		return text
	}
	lines := strings.Split(text, "\n")
	fence := false
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = !fence
			continue
		}
		if fence || !strings.Contains(line, "](") {
			continue
		}
		parts := strings.Split(line, "`")
		for j := 0; j < len(parts); j += 2 {
			parts[j] = fixLineTargets(parts[j])
		}
		lines[i] = strings.Join(parts, "`")
	}
	return strings.Join(lines, "\n")
}

func fixLineTargets(text string) string {
	return targetRe.ReplaceAllStringFunc(text, func(m string) string {
		g := targetRe.FindStringSubmatch(m)
		t := strings.TrimSpace(g[2])
		if strings.HasPrefix(t, "<") || strings.Contains(t, `"`) {
			return m
		}
		if strings.HasPrefix(strings.ToLower(t), "file://") {
			t = t[len("file://"):]
			if len(t) > 2 && t[0] == '/' && t[2] == ':' {
				t = t[1:]
			}
		} else if schemeRe.MatchString(t) {
			return m
		}
		if strings.ContainsAny(t, " \t") || t != strings.TrimSpace(g[2]) {
			return g[1] + "<" + t + ">" + g[3]
		}
		return g[1] + t + g[3]
	})
}

func renderMarkdown(text string) string {
	var buf bytes.Buffer
	if err := md.Convert([]byte(fixTargets(text)), &buf); err != nil {
		return "<p>" + html.EscapeString(text) + "</p>"
	}
	return linkRe.ReplaceAllString(fileTags(buf.String()), `<a target="_blank" rel="noopener noreferrer" href="`)
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
