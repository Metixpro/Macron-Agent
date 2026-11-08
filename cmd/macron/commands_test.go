package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func handledPrivateCommands(t *testing.T) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "bot.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "handleMessage" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SwitchStmt:
				id, ok := n.Tag.(*ast.Ident)
				if !ok || id.Name != "cmd" {
					return true
				}
				for _, cl := range n.Body.List {
					for _, v := range cl.(*ast.CaseClause).List {
						if lit, ok := v.(*ast.BasicLit); ok {
							s, _ := strconv.Unquote(lit.Value)
							if strings.HasPrefix(s, "/") {
								out[strings.TrimPrefix(s, "/")] = true
							}
						}
					}
				}
			case *ast.BinaryExpr:
				id, ok := n.X.(*ast.Ident)
				if !ok || id.Name != "cmd" || n.Op != token.EQL {
					return true
				}
				if lit, ok := n.Y.(*ast.BasicLit); ok {
					s, _ := strconv.Unquote(lit.Value)
					if strings.HasPrefix(s, "/") {
						out[strings.TrimPrefix(s, "/")] = true
					}
				}
			}
			return true
		})
	}
	if len(out) < 30 {
		t.Fatalf("only %d handlers found", len(out))
	}
	return out
}

func TestOwnerCommandMenuCoversEveryPrivateHandlerAndAlias(t *testing.T) {
	handlers := handledPrivateCommands(t)
	valid := regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
	for _, l := range []lang{fa, en} {
		menu := commandMenu(l, commandOwner)
		if len(menu) > 100 {
			t.Fatal("Telegram supports at most 100 commands")
		}
		seen := map[string]bool{}
		for _, c := range menu {
			if seen[c.Command] || !valid.MatchString(c.Command) {
				t.Errorf("invalid/duplicate command: %s", c.Command)
			}
			seen[c.Command] = true
			if !handlers[c.Command] {
				t.Errorf("menu advertises an unavailable handler: /%s", c.Command)
			}
			if n := utf8.RuneCountInString(c.Description); n < 1 || n > 256 {
				t.Errorf("description length for %s: %d", c.Command, n)
			}
			if l == en && hasPersian(c.Description) {
				t.Errorf("Persian in English menu: %s", c.Description)
			}
		}
		for name := range handlers {
			if !seen[name] {
				t.Errorf("/%s is handled but absent from %s menu", name, l.code())
			}
		}
	}
}

func TestRegisteredOwnerMenusContainAllCommandsInEachLanguage(t *testing.T) {
	h := personalHarness(t)
	if err := h.bot.registerCommands(bg); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, c := range h.tg.of("setMyCommands") {
		scope, _ := c.body["scope"].(map[string]any)
		if scope["type"] != "chat" || scope["chat_id"] != float64(ownerID) {
			continue
		}
		code, _ := c.body["language_code"].(string)
		found[code] = true
		commands := map[string]bool{}
		for _, v := range c.body["commands"].([]any) {
			commands[v.(map[string]any)["command"].(string)] = true
		}
		for name := range handledPrivateCommands(t) {
			if !commands[name] {
				t.Errorf("owner %s menu omitted /%s", code, name)
			}
		}
	}
	for _, code := range []string{"", "en", "fa"} {
		if !found[code] {
			t.Errorf("missing owner locale %q", code)
		}
	}
}

func TestCommandMenuRespectsAccessAndReportsOwnerRegistrationErrors(t *testing.T) {
	for _, role := range []commandRole{commandUser, commandAdmin} {
		for _, c := range commandMenu(en, role) {
			for _, spec := range commandSpecs {
				for _, name := range strings.Fields(spec.names) {
					if name == c.Command && spec.role > role {
						t.Errorf("/%s leaked into role %d menu", name, role)
					}
				}
			}
		}
	}
	h := personalHarness(t)
	h.tg.reject = func(method string, body map[string]any) string {
		scope, _ := body["scope"].(map[string]any)
		if method == "setMyCommands" && scope["type"] == "chat" {
			return "Bad Request: menu update refused"
		}
		return ""
	}
	err := h.bot.registerCommands(bg)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprint(ownerID)) || !strings.Contains(err.Error(), "menu update refused") {
		t.Fatalf("owner-menu failure was hidden: %v", err)
	}
}
