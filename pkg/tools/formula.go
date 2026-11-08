package tools

import (
	"math"
	"strconv"
	"strings"
	"unicode"
)

// A formula in a file the bot makes has no value until a spreadsheet
// program opens it and calculates; a phone's preview, Telegram's among
// them, shows the stored value only, so «=SUM(B2:B9)» would show nothing.
// The common formulas are worked out here and their values stored with
// them: numbers, cells, ranges, + − × ÷ ^ and %, SUM, AVERAGE, MIN, MAX,
// COUNT, ROUND, ABS and PRODUCT on the same sheet. Anything else is left
// to the program (the workbook asks it to calculate everything on load).

type sheetCalc struct {
	rows [][]any
	memo map[string]float64
	bad  map[string]bool // no value here
	busy map[string]bool // being worked out: a cycle
}

func newSheetCalc(rows [][]any) *sheetCalc {
	return &sheetCalc{rows: rows, memo: map[string]float64{}, bad: map[string]bool{}, busy: map[string]bool{}}
}

// formulaValue is the value of the formula in a cell, when it can be worked out.
func (c *sheetCalc) formulaValue(row, col int) (float64, bool) {
	v, ok, _ := c.cell(row, col)
	return v, ok
}

// cell is a cell's number: ok false when it has none (text, an error);
// empty for a blank cell.
func (c *sheetCalc) cell(row, col int) (v float64, ok, empty bool) {
	if row < 0 || row >= len(c.rows) || col < 0 || col >= len(c.rows[row]) || c.rows[row][col] == nil {
		return 0, false, true
	}
	switch x := c.rows[row][col].(type) {
	case float64, int, int64:
		return toFloat(x), true, false
	case bool:
		return map[bool]float64{true: 1}[x], true, false
	case string:
		if len(x) < 2 || x[0] != '=' {
			return 0, false, x == ""
		}
		key := strconv.Itoa(row) + ":" + strconv.Itoa(col)
		if v, done := c.memo[key]; done {
			return v, true, false
		}
		if c.bad[key] || c.busy[key] {
			return 0, false, false
		}
		c.busy[key] = true
		p := &formulaParser{src: x[1:], calc: c}
		v, ok := p.expr()
		p.space()
		ok = ok && p.pos == len(p.src) && !math.IsNaN(v) && !math.IsInf(v, 0)
		delete(c.busy, key)
		if !ok {
			c.bad[key] = true
			return 0, false, false
		}
		c.memo[key] = v
		return v, true, false
	}
	return 0, false, false
}

type formulaParser struct {
	src  string
	pos  int
	calc *sheetCalc
}

func (p *formulaParser) space() {
	for p.pos < len(p.src) && p.src[p.pos] == ' ' {
		p.pos++
	}
}

func (p *formulaParser) peek() byte {
	p.space()
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return 0
}

// expr: term (+|- term)*
func (p *formulaParser) expr() (float64, bool) {
	v, ok := p.term()
	for ok {
		switch p.peek() {
		case '+':
			p.pos++
			w, ok2 := p.term()
			v, ok = v+w, ok2
		case '-':
			p.pos++
			w, ok2 := p.term()
			v, ok = v-w, ok2
		default:
			return v, true
		}
	}
	return 0, false
}

// term: power (*|/ power)*
func (p *formulaParser) term() (float64, bool) {
	v, ok := p.power()
	for ok {
		switch p.peek() {
		case '*':
			p.pos++
			w, ok2 := p.power()
			v, ok = v*w, ok2
		case '/':
			p.pos++
			w, ok2 := p.power()
			if w == 0 {
				return 0, false // #DIV/0!: left to the program
			}
			v, ok = v/w, ok2
		default:
			return v, true
		}
	}
	return 0, false
}

// power: unary (^ unary)*
func (p *formulaParser) power() (float64, bool) {
	v, ok := p.unary()
	for ok && p.peek() == '^' {
		p.pos++
		w, ok2 := p.unary()
		v, ok = math.Pow(v, w), ok2
	}
	return v, ok
}

func (p *formulaParser) unary() (float64, bool) {
	switch p.peek() {
	case '-':
		p.pos++
		v, ok := p.unary()
		return -v, ok
	case '+':
		p.pos++
		return p.unary()
	}
	v, ok := p.atom()
	if ok && p.peek() == '%' {
		p.pos++
		v /= 100
	}
	return v, ok
}

