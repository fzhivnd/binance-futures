package scoring

import "futures/internal/domain"

// DefaultScoringConfig returns a domain.ScoringConfig matching the hardcoded DefaultWeights.
// Used as a last-resort reference; the app requires a DB config at startup.
func DefaultScoringConfig() *domain.ScoringConfig {
	minVal := func(v float64) *float64 { return &v }
	maxVal := func(v float64) *float64 { return &v }

	return &domain.ScoringConfig{
		Name:    "default",
		Version: 1,
		Weights: domain.ScoringWeights{
			Funding: 25, OI: 15, BTC: 10, Candle: 20, Volume: 10, ROI: 15, Volatility: 5,
		},
		Thresholds: []domain.ScoringThreshold{
			{Category: "oi", TierOrder: 1, MinValue: minVal(15), Multiplier: 1.00},
			{Category: "oi", TierOrder: 2, MinValue: minVal(10), MaxValue: maxVal(15), Multiplier: 0.75},
			{Category: "oi", TierOrder: 3, MinValue: minVal(5), MaxValue: maxVal(10), Multiplier: 0.50},
			{Category: "oi", TierOrder: 4, MinValue: minVal(0), MaxValue: maxVal(5), Multiplier: 0.30},
			{Category: "oi", TierOrder: 5, MaxValue: maxVal(0), Multiplier: 0.13},
			{Category: "roi", TierOrder: 1, MinValue: minVal(15), MaxValue: maxVal(50), Multiplier: 1.00},
			{Category: "roi", TierOrder: 2, MinValue: minVal(50), MaxValue: maxVal(80), Multiplier: 0.47},
			{Category: "roi", TierOrder: 3, Multiplier: 0.00},
			{Category: "volatility", TierOrder: 1, MinValue: minVal(1), MaxValue: maxVal(3), Multiplier: 1.00},
			{Category: "volatility", TierOrder: 2, MinValue: minVal(3), MaxValue: maxVal(5), Multiplier: 0.60},
			{Category: "volatility", TierOrder: 3, Multiplier: 0.00},
			{Category: "volume", TierOrder: 1, MinValue: minVal(65), Multiplier: 1.00},
			{Category: "volume", TierOrder: 2, MaxValue: maxVal(65), Multiplier: 0.50},
			{Category: "volume", TierOrder: 3, Multiplier: 0.30},
			{Category: "funding", TierOrder: 1, MinValue: minVal(-0.002), Multiplier: 0.00},
			{Category: "funding", TierOrder: 2, MinValue: minVal(-0.005), MaxValue: maxVal(-0.002), Multiplier: 0.67, Interpolate: true},
			{Category: "funding", TierOrder: 3, MinValue: minVal(-0.010), MaxValue: maxVal(-0.005), Multiplier: 0.89, Interpolate: true},
			{Category: "funding", TierOrder: 4, MinValue: minVal(-0.020), MaxValue: maxVal(-0.010), Multiplier: 1.00, Interpolate: true},
			{Category: "funding", TierOrder: 5, MaxValue: maxVal(-0.020), Multiplier: 0.00},
			{Category: "btc", TierOrder: 1, Multiplier: 0.00, Condition: "breakout_bullish"},
			{Category: "btc", TierOrder: 2, MinValue: minVal(70), Multiplier: 0.20, Condition: "bullish_high_momentum"},
			{Category: "btc", TierOrder: 3, Multiplier: 0.30, Condition: "bullish"},
			{Category: "btc", TierOrder: 4, Multiplier: 0.70, Condition: "neutral"},
			{Category: "btc", TierOrder: 5, Multiplier: 1.00, Condition: "bearish"},
		},
		CandleWeights: []domain.CandleWeight{
			{Type: "timeframe", Label: "1h", Weight: 1.00},
			{Type: "timeframe", Label: "30m", Weight: 0.95},
			{Type: "timeframe", Label: "15m", Weight: 0.90},
			{Type: "timeframe", Label: "5m", Weight: 0.85},
			{Type: "strength", Label: "STRONG", Weight: 1.00},
			{Type: "strength", Label: "MEDIUM", Weight: 0.70},
			{Type: "strength", Label: "WEAK", Weight: 0.35},
			{Type: "multitf_bonus", Label: "4", Weight: 0.40},
			{Type: "multitf_bonus", Label: "3", Weight: 0.25},
			{Type: "multitf_bonus", Label: "2", Weight: 0.10},
			{Type: "multitf_bonus", Label: "1", Weight: 0.00},
		},
		ConfidenceTiers: []domain.ConfidenceTier{
			{MinScore: 90, Confidence: "VERY_HIGH", PositionSizePct: 5.5},
			{MinScore: 80, Confidence: "HIGH", PositionSizePct: 5.0},
			{MinScore: 70, Confidence: "MEDIUM", PositionSizePct: 4.0},
			{MinScore: 60, Confidence: "LOW", PositionSizePct: 2.0},
			{MinScore: 0, Confidence: "SKIP", PositionSizePct: 0.0},
		},
		BotParams: domain.BotParams{
			MinDailyROIPct: 15.0, MinVolume24hM: 25.0,
			MaxPositions: 3, Leverage: 20, PositionSizePct: 5.0,
			FundingMinRate: -0.02, FundingMaxRate: -0.002,
			SlPct: 5.0, TpPct: 2.1, TrailingActivationPct: 1.5, BreakevenActivationPct: 1.5,
		},
	}
}
