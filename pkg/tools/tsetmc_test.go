package tools

import (
	"strings"
	"testing"
)

func TestTSETMCFormat(t *testing.T) {
	overview := &MarketOverview{
		Index:        2154320,
		IndexChange:  12450,
		IndexPercent: 0.58,
		EqualIndex:   724500,
		UpdatedAt:    "12:30",
		Quotes: []StockQuote{
			{
				Symbol:        "فولاد",
				Name:          "فولاد مبارکه اصفهان",
				LastPrice:     4850,
				ClosePrice:    4800,
				ChangePercent: 1.2,
				Volume:        50000000,
				PE:            7.2,
				BuyQueue:      "10M صف خرید",
			},
		},
		CodalLetters: []CodalLetter{
			{
				Symbol:      "فولاد",
				Title:       "افشای اطلاعات بااهمیت - الف",
				PublishDate: "1403/08/21",
				URL:         "https://codal.ir/test",
			},
		},
	}

	formatted := overview.Format()
	if !strings.Contains(formatted, "بازار بورس اوراق بهادار تهران") {
		t.Errorf("missing title: %s", formatted)
	}
	if !strings.Contains(formatted, "فولاد") || !strings.Contains(formatted, "485 تومان") {
		t.Errorf("missing symbol and price in toman: %s", formatted)
	}
	if !strings.Contains(formatted, "افشای اطلاعات بااهمیت") {
		t.Errorf("missing Codal announcement: %s", formatted)
	}
}
