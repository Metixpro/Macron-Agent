//go:build windows

package tui

import tea "github.com/charmbracelet/bubbletea"

const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

type vtPart struct {
	bytes []byte
	msg   tea.Msg
}

type vtSplitter struct {
	carry   []rune
	paste   []rune
	pasting bool
}

func runesAt(rs []rune, i int, s string) bool {
	j := i
	for _, c := range s {
		if j >= len(rs) || rs[j] != c {
			return false
		}
		j++
	}
	return true
}

func partialOf(rs []rune, s string) bool {
	m := []rune(s)
	if len(rs) == 0 || len(rs) >= len(m) {
		return false
	}
	for i, r := range rs {
		if m[i] != r {
			return false
		}
	}
	return true
}

func (s *vtSplitter) feed(in []rune) []vtPart {
	rs := append(s.carry, in...)
	s.carry = nil
	var out []vtPart
	var buf []byte
	var wide []rune
	flushBytes := func() {
		if len(buf) > 0 {
			out = append(out, vtPart{bytes: buf})
			buf = nil
		}
	}
	flushWide := func() {
		if len(wide) > 0 {
			out = append(out, vtPart{msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: wide}})
			wide = nil
		}
	}
	for i := 0; i < len(rs); i++ {
		if s.pasting {
			switch {
			case runesAt(rs, i, pasteEnd):
				out = append(out, vtPart{msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: s.paste, Paste: true}})
				s.paste, s.pasting = nil, false
				i += len(pasteEnd) - 1
			case rs[i] == 0x1b && partialOf(rs[i:], pasteEnd):
				s.carry = append([]rune(nil), rs[i:]...)
				i = len(rs)
			default:
				s.paste = append(s.paste, rs[i])
			}
			continue
		}
		if rs[i] == 0x1b {
			if runesAt(rs, i, pasteStart) {
				flushWide()
				flushBytes()
				s.pasting = true
				i += len(pasteStart) - 1
				continue
			}
			if len(rs)-i > 1 && partialOf(rs[i:], pasteStart) {
				flushWide()
				flushBytes()
				s.carry = append([]rune(nil), rs[i:]...)
				break
			}
		}
		if rs[i] < 0x80 {
			flushWide()
			buf = append(buf, byte(rs[i]))
		} else {
			flushBytes()
			wide = append(wide, rs[i])
		}
	}
	flushWide()
	flushBytes()
	return out
}
