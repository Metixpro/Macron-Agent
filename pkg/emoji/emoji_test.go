package emoji

import (
	"strings"
	"testing"
	"unicode/utf16"
)

var set = New(map[string]string{"😀": "111", "🔥": "222", "❤️": "333", "👨‍💻": "444", "👍": "555", "1️⃣": "666"})

func TestHTML(t *testing.T) {
	cases := map[string]string{
		"سلام 😀 دنیا":                         `سلام <tg-emoji emoji-id="111">😀</tg-emoji> دنیا`,
		"<b>🔥داغ</b>":                         `<b><tg-emoji emoji-id="222">🔥</tg-emoji>داغ</b>`,
		"عاشقتم ❤️ و ❤":                       `عاشقتم <tg-emoji emoji-id="333">❤️</tg-emoji> و <tg-emoji emoji-id="333">❤</tg-emoji>`,
		"کد: <code>😀</code> <pre>🔥</pre>":     "کد: <code>😀</code> <pre>🔥</pre>",
		"👨‍💻 برنامه‌نویس":                     `<tg-emoji emoji-id="444">👨‍💻</tg-emoji> برنامه‌نویس`,
		"👍🏽 skin tone stays":                  "👍🏽 skin tone stays",
		`<a href="https://x/😀">😀</a>`:         `<a href="https://x/😀"><tg-emoji emoji-id="111">😀</tg-emoji></a>`,
		`<tg-emoji emoji-id="9">😀</tg-emoji>`: `<tg-emoji emoji-id="9">😀</tg-emoji>`,
		"شماره 1️⃣ و 1 عادی":                  `شماره <tg-emoji emoji-id="666">1️⃣</tg-emoji> و 1 عادی`,
		"no emoji & <i>text</i>":              "no emoji & <i>text</i>",
	}
	for in, want := range cases {
		if got := set.HTML(in); got != want {
			t.Errorf("HTML(%q)\n got  %q\n want %q", in, got, want)
		}
	}
	var empty *Set
	if empty.HTML("😀") != "😀" || New(nil).Markdown("😀") != "😀" {
		t.Error("empty sets must be no-ops")
	}
}

func TestMarkdown(t *testing.T) {
	in := "سلام 😀\n```go\n// 🔥 in code\n```\nو `🔥` و [لینک 🔥](https://a/😀) و ![😀](tg://emoji?id=9) پایان 🔥"
	want := "سلام ![😀](tg://emoji?id=111)\n```go\n// 🔥 in code\n```\nو `🔥` و [لینک ![🔥](tg://emoji?id=222)](https://a/😀) و ![😀](tg://emoji?id=9) پایان ![🔥](tg://emoji?id=222)"
	if got := set.Markdown(in); got != want {
		t.Errorf("Markdown\n got  %q\n want %q", got, want)
	}
}

func TestButton(t *testing.T) {
	cases := []struct{ in, label, id string }{
		{"🔥 آمار", "آمار", "222"},
		{"تنظیمات ❤️", "تنظیمات", "333"},
		{"😀", "\u200c", "111"}, // emoji-only: the icon alone, not the emoji twice
		{"📊 بدون نگاشت", "📊 بدون نگاشت", ""},
		{"متن ساده", "متن ساده", ""},
	}
	for _, c := range cases {
		if label, id := set.Button(c.in); label != c.label || id != c.id {
			t.Errorf("Button(%q) = %q, %q; want %q, %q", c.in, label, id, c.label, c.id)
		}
	}
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

func TestParseMappings(t *testing.T) {
	// "😀 ⭐\n🔥 5368324170671202286\nhello 123456\n❤️\n" where ⭐ is a custom emoji entity.
	line1 := "😀 ⭐"
	text := line1 + "\n🔥 5368324170671202286\nhello 123456\n❤️\n👍 ✨ extra words"
	ents := []Entity{
		{Offset: utf16Len("😀 "), Length: utf16Len("⭐"), CustomEmojiID: "777"},
		{Offset: utf16Len(line1 + "\n🔥 5368324170671202286\nhello 123456\n❤️\n👍 "), Length: utf16Len("✨"), CustomEmojiID: "888"},
	}
	pairs, problems := ParseMappings(text, ents)
	got := map[string]string{}
	for _, p := range pairs {
		got[p.Emoji] = p.ID
	}
	if len(pairs) != 3 || got["😀"] != "777" || got["🔥"] != "5368324170671202286" || got["👍"] != "888" {
		t.Fatalf("pairs: %+v", pairs)
	}
	if len(problems) != 2 || !strings.Contains(problems[0].Line, "hello") || problems[0].MissingID ||
		!strings.Contains(problems[1].Line, "❤️") || !problems[1].MissingID {
		t.Errorf("problems: %+v", problems)
	}
}

func TestIsEmoji(t *testing.T) {
	for _, ok := range []string{"😀", "❤️", "👨‍💻", "👍🏽", "1️⃣", "🏳️‍🌈", "©️", "⭐", "‼️", "⁉️", "ℹ️", "ℹ", "™️"} {
		if !IsEmoji(ok) {
			t.Errorf("%q should be an emoji", ok)
		}
	}
	for _, bad := range []string{"a", "سلام", "12", "", "😀abc", "#"} {
		if IsEmoji(bad) {
			t.Errorf("%q is not an emoji", bad)
		}
	}
}

func TestPreview(t *testing.T) {
	out, more := Preview(map[string]string{"😀": "1", "🔥": "2", "❤️": "3"}, 2)
	if strings.Count(out, "<tg-emoji") != 2 || more != 1 {
		t.Errorf("preview: %s (+%d)", out, more)
	}
}

func TestDedupeAndFilter(t *testing.T) {
	// One emoji keeps its first mapping; different emojis may share a premium emoji.
	kept, extra := Dedupe([]Pair{{"❤️", "1"}, {"❤", "2"}, {"😀", "3"}, {"🙂", "3"}, {"🔥", "4"}})
	if len(kept) != 4 || len(extra) != 1 || extra[0].Emoji != "❤" || kept[2].Emoji != "🙂" {
		t.Fatalf("kept %v extra %v", kept, extra)
	}
	add, rej := Filter(map[string]string{"❤️": "1", "😀": "3"},
		[]Pair{{"❤", "9"}, {"🌟", "3"}, {"🔥", "5"}, {"🔥", "6"}, {"🎉", "5"}, {"🚀", "7"}})
	if len(add) != 2 || add[0].Emoji != "🔥" || add[1].Emoji != "🚀" {
		t.Fatalf("add %v", add)
	}
	if len(rej) != 4 || !rej[0].Taken || rej[1].UsedFor != "😀" || !rej[2].Twice || rej[3].UsedFor != "🔥" {
		t.Fatalf("rejected %+v", rej)
	}
}
