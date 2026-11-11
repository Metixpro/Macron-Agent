package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const ToolCreateDashboard = "create_dashboard"

type DashboardCard struct {
	Title  string `json:"title"`
	Value  string `json:"value"`
	Change string `json:"change,omitempty"`
	Icon   string `json:"icon,omitempty"`
}

type DashboardChart struct {
	Title    string     `json:"title"`
	Type     string     `json:"type"`
	Labels   []string   `json:"labels"`
	Datasets []ChartSet `json:"datasets"`
}

type ChartSet struct {
	Label string    `json:"label"`
	Data  []float64 `json:"data"`
	Color string    `json:"color,omitempty"`
}

type DashboardTable struct {
	Title   string     `json:"title,omitempty"`
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"rows"`
}

func GenerateHTMLDashboard(title string, cards []DashboardCard, charts []DashboardChart, table *DashboardTable) string {
	if title == "" {
		title = "Analytics Dashboard"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`<!DOCTYPE html>
<html lang="fa" dir="rtl">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>%s</title>
  <script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.6/dist/chart.umd.min.js"></script>
  <style>
    :root { --bg: #0f172a; --card: #1e293b; --text: #f8fafc; --sub: #94a3b8; --border: #334155; --accent: #6366f1; }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body { font-family: system-ui, -apple-system, sans-serif; background: var(--bg); color: var(--text); padding: 2rem; }
    .header { margin-bottom: 2rem; display: flex; justify-content: space-between; align-items: center; border-bottom: 1px solid var(--border); padding-bottom: 1rem; }
    .header h1 { font-size: 1.75rem; }
    .timestamp { color: var(--sub); font-size: 0.875rem; }
    .cards-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 1.25rem; margin-bottom: 2rem; }
    .card { background: var(--card); border: 1px solid var(--border); border-radius: 12px; padding: 1.25rem; }
    .card-title { font-size: 0.875rem; color: var(--sub); margin-bottom: 0.5rem; }
    .card-value { font-size: 1.75rem; font-weight: bold; }
    .card-change { font-size: 0.875rem; color: #10b981; margin-top: 0.5rem; }
    .charts-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(400px, 1fr)); gap: 1.5rem; margin-bottom: 2rem; }
    .chart-box { background: var(--card); border: 1px solid var(--border); border-radius: 12px; padding: 1.5rem; }
    .chart-box h3 { font-size: 1.125rem; margin-bottom: 1rem; color: var(--text); }
    .table-box { background: var(--card); border: 1px solid var(--border); border-radius: 12px; padding: 1.5rem; overflow-x: auto; }
    table { width: 100%%; border-collapse: collapse; text-align: right; }
    th { padding: 0.75rem 1rem; border-bottom: 1px solid var(--border); color: var(--sub); font-weight: 600; }
    td { padding: 0.75rem 1rem; border-bottom: 1px solid rgba(255,255,255,0.05); }
    tr:hover { background: rgba(255,255,255,0.02); }
  </style>
</head>
<body>
  <div class="header">
    <h1>%s</h1>
    <div class="timestamp">بروزرسانی: %s</div>
  </div>
`, title, title, time.Now().Format("2006-01-02 15:04")))

	if len(cards) > 0 {
		sb.WriteString(`  <div class="cards-grid">\n`)
		for _, c := range cards {
			changeHTML := ""
			if c.Change != "" {
				changeHTML = fmt.Sprintf(`<div class="card-change">%s</div>`, c.Change)
			}
			sb.WriteString(fmt.Sprintf(`    <div class="card">
      <div class="card-title">%s</div>
      <div class="card-value">%s</div>
      %s
    </div>
`, c.Title, c.Value, changeHTML))
		}
		sb.WriteString(`  </div>\n`)
	}

	if len(charts) > 0 {
		sb.WriteString(`  <div class="charts-grid">\n`)
		for i, ch := range charts {
			canvasID := fmt.Sprintf("chart_%d", i+1)
			sb.WriteString(fmt.Sprintf(`    <div class="chart-box">
      <h3>%s</h3>
      <canvas id="%s"></canvas>
    </div>
`, ch.Title, canvasID))
		}
		sb.WriteString(`  </div>\n`)
	}

	if table != nil && len(table.Headers) > 0 {
		tTitle := table.Title
		if tTitle == "" {
			tTitle = "جدول داده‌ها"
		}
		sb.WriteString(fmt.Sprintf(`  <div class="table-box">
    <h3 style="margin-bottom: 1rem;">%s</h3>
    <table>
      <thead>
        <tr>
`, tTitle))
		for _, h := range table.Headers {
			sb.WriteString(fmt.Sprintf(`          <th>%s</th>\n`, h))
		}
		sb.WriteString(`        </tr>
      </thead>
      <tbody>
`)
		for _, row := range table.Rows {
			sb.WriteString(`        <tr>\n`)
			for _, cell := range row {
				sb.WriteString(fmt.Sprintf(`          <td>%s</td>\n`, cell))
			}
			sb.WriteString(`        </tr>\n`)
		}
		sb.WriteString(`      </tbody>
    </table>
  </div>
`)
	}

	if len(charts) > 0 {
		sb.WriteString(`  <script>\n`)
		for i, ch := range charts {
			canvasID := fmt.Sprintf("chart_%d", i+1)
			labelsJSON, _ := json.Marshal(ch.Labels)

			type dsPayload struct {
				Label       string    `json:"label"`
				Data        []float64 `json:"data"`
				BorderColor string    `json:"borderColor,omitempty"`
				Background  string    `json:"backgroundColor,omitempty"`
			}
			colors := []string{"#6366f1", "#10b981", "#f59e0b", "#ec4899", "#3b82f6"}
			var dsList []dsPayload
			for j, ds := range ch.Datasets {
				col := colors[j%len(colors)]
				if ds.Color != "" {
					col = ds.Color
				}
				dsList = append(dsList, dsPayload{
					Label:       ds.Label,
					Data:        ds.Data,
					BorderColor: col,
					Background:  col + "33",
				})
			}
			datasetsJSON, _ := json.Marshal(dsList)

			sb.WriteString(fmt.Sprintf(`    new Chart(document.getElementById('%s'), {
      type: '%s',
      data: {
        labels: %s,
        datasets: %s
      },
      options: {
        responsive: true,
        plugins: { legend: { labels: { color: '#94a3b8' } } },
        scales: {
          x: { ticks: { color: '#94a3b8' }, grid: { color: '#334155' } },
          y: { ticks: { color: '#94a3b8' }, grid: { color: '#334155' } }
        }
      }
    });\n`, canvasID, ch.Type, labelsJSON, datasetsJSON))
		}
		sb.WriteString(`  </script>\n`)
	}

	sb.WriteString(`</body>
</html>`)
	return sb.String()
}