func (p *formulaParser) atom() (float64, bool) {
	c := p.peek()
	switch {
	case c == '(':
		p.pos++
		v, ok := p.expr()
		if !ok || p.peek() != ')' {
			return 0, false
		}
		p.pos++
		return v, true
	case c >= '0' && c <= '9' || c == '.':
		start := p.pos
		for p.pos < len(p.src) && (p.src[p.pos] >= '0' && p.src[p.pos] <= '9' || p.src[p.pos] == '.') {
			p.pos++
		}
		v, err := strconv.ParseFloat(p.src[start:p.pos], 64)
		return v, err == nil
	case c == '$' || unicode.IsLetter(rune(c)):
		start := p.pos
		for p.pos < len(p.src) && (p.src[p.pos] == '$' || p.src[p.pos] < 0x80 && (unicode.IsLetter(rune(p.src[p.pos])) || unicode.IsDigit(rune(p.src[p.pos])))) {
			p.pos++
		}
		word := strings.ToUpper(p.src[start:p.pos])
		if p.peek() == '(' {
			p.pos++
			return p.function(word)
		}
		row, col, ok := parseRef(word)
		if !ok {
			return 0, false
		}
		v, ok, empty := p.calc.cell(row, col)
		if empty {
			return 0, true
		}
		return v, ok
	}
	return 0, false
}

// function reads the arguments of a function and works it out.
func (p *formulaParser) function(name string) (float64, bool) {
	var nums []float64
	count := 0 // COUNT counts numbers only
	for p.peek() != ')' {
		if !p.argument(&nums, &count) {
			return 0, false
		}
		if p.peek() == ',' || p.peek() == ';' {
			p.pos++
		} else if p.peek() != ')' {
			return 0, false
		}
	}
	p.pos++
	switch name {
	case "SUM":
		s := 0.0
		for _, v := range nums {
			s += v
		}
		return s, true
	case "PRODUCT":
		s := 1.0
		for _, v := range nums {
			s *= v
		}
		return s, len(nums) > 0
	case "AVERAGE":
		if len(nums) == 0 {
			return 0, false
		}
		s := 0.0
		for _, v := range nums {
			s += v
		}
		return s / float64(len(nums)), true
	case "MIN", "MAX":
		if len(nums) == 0 {
			return 0, true
		}
		v := nums[0]
		for _, x := range nums[1:] {
			if name == "MIN" {
				v = math.Min(v, x)
			} else {
				v = math.Max(v, x)
			}
		}
		return v, true
	case "COUNT":
		return float64(count), true
	case "ABS":
		if len(nums) != 1 {
			return 0, false
		}
		return math.Abs(nums[0]), true
	case "ROUND":
		if len(nums) == 0 || len(nums) > 2 {
			return 0, false
		}
		digits := 0.0
		if len(nums) == 2 {
			digits = nums[1]
		}
		f := math.Pow(10, digits)
		return math.Round(nums[0]*f) / f, true
	}
	return 0, false
}

// argument reads one argument: a range (its numbers; text and blanks are
// skipped, as the program does) or an expression.
func (p *formulaParser) argument(nums *[]float64, count *int) bool {
	save := p.pos
	p.space()
	start := p.pos
	for p.pos < len(p.src) && (p.src[p.pos] == '$' || p.src[p.pos] == ':' || p.src[p.pos] < 0x80 && (unicode.IsLetter(rune(p.src[p.pos])) || unicode.IsDigit(rune(p.src[p.pos])))) {
		p.pos++
	}
	if from, to, ok := strings.Cut(strings.ToUpper(p.src[start:p.pos]), ":"); ok {
		r1, c1, ok1 := parseRef(from)
		r2, c2, ok2 := parseRef(to)
		if c := p.peek(); !ok1 || !ok2 || (c != ',' && c != ';' && c != ')') {
			return false
		}
		if (r2-r1+1)*(c2-c1+1) > 100000 {
			return false
		}
		for r := min(r1, r2); r <= max(r1, r2); r++ {
			for c := min(c1, c2); c <= max(c1, c2); c++ {
				v, ok, empty := p.calc.cell(r, c)
				switch {
				case ok:
					*nums = append(*nums, v)
					*count++
				case empty:
				case p.calc.isFormula(r, c):
					return false // a formula with no value here: so has this one
				}
			}
		}
		return true
	}
	p.pos = save
	v, ok := p.expr()
	if ok {
		*nums = append(*nums, v)
		*count++
	}
	return ok
}

func (c *sheetCalc) isFormula(row, col int) bool {
	if row < 0 || row >= len(c.rows) || col < 0 || col >= len(c.rows[row]) {
		return false
	}
	s, ok := c.rows[row][col].(string)
	return ok && len(s) > 1 && s[0] == '='
}

// parseRef reads "B12" or "$B$12" (0-based row and column).
func parseRef(ref string) (row, col int, ok bool) {
	ref = strings.ReplaceAll(ref, "$", "")
	i := 0
	for i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z' {
		col = col*26 + int(ref[i]-'A'+1)
		i++
	}
	if i == 0 || i > 3 || i == len(ref) {
		return 0, 0, false
	}
	n, err := strconv.Atoi(ref[i:])
	if err != nil || n < 1 {
		return 0, 0, false
	}
	return n - 1, col - 1, true
}
