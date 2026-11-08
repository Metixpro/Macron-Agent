// Package cmdgate decides whether a shell command the agent wants to run on
// this machine may run: a floor of commands that never run (HARDLINE), the
// owner's deny rules, a permanent allowlist, a guardian model, the owner in
// the chat, and a breaker that stops a job after several refusals in a row.
//
// Commands are parsed, not matched as raw text: a rule looks at the command
// in command position (rm, dd, mkfs…) with its real arguments, so the same
// words inside a quoted message (git commit -m "never rm -rf /") are
// harmless, while quoting, escapes and empty quotes (r”m, \rm) cannot hide
// a command, and what sh -c, bash -c, eval, $(…) and `…` run is read too.
package cmdgate

import (
	"strings"
	"unicode"
)

// Cmd is one simple command: its words with quotes and escapes resolved.
type Cmd struct {
	Words   []string
	Redirs  []Redir
	Heredoc bool // it reads a heredoc or a here-string
}

// Redir is a redirection: its operator (">", ">>", "<", "2>"…) and target.
type Redir struct {
	Op, Target string
}

// Pipeline is commands joined by |.
type Pipeline []Cmd

// Script is what a command line runs: its pipelines in order, and the code
// it runs inside it ($(…), `…`, <(…), >(…)), to be read on its own.
type Script struct {
	Pipelines []Pipeline
	Nested    []string
}

// invisible are characters that change nothing a shell runs but split words
// for a reader: control bytes and zero-width marks.
func invisible(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20 || r == 0x7f:
		return true
	case r >= 0x200b && r <= 0x200f, r == 0x2060, r == 0xfeff, r == 0x00ad:
		return true
	}
	return false
}

// Clean drops the invisible characters of a command (what a person sees is
// what is checked).
func Clean(s string) string {
	return strings.Map(func(r rune) rune {
		if invisible(r) {
			return -1
		}
		return r
	}, s)
}

// Parse reads a command line the way a POSIX shell splits it.
func Parse(s string) Script {
	p := &parser{src: []rune(Clean(s))}
	p.run()
	return p.out
}

type parser struct {
	src   []rune
	i     int
	out   Script
	pipe  Pipeline
	cmd   Cmd
	word  strings.Builder
	has   bool     // the word has started (an empty "" is a word)
	redir string   // the operator waiting for its target
	docs  []string // heredoc delimiters waiting for the next line
}

func (p *parser) peek(k int) rune {
	if p.i+k < len(p.src) {
		return p.src[p.i+k]
	}
	return 0
}

func (p *parser) endWord() {
	if !p.has {
		return
	}
	w := p.word.String()
	p.word.Reset()
	p.has = false
	if p.redir != "" {
		if strings.HasPrefix(p.redir, "<<") && p.redir != "<<<" {
			delim := strings.Trim(w, `'"`)
			p.docs = append(p.docs, delim)
		}
		p.cmd.Redirs = append(p.cmd.Redirs, Redir{Op: p.redir, Target: w})
		p.redir = ""
		return
	}
	p.cmd.Words = append(p.cmd.Words, w)
}

func (p *parser) endCmd() {
	p.endWord()
	if len(p.cmd.Words) > 0 || len(p.cmd.Redirs) > 0 {
		p.pipe = append(p.pipe, p.cmd)
	}
	p.cmd = Cmd{}
}

func (p *parser) endPipe() {
	p.endCmd()
	if len(p.pipe) > 0 {
		p.out.Pipelines = append(p.out.Pipelines, p.pipe)
	}
	p.pipe = nil
}

// nested reads $( … ) or <( … ) from the opening parenthesis on (p.i at it)
// and returns what is inside, honouring quotes and nesting.
func (p *parser) nested() string {
	start := p.i + 1
	depth := 0
	var quote rune
	for ; p.i < len(p.src); p.i++ {
		c := p.src[p.i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' {
				p.i++
			} else if c == quote {
				quote = 0
			}
		case c == '\\':
			p.i++
		case c == '\'' || c == '"':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return string(p.src[start:p.i])
			}
		}
	}
	return string(p.src[start:]) // unbalanced: what there is
}

// backtick reads `…` from the opening backtick on.
func (p *parser) backtick() string {
	start := p.i + 1
	for p.i++; p.i < len(p.src); p.i++ {
		if p.src[p.i] == '\\' {
			p.i++
			continue
		}
		if p.src[p.i] == '`' {
			return string(p.src[start:p.i])
		}
	}
	return string(p.src[start:])
}

