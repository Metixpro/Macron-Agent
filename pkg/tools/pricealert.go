package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	ToolPriceAlert = "price_alert"
	priceAlertsDir = ".macron_price_alerts"
)

type PriceAlert struct {
	ID          string    `json:"id"`
	Asset       string    `json:"asset"`
	AssetFa     string    `json:"asset_fa"`
	Operator    string    `json:"operator"` // ">", ">=", "<", "<="
	TargetPrice float64   `json:"target_price"`
	CurrentPrice float64  `json:"current_price"`
	Note        string    `json:"note,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	Triggered   bool      `json:"triggered"`
	TriggeredAt time.Time `json:"triggered_at,omitempty"`
}

var assetNames = map[string]string{
	"usd":     "دلار آمریکا",
	"eur":     "یورو",
	"aed":     "درهم امارات",
	"gbp":     "پوند انگلیس",
	"gold18":  "طلای ۱۸ عیار",
	"gold24":  "طلای ۲۴ عیار",
	"mithqal": "مثقال طلا",
	"ounce":   "انس طلا",
	"emami":   "سکه امامی",
	"bahar":   "سکه بهار آزادی",
	"nim":     "نیم سکه",
	"rob":     "ربع سکه",
	"btc":     "بیت‌کوین (BTC)",
	"eth":     "اتریوم (ETH)",
	"usdt":    "تتر (USDT)",
}

// CreatePriceAlert creates a threshold alert in the workspace.
func (r *Registry) CreatePriceAlert(sessionID, asset, op string, target float64, note string) (*PriceAlert, error) {
	asset = strings.ToLower(strings.TrimSpace(asset))
	nameFa := assetNames[asset]
	if nameFa == "" {
		nameFa = strings.ToUpper(asset)
	}

	op = strings.TrimSpace(op)
	switch op {
	case ">", ">=", "<", "<=":
	default:
		op = ">="
	}

	id := fmt.Sprintf("alert_%s_%x", sanitizeBase(asset), time.Now().Unix()%100000)
	alert := &PriceAlert{
		ID:          id,
		Asset:       asset,
		AssetFa:     nameFa,
		Operator:    op,
		TargetPrice: target,
		Note:        note,
		CreatedAt:   time.Now(),
		Triggered:   false,
	}

	data, _ := json.MarshalIndent(alert, "", "  ")
	path := fmt.Sprintf("%s/%s.json", priceAlertsDir, id)
	if err := r.Machine.WriteFile(sessionID, path, data); err != nil {
		return nil, err
	}
	return alert, nil
}

// CheckPriceAlerts evaluates all active alerts against live prices and returns triggered messages.
func (r *Registry) CheckPriceAlerts(ctx context.Context, sessionID string) ([]string, error) {
	alerts, err := r.ListPriceAlerts(sessionID)
	if err != nil || len(alerts) == 0 {
		return nil, err
	}

	// Fetch current market quotes
	quotes, _ := r.fetchMarketQuotes(ctx)

	var notifications []string
	for _, a := range alerts {
		if a.Triggered {
			continue
		}
		curVal, found := quotes[a.Asset]
		if !found {
			continue
		}
		a.CurrentPrice = curVal

		triggered := false
		switch a.Operator {
		case ">":
			triggered = curVal > a.TargetPrice
		case ">=":
			triggered = curVal >= a.TargetPrice
		case "<":
			triggered = curVal < a.TargetPrice
		case "<=":
			triggered = curVal <= a.TargetPrice
		}

		if triggered {
			a.Triggered = true
			a.TriggeredAt = time.Now()
			// Update file
			data, _ := json.MarshalIndent(a, "", "  ")
			_ = r.Machine.WriteFile(sessionID, fmt.Sprintf("%s/%s.json", priceAlertsDir, a.ID), data)

			msg := fmt.Sprintf("🚨 **هشدار قیمت:** %s به قیمت %.0f رسید (شرط: %s %.0f)",
				a.AssetFa, curVal, a.Operator, a.TargetPrice)
			if a.Note != "" {
				msg += fmt.Sprintf(" — یادداشت: %s", a.Note)
			}
			notifications = append(notifications, msg)
		}
	}
	return notifications, nil
}

// ListPriceAlerts lists all alerts in the workspace.
func (r *Registry) ListPriceAlerts(sessionID string) ([]PriceAlert, error) {
	files, err := r.Machine.ListFiles(sessionID)
	if err != nil {
		return nil, err
	}
	var out []PriceAlert
	for _, f := range files {
		if strings.HasPrefix(f.Name, priceAlertsDir+"/") && strings.HasSuffix(f.Name, ".json") {
			data, _, err := r.Machine.ReadFile(sessionID, f.Name, 32<<10)
			if err == nil {
				var a PriceAlert
				if json.Unmarshal(data, &a) == nil {
					out = append(out, a)
				}
			}
		}
	}
	return out, nil
}

func (r *Registry) fetchMarketQuotes(ctx context.Context) (map[string]float64, error) {
	var allKeys []string
	for k := range assetNames {
		allKeys = append(allKeys, k)
	}
	snap, err := r.marketSnapshot(ctx, allKeys)
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64)
	for k, q := range snap.quotes {
		out[k] = q.value
	}
	return out, nil
}

// DeletePriceAlert deletes an alert.
func (r *Registry) DeletePriceAlert(sessionID, alertID string) error {
	path := fmt.Sprintf("%s/%s.json", priceAlertsDir, alertID)
	return r.Machine.Remove(sessionID, path)
}
