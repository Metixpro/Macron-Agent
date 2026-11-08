package tools

import (
	"math"
	"testing"
)

func TestFormulaValues(t *testing.T) {
	rows := [][]any{
		{"item", "price", "qty", "total"},
		{"a", 120.0, 2.0, "=B2*C2"},
		{"b", 80.5, 3.0, "=B3*C3"},
		{"c", "n/a", 1.0, "=B4*C4"},                // text in arithmetic: left to the program
		{"sum", "=SUM(B2:B4)", nil, "=SUM(D2:D3)"}, // text in a range is skipped
		{"x", "=AVERAGE(B2:B3)", "=COUNT(B2:B4)", "=ROUND(D5*9%,1)"},
		{"y", "=MAX(B2:B3)-MIN(B2,B3)", "=-(2+3)^2", "=$D$2/0"}, // as in Excel, - binds before ^
		{"z", "=A1+1", "=VLOOKUP(1,A1:B2,2)", "=D8"},            // a cycle
		{"w", "=SUM(D4,1)", "=ABS(-4)+PRODUCT(2,3)", "=B5+D5"},
	}
	c := newSheetCalc(rows)
	for _, tc := range []struct {
		r, c int
		want float64
		ok   bool
	}{
		{1, 3, 240, true}, {2, 3, 241.5, true}, {3, 3, 0, false},
		{4, 1, 200.5, true}, {4, 3, 481.5, true},
		{5, 1, 100.25, true}, {5, 2, 2, true}, {5, 3, 43.3, true},
		{6, 1, 39.5, true}, {6, 2, 25, true}, {6, 3, 0, false},
		{7, 1, 0, false}, {7, 2, 0, false}, {7, 3, 0, false},
		{8, 1, 0, false}, {8, 2, 10, true}, {8, 3, 682, true},
	} {
		got, ok := c.formulaValue(tc.r, tc.c)
		if ok != tc.ok || (ok && math.Abs(got-tc.want) > 1e-9) {
			t.Errorf("%s: %v %v, want %v %v", rows[tc.r][tc.c], got, ok, tc.want, tc.ok)
		}
	}
}
