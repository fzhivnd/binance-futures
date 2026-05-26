package scanner

import (
	"math"
	"testing"
)

func TestMapFundingToScore(t *testing.T) {
	tests := []struct {
		rate      float64
		wantMin   float64
		wantMax   float64
		wantExact *float64
	}{
		{rate: 0.001, wantExact: ptr(0.0)},   // positive: no score
		{rate: -0.001, wantExact: ptr(0.0)},  // above -0.002: no score
		{rate: -0.002, wantExact: ptr(0.0)},  // boundary: excluded (rate >= -0.002)
		{rate: -0.003, wantMin: 40, wantMax: 60},
		{rate: -0.005, wantMin: 40, wantMax: 60},
		{rate: -0.007, wantMin: 60, wantMax: 80},
		{rate: -0.01, wantMin: 60, wantMax: 80},
		{rate: -0.015, wantMin: 80, wantMax: 90},
		{rate: -0.019, wantMin: 80, wantMax: 90},
		{rate: -0.02, wantExact: ptr(0.0)}, // hits default (not > -0.02)
		{rate: -0.05, wantExact: ptr(0.0)}, // too extreme: score 0
	}

	for _, tt := range tests {
		got := mapFundingToScore(tt.rate)
		if tt.wantExact != nil {
			if math.Abs(got-*tt.wantExact) > 1e-9 {
				t.Errorf("rate=%.4f: got %.4f, want %.4f", tt.rate, got, *tt.wantExact)
			}
			continue
		}
		if got < tt.wantMin || got > tt.wantMax {
			t.Errorf("rate=%.4f: score %.4f not in [%.0f, %.0f]", tt.rate, got, tt.wantMin, tt.wantMax)
		}
	}
}

func TestMapFundingToScore_Boundaries(t *testing.T) {
	// Verify monotonicity within each band
	prev := mapFundingToScore(-0.0025)
	for _, rate := range []float64{-0.003, -0.004, -0.005} {
		cur := mapFundingToScore(rate)
		if cur < prev {
			t.Errorf("score not monotonically increasing at rate=%.4f: %.4f < %.4f", rate, cur, prev)
		}
		prev = cur
	}
}

func TestBuildCandidate(t *testing.T) {
	c := buildCandidate("BTCUSDT", -0.01, 50000.0, 8, nil)

	if c.Symbol != "BTCUSDT" {
		t.Errorf("symbol mismatch: %s", c.Symbol)
	}
	if c.FundingRate != -0.01 {
		t.Errorf("funding rate mismatch: %v", c.FundingRate)
	}
	if c.MarkPrice != 50000.0 {
		t.Errorf("mark price mismatch: %v", c.MarkPrice)
	}

	// daily ROI: -(-0.01) * (24/8) * 100 = 3.0
	wantROI := 3.0
	if math.Abs(c.DailyROI-wantROI) > 1e-9 {
		t.Errorf("daily ROI: got %v, want %v", c.DailyROI, wantROI)
	}
}

func TestBuildCandidate_CustomInterval(t *testing.T) {
	// 4-hour funding interval: 6 payments/day
	c := buildCandidate("ETHUSDT", -0.005, 3000.0, 4, nil)
	wantROI := 0.005 * 6.0 * 100 // 3.0
	if math.Abs(c.DailyROI-wantROI) > 1e-9 {
		t.Errorf("daily ROI for 4h interval: got %v, want %v", c.DailyROI, wantROI)
	}
}

func TestBuildCandidate_ZeroInterval_DefaultsToEight(t *testing.T) {
	c1 := buildCandidate("X", -0.01, 100.0, 0, nil)
	c2 := buildCandidate("X", -0.01, 100.0, 8, nil)
	if c1.DailyROI != c2.DailyROI {
		t.Errorf("zero interval should default to 8h: got %v vs %v", c1.DailyROI, c2.DailyROI)
	}
}

func ptr(f float64) *float64 { return &f }