// skipHeredocs drops the heredoc bodies that start after a newline: they are
// data for their command, not commands.
func (p *parser) skipHeredocs() {
	for len(p.docs) > 0 {
		delim := p.docs[0]
		p.docs = p.docs[1:]
		for p.i < len(p.src) {
			end := p.i
			for end < len(p.src) && p.src[end] != '\n' {
				end++
			}
			line := strings.TrimLeft(string(p.src[p.i:end]), "\t")
			p.i = end + 1
			if strings.TrimRightFunc(line, unicode.IsSpace) == delim {
				break
			}
		}
	}
	p.i-- // the loop of run steps over the last newline
}

func (p *parser) run() {
	for ; p.i < len(p.src); p.i++ {
		c := p.src[p.i]
		switch {
		case c == '\\':
			if p.peek(1) == '\n' { // a line continuation
				p.i++
				continue
			}
			if p.i+1 < len(p.src) {
				p.i++
				p.word.WriteRune(p.src[p.i])
			}
			p.has = true
		case c == '\'':
			p.has = true
			for p.i++; p.i < len(p.src) && p.src[p.i] != '\''; p.i++ {
				p.word.WriteRune(p.src[p.i])
			}
		case c == '"':
			p.has = true
			for p.i++; p.i < len(p.src) && p.src[p.i] != '"'; p.i++ {
				d := p.src[p.i]
				switch {
				case d == '\\' && strings.ContainsRune("\"\\$`\n", p.peek(1)):
					p.i++
					p.word.WriteRune(p.src[p.i])
				case d == '$' && p.peek(1) == '(':
					p.i++
					p.out.Nested = append(p.out.Nested, p.nested())
					p.word.WriteString("$(…)")
				case d == '`':
					p.out.Nested = append(p.out.Nested, p.backtick())
					p.word.WriteString("$(…)")
				default:
					p.word.WriteRune(d)
				}
			}
		case c == '$' && p.peek(1) == '(':
			p.i++
			p.out.Nested = append(p.out.Nested, p.nested())
			p.word.WriteString("$(…)")
			p.has = true
		case c == '`':
			p.out.Nested = append(p.out.Nested, p.backtick())
			p.word.WriteString("$(…)")
			p.has = true
		case (c == '<' || c == '>') && p.peek(1) == '(':
			p.endWord()
			p.i++
			p.out.Nested = append(p.out.Nested, p.nested())
			p.word.WriteString(string(c) + "(…)")
			p.has = true
		case c == '#' && !p.has:
			for p.i < len(p.src) && p.src[p.i] != '\n' {
				p.i++
			}
			p.i--
		case c == '\n':
			p.endPipe()
			if len(p.docs) > 0 {
				p.i++
				p.skipHeredocs()
			}
		case c == ';':
			p.endPipe()
		case c == '&':
			if p.peek(1) == '>' { // &> and &>>: a redirection
				p.endWord()
				op := "&>"
				p.i++
				if p.peek(1) == '>' {
					op, p.i = "&>>", p.i+1
				}
				p.redir = op
				continue
			}
			if p.peek(1) == '&' {
				p.i++
			}
			p.endPipe()
		case c == '|':
			if p.peek(1) == '|' {
				p.i++
				p.endPipe()
			} else {
				if p.peek(1) == '&' {
					p.i++
				}
				p.endCmd()
			}
		case c == '(' || c == ')' || c == '{' && !p.has && unicode.IsSpace(p.peek(1)) || c == '}' && !p.has:
			p.endPipe()
		case c == '>' || c == '<':
			// A file descriptor right before it (2>, 1>>) belongs to the operator.
			fd := ""
			if p.has {
				if w := p.word.String(); w != "" && strings.Trim(w, "0123456789") == "" {
					fd, p.has = w, false
					p.word.Reset()
				}
			}
			p.endWord()
			op := fd + string(c)
			for p.peek(1) == '>' || p.peek(1) == '<' || p.peek(1) == '&' || p.peek(1) == '|' || (c == '<' && p.peek(1) == '-') {
				p.i++
				op += string(p.src[p.i])
			}
			if strings.HasSuffix(op, "&") { // 2>&1: no file
				for p.i+1 < len(p.src) && (unicode.IsDigit(p.peek(1)) || p.peek(1) == '-') {
					p.i++
				}
				continue
			}
			if strings.HasPrefix(op, "<<") && op != "<<<" {
				p.cmd.Heredoc = true
			}
			if op == "<<<" {
				p.cmd.Heredoc = true
			}
			p.redir = op
		case unicode.IsSpace(c):
			p.endWord()
		default:
			p.word.WriteRune(c)
			p.has = true
		}
	}
	p.endPipe()
}
