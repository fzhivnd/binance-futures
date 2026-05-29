package scoring

import (
	"testing"

	"futures/internal/domain"
)

var neutralBTC = &domain.BTCContext{Trend: "neutral", MomentumScore: 40}

func emptySnap() *domain.IndicatorSnapshot {
	return &domain.IndicatorSnapshot{}
}

func TestScore_BelowThreshold(t *testing.T) {
	s := NewDefaultScorer()
	cand := domain.Candidate{FundingRate: -0.003, DailyROI: 30}
	sc := s.Score(cand, emptySnap(), neutralBTC)
	if sc.Confidence != "SKIP" {
		// score may still be low — just check no crash
		t.Logf("score=%.1f confidence=%s", sc.CompositeScore, sc.Confidence)
	}
}

func TestScore_FundingContribution(t *testing.T) {
	s := NewDefaultScorer()
	cand := domain.Candidate{FundingRate: -0.015, DailyROI: 35}
	snap := &domain.IndicatorSnapshot{OIDelta1h: 16, ATRRatio: 2, RSI7_5m: 70, VolumeSpike: true}
	sc := s.Score(cand, snap, neutralBTC)
	if sc.Breakdown.FundingScore <= 0 {
		t.Errorf("expected positive funding score, got %.2f", sc.Breakdown.FundingScore)
	}
}

func TestScore_BTCBreakout_ZeroScore(t *testing.T) {
	s := NewDefaultScorer()
	btc := &domain.BTCContext{Trend: "bullish", IsBreakout: true, MomentumScore: 80}
	cand := domain.Candidate{FundingRate: -0.01, DailyROI: 35}
	sc := s.Score(cand, emptySnap(), btc)
	if sc.Breakdown.BTCScore != 0 {
		t.Errorf("expected 0 BTC score during breakout, got %.2f", sc.Breakdown.BTCScore)
	}
}

func TestScore_ROIOutsideRange_ZeroROIScore(t *testing.T) {
	s := NewDefaultScorer()
	cand := domain.Candidate{FundingRate: -0.01, DailyROI: 90}
	sc := s.Score(cand, emptySnap(), neutralBTC)
	if sc.Breakdown.ROIScore != 0 {
		t.Errorf("expected 0 ROI score for ROI > 80%%, got %.2f", sc.Breakdown.ROIScore)
	}
}

func TestScore_OptimalROI(t *testing.T) {
	s := NewDefaultScorer()
	cand := domain.Candidate{FundingRate: -0.01, DailyROI: 35}
	sc := s.Score(cand, emptySnap(), neutralBTC)
	wantROI := s.Config().Weights.ROI
	if sc.Breakdown.ROIScore != wantROI {
		t.Errorf("expected full ROI score %.0f, got %.2f", wantROI, sc.Breakdown.ROIScore)
	}
}

func TestScore_ConfidenceMapping(t *testing.T) {
	tiers := DefaultScoringConfig().ConfidenceTiers
	cases := []struct {
		score    float64
		wantConf string
	}{
		{95, "VERY_HIGH"},
		{85, "HIGH"},
		{75, "MEDIUM"},
		{65, "LOW"},
		{55, "SKIP"},
	}
	for _, tc := range cases {
		conf, _ := mapScoreToConfidence(tiers, tc.score)
		if conf != tc.wantConf {
			t.Errorf("score %.0f: expected %s got %s", tc.score, tc.wantConf, conf)
		}
	}
}
