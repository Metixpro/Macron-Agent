package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func translatedTexts(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	sort.Strings(files)
	consts := map[string]string{}
	var parsed []*ast.File
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "i18n_en.go" {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, af)
		for _, d := range af.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.CONST {
				for _, sp := range g.Specs {
					vs := sp.(*ast.ValueSpec)
					for i, n := range vs.Names {
						if i < len(vs.Values) {
							if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING {
								consts[n.Name], _ = strconv.Unquote(bl.Value)
							}
						}
					}
				}
			}
		}
	}
	found := map[string]string{}
	add := func(pos token.Pos, s string) {
		if hasPersian(s) {
			if _, ok := found[s]; !ok {
				found[s] = fset.Position(pos).String()
			}
		}
	}
	literals := func(n ast.Node) {
		ast.Inspect(n, func(n ast.Node) bool {
			if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING {
				v, _ := strconv.Unquote(bl.Value)
				add(bl.Pos(), v)
			}
			return true
		})
	}
	tables := map[string]bool{"audienceNames": true, "statusNames": true, "fileCategories": true, "categoryAll": true, "effortLevels": true, "thinkKinds": true}
	for _, af := range parsed {
		for _, d := range af.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.VAR {
				for _, sp := range g.Specs {
					if vs := sp.(*ast.ValueSpec); len(vs.Names) > 0 && tables[vs.Names[0].Name] {
						literals(vs)
					}
				}
			}
		}
		ast.Inspect(af, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				if x.Name.Name == "toolStatus" {
					literals(x)
				}
			case *ast.CallExpr:
				sel, ok := x.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "t" && sel.Sel.Name != "f") || len(x.Args) == 0 {
					return true
				}
				switch a := x.Args[0].(type) {
				case *ast.BasicLit:
					if a.Kind == token.STRING {
						v, _ := strconv.Unquote(a.Value)
						add(a.Pos(), v)
					}
				case *ast.Ident:
					if v, ok := consts[a.Name]; ok {
						add(a.Pos(), v)
					}
				}
			}
			return true
		})
	}
	return found
}

var (
	fmtVerbRe = regexp.MustCompile(`%(?:\[\d+\])?[-+# 0]*\d*(?:\.\d+)?[a-zA-Z%]`)
	htmlTagRe = regexp.MustCompile(`</?(?:b|i|u|s|code|pre|a|tg-emoji|blockquote)\b[^>]*>`)
)

func fmtVerbs(s string) []string {
	var out []string
	for _, v := range fmtVerbRe.FindAllString(s, -1) {
		if v != "%%" {
			out = append(out, v)
		}
	}
	return out
}

func htmlTags(s string) []string {
	out := htmlTagRe.FindAllString(s, -1)
	sort.Strings(out)
	return out
}

func edgeSpace(s string) string {
	return fmt.Sprintf("%q|%q", s[:len(s)-len(strings.TrimLeft(s, " \n\t"))], s[len(strings.TrimRight(s, " \n\t")):])
}

func persianLeft(s string) bool { return hasPersian(strings.ReplaceAll(s, "فارسی", "")) }

