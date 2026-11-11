package tools

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

const ToolStatsForecast = "statistics_forecast"

type DescriptiveStats struct {
	Count    int     `json:"count"`
	Mean     float64 `json:"mean"`
	Median   float64 `json:"median"`
	Min      float64 `json:"min"`
	Max      float64 `json:"max"`
	Variance float64 `json:"variance"`
	StdDev   float64 `json:"std_dev"`
	Q1       float64 `json:"q1"`
	Q3       float64 `json:"q3"`
	IQR      float64 `json:"iqr"`
}

type ForecastResult struct {
	Slope     float64   `json:"slope"`
	Intercept float64   `json:"intercept"`
	RSquared  float64   `json:"r_squared"`
	Forecast  []float64 `json:"forecast"`
}

func ComputeDescriptiveStats(numbers []float64) DescriptiveStats {
	n := len(numbers)
	if n == 0 {
		return DescriptiveStats{}
	}

	sorted := make([]float64, n)
	copy(sorted, numbers)
	sort.Float64s(sorted)

	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	mean := sum / float64(n)

	median := sorted[n/2]
	if n%2 == 0 {
		median = (sorted[n/2-1] + sorted[n/2]) / 2.0
	}

	varSum := 0.0
	for _, v := range sorted {
		diff := v - mean
		varSum += diff * diff
	}
	variance := varSum / float64(n)
	stdDev := math.Sqrt(variance)

	q1 := sorted[n/4]
	q3 := sorted[3*n/4]

	return DescriptiveStats{
		Count:    n,
		Mean:     mean,
		Median:   median,
		Min:      sorted[0],
		Max:      sorted[n-1],
		Variance: variance,
		StdDev:   stdDev,
		Q1:       q1,
		Q3:       q3,
		IQR:      q3 - q1,
	}
}

func ComputeLinearForecast(series []float64, steps int) ForecastResult {
	n := len(series)
	if n < 2 || steps <= 0 {
		return ForecastResult{}
	}

	sumX, sumY, sumXY, sumX2 := 0.0, 0.0, 0.0, 0.0
	for i, y := range series {
		x := float64(i + 1)
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
	}

	nf := float64(n)
	slope := (nf*sumXY - sumX*sumY) / (nf*sumX2 - sumX*sumX)
	intercept := (sumY - slope*sumX) / nf

	meanY := sumY / nf
	ssTot, ssRes := 0.0, 0.0
	for i, y := range series {
		x := float64(i + 1)
		pred := slope*x + intercept
		ssTot += (y - meanY) * (y - meanY)
		ssRes += (y - pred) * (y - pred)
	}
	r2 := 1.0
	if ssTot > 0 {
		r2 = 1.0 - (ssRes / ssTot)
	}

	forecast := make([]float64, steps)
	for s := 0; s < steps; s++ {
		xNext := float64(n + 1 + s)
		forecast[s] = slope*xNext + intercept
	}

	return ForecastResult{
		Slope:     slope,
		Intercept: intercept,
		RSquared:  r2,
		Forecast:  forecast,
	}
}

func (s DescriptiveStats) Format() string {
	var sb strings.Builder
	sb.WriteString("📈 **تحلیل آماری داده‌ها (Descriptive Statistics):**\n\n")
	sb.WriteString(fmt.Sprintf("- تعداد داده‌ها (Count): %d\n", s.Count))
	sb.WriteString(fmt.Sprintf("- میانگین (Mean): %.2f\n", s.Mean))
	sb.WriteString(fmt.Sprintf("- میانه (Median): %.2f\n", s.Median))
	sb.WriteString(fmt.Sprintf("- کمینه / بیشینه (Min / Max): %.2f / %.2f\n", s.Min, s.Max))
	sb.WriteString(fmt.Sprintf("- انحراف معیار (StdDev): %.2f\n", s.StdDev))
	sb.WriteString(fmt.Sprintf("- دامنه میان‌چارکی (IQR: Q1..Q3): %.2f .. %.2f (IQR = %.2f)\n", s.Q1, s.Q3, s.IQR))
	return sb.String()
}

func (f ForecastResult) Format() string {
	var sb strings.Builder
	sb.WriteString("🔮 **پیش‌بینی سری زمانی (Time-Series Regression Forecast):**\n\n")
	sb.WriteString(fmt.Sprintf("- شیب روند (Slope / Trend): %.4f\n", f.Slope))
	sb.WriteString(fmt.Sprintf("- ضریب تعیین (R² Goodness of fit): %.2f%%\n\n", f.RSquared*100))
	sb.WriteString("**پیش‌بینی دوره‌های آینده:**\n")
	for i, val := range f.Forecast {
		sb.WriteString(fmt.Sprintf("- دوره +%d: **%.2f**\n", i+1, val))
	}
	return sb.String()
}
