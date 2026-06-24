package scanner

import (
	"testing"
)

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
	c := buildCandidate("ETHUSDT", -0.005, 3000.0, nil)
	if c.Score != -0.005 {
		t.Errorf("expected score equal to funding rate -0.005, got %v", c.Score)
	}
}

func TestBuildCandidate_NilProvider_ZeroVolume(t *testing.T) {
	c := buildCandidate("X", -0.01, 100.0, nil)
	if c.Volume24h != 0 {
		t.Errorf("expected zero volume with nil provider, got %v", c.Volume24h)
	}
}