func TestEnglishCatalog(t *testing.T) {
	used := translatedTexts(t)
	if len(used) < 500 {
		t.Fatalf("found only %d texts; is the scan broken?", len(used))
	}
	var missing []string
	for s, where := range used {
		if _, ok := english[s]; !ok {
			missing = append(missing, fmt.Sprintf("%s: %q", where, s))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d texts have no English in i18n_en.go:\n%s", len(missing), strings.Join(missing, "\n"))
	}
	for fa, e := range english {
		if _, ok := used[fa]; !ok {
			t.Errorf("unused catalog entry %q", fa)
		}
		if a, b := fmtVerbs(fa), fmtVerbs(e); fmt.Sprint(a) != fmt.Sprint(b) {
			t.Errorf("fmt verbs differ: %q %v vs %q %v", fa, a, e, b)
		}
		if a, b := htmlTags(fa), htmlTags(e); fmt.Sprint(a) != fmt.Sprint(b) {
			t.Errorf("HTML tags differ: %q %v vs %q %v", fa, a, e, b)
		}
		if a, b := edgeSpace(fa), edgeSpace(e); a != b {
			t.Errorf("leading/trailing space differs: %q %s vs %q %s", fa, a, e, b)
		}
		if persianLeft(e) {
			t.Errorf("English still has Persian: %q", e)
		}
	}
}

func TestLangFromCode(t *testing.T) {
	for code, want := range map[string]lang{"": fa, "fa": fa, "fa-IR": fa, "ps": fa, "en": en, "en-US": en, "de": en, "ar": en, "RU": en} {
		if got := langFromCode(code); got != want {
			t.Errorf("%q: %v, want %v", code, got, want)
		}
	}
	if fa.t("⬅️ برگشت") != "⬅️ برگشت" || en.t("⬅️ برگشت") != "⬅️ Back" || en.t("not in the catalog") != "not in the catalog" {
		t.Error("t")
	}
	if en.num(1234) != "1234" || fa.num(1234) != "۱۲۳۴" {
		t.Error("num")
	}
}

func englishMsg(userID int64, text string) *telegram.Message {
	m := private(userID, text)
	m.From.FirstName, m.From.Username, m.From.LanguageCode = "John", "john_dev", "en-GB"
	return m
}

const userData = "عاشق Go و قهوه"

func (h *harness) noPersian(step string) {
	h.t.Helper()
	for _, s := range h.tg.texts() {
		if persianLeft(strings.ReplaceAll(s, userData, "")) {
			h.t.Errorf("%s: Persian sent to an English user:\n%s", step, s)
		}
	}
	h.tg.mu.Lock()
	defer h.tg.mu.Unlock()
	for _, c := range h.tg.calls {
		for _, k := range []string{"reply_markup", "text"} {
			if v, ok := c.body[k]; ok && c.method != "sendMessage" && c.method != "editMessageText" && persianLeft(fmt.Sprint(v)) {
				h.t.Errorf("%s: Persian in %s %s: %v", step, c.method, k, v)
			}
		}
		if v, ok := c.body["reply_markup"]; ok && persianLeft(fmt.Sprint(v)) {
			h.t.Errorf("%s: Persian button for an English user: %v", step, v)
		}
	}
}

func TestEnglishUser(t *testing.T) {
	h := newHarness(t)
	const uid = 7001
	for _, cmd := range []string{"/start", "/help", "/me", "/memory", "/files", "/reminders", "/new", "/cancel", "/id", "/nope",
		"/buy", "/terms", "/paysupport", "/recap", "/language"} {
		h.tg.reset()
		h.send(englishMsg(uid, cmd))
		if len(h.tg.texts()) == 0 {
			t.Fatalf("%s: no answer", cmd)
		}
		h.noPersian(cmd)
	}
	h.tg.reset()
	h.send(englishMsg(uid, "/start"))
	h.wantText("Hi John")
	h.wantText("/language")

	h.tg.reset()
	h.send(englishMsg(uid, "hi there"))
	if frames := liveFrames(h); len(frames) == 0 || strings.Contains(strings.Join(frames, ""), "دارم") {
		t.Fatalf("English live status missing: %v", frames)
	}

	h.tg.reset()
	h.llm.reply = say("Hello John!")
	h.send(englishMsg(uid, "hello"))
	if !strings.Contains(llmText(h.llm.last()), "uses the bot in English") {
		t.Error("the agent is not told to answer in English")
	}
	h.noPersian("chat")

	h.tg.reset()
	h.send(englishMsg(uid, "/language"))
	h.wantText("Which language")
	if !strings.Contains(lastMarkup(h), "lang:fa") || !strings.Contains(lastMarkup(h), "lang:en") {
		t.Fatalf("language buttons: %s", lastMarkup(h))
	}
	h.click(uid, uid, 5, "lang:fa")
	if p, _ := h.st.UserPref(bg, uid, prefLang); p != "fa" {
		t.Fatalf("stored language %q", p)
	}
	h.wantText("فارسی حرف می‌زنیم")
	h.tg.reset()
	h.send(englishMsg(uid, "/help"))
	h.wantText("چطوری باهام کار کنی؟")

	h.click(uid, uid, 5, "lang:en")
	h.wantText("we'll talk in English")
	h.bot.langs = langs{}
	if h.bot.lang(uid) != en {
		t.Error("the choice was not reloaded from the database")
	}

	h.tg.reset()
	h.send(private(7002, "/help"))
	h.wantText("چطوری باهام کار کنی؟")
}

func TestEnglishAdminPanel(t *testing.T) {
	h := newHarness(t)
	h.send(englishMsg(ownerID, "/start"))
	h.send(englishMsg(55, "hi"))
	h.tg.reset()
	h.send(englishMsg(ownerID, "/admin"))
	h.wantText("Macron Ai admin panel")
	h.noPersian("/admin")
	for _, data := range []string{"a:stats", "a:top", "a:users:0", "a:u:55", "a:st", "a:soul", "a:dl", "a:pv", "a:pv:1", "a:fj", "a:em",
		"a:bc", "a:ad", "a:gr", "a:shop", "a:shop:pay:0", "a:home"} {
		h.tg.reset()
		h.click(ownerID, ownerID, 9, data)
		if len(h.tg.calls) == 0 {
			t.Errorf("%s: nothing happened", data)
		}
		h.noPersian(data)
	}
}

func TestCommandMenus(t *testing.T) {
	h := newHarness(t)
	h.tg.reset()
	if err := h.bot.registerCommands(bg); err != nil {
		t.Fatal(err)
	}
	var sawFa, sawEn bool
	for _, c := range h.tg.of("setMyCommands") {
		menu := fmt.Sprint(c.body["commands"])
		switch c.body["language_code"] {
		case "fa":
			sawFa = sawFa || strings.Contains(menu, "گفتگوی تازه")
		case nil, "":
			sawEn = sawEn || strings.Contains(menu, "New conversation")
			if persianLeft(menu) {
				t.Errorf("Persian in the default (English) menu: %s", menu)
			}
		}
	}
	if !sawFa || !sawEn {
		t.Errorf("menus: Persian %v, English %v", sawFa, sawEn)
	}
}

func TestEnglishGroup(t *testing.T) {
	h := newHarness(t)
	const g = -100901
	h.send(englishMsg(7005, "/start"))
	h.tg.reset()
	addedBy(h, g, 7005)
	var toGroup, toOwner string
	for _, c := range h.tg.of("sendMessage") {
		switch int64(c.body["chat_id"].(float64)) {
		case g:
			toGroup += c.body["text"].(string)
		case ownerID:
			toOwner += c.body["text"].(string)
		}
	}
	if !strings.Contains(toGroup, "has to approve") || persianLeft(toGroup) {
		t.Errorf("an English adder's group is told in English: %q", toGroup)
	}
	if !strings.Contains(toOwner, "منتظر تأیید") {
		t.Errorf("the owner keeps their own language: %q", toOwner)
	}
	h.tg.reset()
	h.bot.handleCallback(&telegram.CallbackQuery{ID: "q", From: telegram.User{ID: ownerID}, Data: fmt.Sprintf("ga:%d:ok", g),
		Message: &telegram.Message{MessageID: 3, Chat: telegram.Chat{ID: ownerID, Type: "private"}}})
	h.bot.wg.Wait()
	h.wantText("start your message with «<b>Macron</b>»")
}
