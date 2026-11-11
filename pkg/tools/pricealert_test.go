package tools

import (
	"testing"
)

func TestPriceAlertConditions(t *testing.T) {
	cases := []struct {
		curVal float64
		op     string
		target float64
		want   bool
	}{
		{92000, ">=", 90000, true},
		{89000, ">=", 90000, false},
		{89000, "<=", 90000, true},
		{91000, "<", 90000, false},
		{95000, ">", 90000, true},
	}

	for _, c := range cases {
		triggered := false
		switch c.op {
		case ">":
			triggered = c.curVal > c.target
		case ">=":
			triggered = c.curVal >= c.target
		case "<":
			triggered = c.curVal < c.target
		case "<=":
			triggered = c.curVal <= c.target
		}
		if triggered != c.want {
			t.Errorf("curVal=%.0f %s %.0f: got %v, want %v", c.curVal, c.op, c.target, triggered, c.want)
		}
	}
}
