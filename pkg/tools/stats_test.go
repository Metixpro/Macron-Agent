package tools

import (
	"math"
	"testing"
)

func TestComputeDescriptiveStats(t *testing.T) {
	nums := []float64{10, 20, 30, 40, 50}
	stats := ComputeDescriptiveStats(nums)

	if stats.Count != 5 {
		t.Errorf("count = %d, want 5", stats.Count)
	}
	if stats.Mean != 30.0 {
		t.Errorf("mean = %f, want 30.0", stats.Mean)
	}
	if stats.Median != 30.0 {
		t.Errorf("median = %f, want 30.0", stats.Median)
	}
	if stats.Min != 10.0 || stats.Max != 50.0 {
		t.Errorf("min/max = %f/%f, want 10/50", stats.Min, stats.Max)
	}
	// Variance of [10, 20, 30, 40, 50]: sum of squared diffs = 400 + 100 + 0 + 100 + 400 = 1000 / 5 = 200
	if math.Abs(stats.Variance-200.0) > 0.001 {
		t.Errorf("variance = %f, want 200.0", stats.Variance)
	}
}

func TestComputeLinearForecast(t *testing.T) {
	// y = 2x + 1: x=[1, 2, 3], y=[3, 5, 7]
	series := []float64{3, 5, 7}
	fc := ComputeLinearForecast(series, 2)

	if math.Abs(fc.Slope-2.0) > 0.001 {
		t.Errorf("slope = %f, want 2.0", fc.Slope)
	}
	if math.Abs(fc.Intercept-1.0) > 0.001 {
		t.Errorf("intercept = %f, want 1.0", fc.Intercept)
	}
	if math.Abs(fc.RSquared-1.0) > 0.001 {
		t.Errorf("r2 = %f, want 1.0", fc.RSquared)
	}
	// Next x=4 -> y=9, x=5 -> y=11
	if len(fc.Forecast) != 2 || math.Abs(fc.Forecast[0]-9.0) > 0.001 || math.Abs(fc.Forecast[1]-11.0) > 0.001 {
		t.Errorf("forecast = %v, want [9.0, 11.0]", fc.Forecast)
	}
}
