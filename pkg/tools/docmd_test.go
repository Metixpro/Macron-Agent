package tools

import (
	"strings"
	"testing"
	"time"
)

func TestDocHTML(t *testing.T) {
	md := "# عنوان\nمتن **پررنگ** و *کج* و ~~خط~~ و `code` و [لینک](https://example.com).\n\n" +
		"| ماه | فروش |\n|---|---:|\n| مهر | ۱۲۰ |\n| آبان |\n\n" +
		"- یک\n  - nested\n- [x] done\n1. first\n2. second\n\n> نقل\n\n```go\nfmt.Println(\"<b>\")\n```\n\n---\n![نمودار](chart.png) <script>alert(1)</script>"
	got := docHTML(md)
	for _, want := range []string{
		`<h1 dir="rtl">عنوان</h1>`, "<strong>پررنگ</strong>", "<em>کج</em>", "<del>خط</del>", "<code>code</code>",
		`<a href="https://example.com">لینک</a>`, `<table dir="rtl">`, `<th style="text-align:right">فروش</th>`, "<td>آبان</td><td style=\"text-align:right\"></td>",
		`<ul dir="rtl"><li>یک<ul dir="ltr"><li>nested</li></ul></li><li>☑ done</li></ul><ol dir="ltr"><li>first</li><li>second</li></ol>`,
		`<blockquote dir="rtl"><p dir="rtl">نقل</p>`, `<pre dir="ltr"><code class="language-go">fmt.Println(&#34;&lt;b&gt;&#34;)</code></pre>`, "<hr>",
		`<img src="https://waze.local/chart.png" alt="نمودار">`, "&lt;script&gt;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<script>") {
		t.Error("text never becomes markup")
	}
}

func TestJalali(t *testing.T) {
	for in, want := range map[string]string{"2026-10-25": "۳ آبان ۱۴۰۵", "2025-03-21": "۱ فروردین ۱۴۰۴", "2024-03-19": "۲۹ اسفند ۱۴۰۲", "2000-01-01": "۱۱ دی ۱۳۷۸"} {
		day, _ := time.Parse("2006-01-02", in)
		if got := persianDate(day); got != want {
			t.Errorf("%s → %s, want %s", in, got, want)
		}
	}
}
