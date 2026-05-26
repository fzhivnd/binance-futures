package scoring

import (
	"futures/internal/domain"
)

// Scorer computes a composite score for a candidate.
type Scorer struct {
	weights WeightConfig
}

func NewScorer(weights WeightConfig) *Scorer {
	return &Scorer{weights: weights}
}

func NewDefaultScorer() *Scorer {
	return NewScorer(DefaultWeights)
}

// Score produces a ScoredCandidate by combining funding, indicators, and BTC context.
func (s *Scorer) Score(c domain.Candidate, ind *domain.IndicatorSnapshot, btc *domain.BTCContext) *domain.ScoredCandidate {
	var bd domain.ScoreBreakdown

	// 1. Funding (max 25): reuse Phase 1 mapping, normalized to 25-point scale
	rawFunding := mapFundingToScore(c.FundingRate)
	bd.FundingScore = rawFunding / 90 * s.weights.Funding

	// 2. OI (max 15)
	switch {
	case ind.OIDelta1h > 15:
		bd.OIScore = s.weights.OI
	case ind.OIDelta1h > 10:
		bd.OIScore = s.weights.OI * 0.75
	case ind.OIDelta1h > 5:
		bd.OIScore = s.weights.OI * 0.50
	case ind.OIDelta1h > 0:
		bd.OIScore = s.weights.OI * 0.30
	default:
		bd.OIScore = s.weights.OI * 0.13
	}

	// 3. BTC (max 10)
	if btc != nil {
		switch {
		case btc.IsBreakout && btc.Trend == "bullish":
			bd.BTCScore = 0
		case btc.Trend == "bullish" && btc.MomentumScore > 70:
			bd.BTCScore = s.weights.BTC * 0.2
		case btc.Trend == "bullish":
			bd.BTCScore = s.weights.BTC * 0.3
		case btc.Trend == "neutral":
			bd.BTCScore = s.weights.BTC * 0.7
		case btc.Trend == "bearish":
			bd.BTCScore = s.weights.BTC
		}
	} else {
		bd.BTCScore = s.weights.BTC * 0.7
	}

	// 4. Candle (max 20)
	bd.CandleScore = scoreCandlePatterns(ind.Patterns, s.weights.Candle)

	// 5. Volume (max 10): spike + overbought RSI = exhaustion signal
	switch {
	case ind.VolumeSpike && ind.RSI14_1h > 60:
		bd.VolumeScore = s.weights.Volume
	case ind.VolumeSpike:
		bd.VolumeScore = s.weights.Volume * 0.5
	default:
		bd.VolumeScore = s.weights.Volume * 0.3
	}

	// 6. ROI (max 15): based on 24h price change
	switch {
	case c.DailyROI >= 20 && c.DailyROI <= 50:
		bd.ROIScore = s.weights.ROI
	case c.DailyROI > 50 && c.DailyROI <= 80:
		bd.ROIScore = s.weights.ROI * 0.47
	default:
		bd.ROIScore = 0
	}

	// 7. Volatility (max 5)
	switch {
	case ind.ATRRatio >= 1 && ind.ATRRatio <= 3:
		bd.VolatilityScore = s.weights.Volatility
	case ind.ATRRatio > 3 && ind.ATRRatio <= 5:
		bd.VolatilityScore = s.weights.Volatility * 0.6
	default:
		bd.VolatilityScore = 0
	}

	composite := bd.FundingScore + bd.OIScore + bd.BTCScore +
		bd.CandleScore + bd.VolumeScore + bd.ROIScore + bd.VolatilityScore

	confidence, sizePct := mapScoreToConfidence(composite)

	return &domain.ScoredCandidate{
		Candidate:       c,
		Indicators:      ind,
		CompositeScore:  composite,
		Breakdown:       bd,
		Confidence:      confidence,
		PositionSizePct: sizePct,
	}
}

// mapFundingToScore replicates the Phase 1 funding-to-score mapping (0–90 scale).
func mapFundingToScore(rate float64) float64 {
	switch {
	case rate >= -0.002:
		return 0
	case rate >= -0.005:
		t := (rate - (-0.002)) / (-0.005 - (-0.002))
		return 40 + t*20
	case rate >= -0.01:
		t := (rate - (-0.005)) / (-0.01 - (-0.005))
		return 60 + t*20
	case rate > -0.02:
		t := (rate - (-0.01)) / (-0.02 - (-0.01))
		return 80 + t*10
	default:
		return 0
	}
}

// scoreCandlePatterns scores signals across timeframes, applying timeframe and strength weights.
func scoreCandlePatterns(signals []domain.CandleSignal, maxScore float64) float64 {
	if len(signals) == 0 {
		return 0
	}

	tfWeight := map[domain.Timeframe]float64{
		domain.Timeframe1h:  1.00,
		domain.Timeframe30m: 0.95,
		domain.Timeframe15m: 0.90,
		domain.Timeframe5m:  0.85,
	}
	strWeight := map[domain.PatternStrength]float64{
		domain.StrengthStrong: 1.0,
		domain.StrengthMedium: 0.6,
		domain.StrengthWeak:   0.3,
	}

	var rawScore float64
	tfHit := make(map[domain.Timeframe]bool)

	for _, sig := range signals {
		rawScore += tfWeight[sig.Timeframe] * strWeight[sig.Strength]
		tfHit[sig.Timeframe] = true
	}

	var bonus float64
	switch len(tfHit) {
	case 4:
		bonus = 0.4
	case 3:
		bonus = 0.25
	case 2:
		bonus = 0.1
	}

	// max possible raw score = 3.70 (one STRONG on each of 4 TFs: 1.00+0.95+0.90+0.85)
	normalized := rawScore / 3.70
	if normalized > 1.0 {
		normalized = 1.0
	}

	return (normalized + bonus) * maxScore
}
