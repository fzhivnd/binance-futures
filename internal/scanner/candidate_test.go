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
	c := buildCandidate("BTCUSDT", -0.01, 50000.0, nil)

	if c.Symbol != "BTCUSDT" {
		t.Errorf("symbol mismatch: %s", c.Symbol)
	}
	if c.FundingRate != -0.01 {
		t.Errorf("funding rate mismatch: %v", c.FundingRate)
	}
	if c.MarkPrice != 50000.0 {
		t.Errorf("mark price mismatch: %v", c.MarkPrice)
	}
	// no candle provider → ROI fields are zero
	if c.ROI1D != 0 || c.DailyROI != 0 {
		t.Errorf("expected zero ROI with nil provider, got ROI1D=%v DailyROI=%v", c.ROI1D, c.DailyROI)
	}
}

func TestBuildCandidate_ScoreFromFunding(t *testing.T) {
	// funding -0.005 should produce a non-zero score
	c := buildCandidate("ETHUSDT", -0.005, 3000.0, nil)
	if c.Score <= 0 {
		t.Errorf("expected positive score for rate -0.005, got %v", c.Score)
	}
}

func TestBuildCandidate_NilProvider_ZeroVolume(t *testing.T) {
	c := buildCandidate("X", -0.01, 100.0, nil)
	if c.Volume24h != 0 {
		t.Errorf("expected zero volume with nil provider, got %v", c.Volume24h)
	}
}

func ptr(f float64) *float64 { return &f }
