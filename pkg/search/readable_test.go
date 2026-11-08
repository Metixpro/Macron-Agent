package search

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// A page is read without its frame: the article, its headings, table rows
// and code as written; not the menu, the language list, the footer, the
// share buttons or the citation marks.
func TestReadableTextKeepsTheContent(t *testing.T) {
	para := strings.Repeat("The tower is the tallest in the country and one of the city's symbols. ", 6)
	page := `<html><body>
<header class="site-header"><a href="/">Home</a> <a href="/news">News</a></header>
<div id="mw-panel"><ul><li><a>English</a></li><li><a>Deutsch</a></li><li><a>Français</a></li></ul></div>
<nav><a>Login</a></nav>
<main>
 <h1>Milad Tower</h1>
 <p>` + para + `<sup class="reference">[1]</sup></p>
 <h2>Facts</h2>
 <table><tr><th>Height</th><td>435 m</td></tr><tr><td>Opened</td><td>2008</td></tr></table>
 <pre>for i := range 10 {
    fmt.Println(i)
}</pre>
 <ul><li><a href="/x"></a></li><li>Observation deck</li></ul>
 <div class="share-buttons">Share on Telegram</div>
 <span class="mw-editsection">[edit]</span>
</main>
<aside class="sidebar">Most read: something else entirely</aside>
<footer>© 2026 Example — all rights reserved</footer>
</body></html>`
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatal(err)
	}
	got := readableText(doc)
	for _, want := range []string{"# Milad Tower", "## Facts", "Height | 435 m", "Opened | 2008", "for i := range 10 {\n    fmt.Println(i)\n}", "• Observation deck", "tallest in the country"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"Home", "Deutsch", "Login", "Share on", "[1]", "[edit]", "Most read", "rights reserved"} {
		if strings.Contains(got, bad) {
			t.Errorf("the page's frame %q is left in:\n%s", bad, got)
		}
	}
	if strings.Contains(got, "•\n") || strings.HasSuffix(got, "•") {
		t.Errorf("an empty list item:\n%s", got)
	}

	// A page whose <main> holds little of its text (a table of prices
	// outside it) is read whole, without the frame.
	page2 := `<html><body><nav>Menu</nav><main><p>Prices</p></main><table>` + strings.Repeat(`<tr><td>Dollar</td><td>269,500</td></tr>`, 40) + `</table></body></html>`
	doc2, _ := html.Parse(strings.NewReader(page2))
	if got := readableText(doc2); !strings.Contains(got, "Dollar | 269,500") || !strings.Contains(got, "Prices") || strings.Contains(got, "Menu") {
		t.Errorf("a page read whole:\n%s", got)
	}

	// Documents (HTMLText) keep everything.
	if _, text := HTMLText(`<html><body><nav>Chapter list</nav><p>Body</p></body></html>`); !strings.Contains(text, "Body") {
		t.Errorf("document text: %q", text)
	}
}
