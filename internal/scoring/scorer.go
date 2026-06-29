package scoring

import (
	"math"
	"sort"
	"sync"

	"futures/internal/domain"
)

type Scorer struct {
	mu  sync.RWMutex
	cfg *domain.ScoringConfig
}

func NewScorerFromConfig(cfg *domain.ScoringConfig) *Scorer {
	return &Scorer{cfg: cfg}
}

func NewDefaultScorer() *Scorer {
	return &Scorer{cfg: DefaultScoringConfig()}
}

var bearishPatternWeight = map[domain.CandlePattern]float64{
	domain.PatternUpperWickReject:  2.0,
	domain.PatternDojiAfterPump:    1.5,
	domain.PatternFailedBreakout:   3.0,
	domain.PatternShootingStar:     3.0,
	domain.PatternBearishEngulfing: 4.0,
	domain.PatternEveningStar:      5.0,
	domain.PatternBreakStructure:   6.0,
	domain.PatternLiquiditySweep:   6.0,
}

var bullishPatternWeight = map[domain.CandlePattern]float64{
	domain.PatternHammer:           0.5,
	domain.PatternStrongMomentum:   1.0,
	domain.PatternBullishEngulfing: 1.5,
	domain.PatternMorningStar:      2.0,
	domain.PatternBreakOfStructure: 6.0,
	domain.PatternLiquiditySweep:   6.0,
	domain.PatternLowerWickReject:  1.0,
}

func (s *Scorer) UpdateConfig(cfg *domain.ScoringConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
}

func (s *Scorer) Config() *domain.ScoringConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Score produces a ScoredCandidate by combining funding, indicators, and BTC context.
func (s *Scorer) Score(c domain.Candidate, ind *domain.IndicatorSnapshot, btc *domain.BTCContext) *domain.ScoredCandidate {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()

	tfWeights, strWeights, multitfBonus := buildCandleMaps(cfg.CandleWeights)

	var bd domain.ScoreBreakdown

	// 1. Funding
	bd.FundingScore = evalInterpolated(cfg, "funding", c.FundingRate) * cfg.Weights.Funding

	// 2. OI
	bd.OIScore = evalThreshold(cfg, "oi", ind.OIDelta1h) * cfg.Weights.OI

	// 3. BTC
	bd.BTCScore = evalBTC(cfg, btc) * cfg.Weights.BTC

	// 4. Candle
	bd.CandleScore = scoreCandlePatterns(ind.Patterns, cfg.Weights.Candle, tfWeights, strWeights, multitfBonus)

	// 5. Volume: two-input check — VolumeSpike + RSI7_5m overbought
	bd.VolumeScore = evalVolume(cfg, ind.VolumeSpike, ind.RSI7_5m) * cfg.Weights.Volume

	// 6. ROI
	bd.ROIScore = evalThreshold(cfg, "roi", c.DailyROI) * cfg.Weights.ROI

	// 7. Volatility
	bd.VolatilityScore = evalThreshold(cfg, "volatility", ind.ATRRatio) * cfg.Weights.Volatility

	// 8. RSI Divergence
	bd.RSIDivergenceScore = evalRSIDivergence(ind.RSIDivergences) * cfg.Weights.RSIDivergence

	// 9. Bullish candle penalty: each strong bullish candle on 1h/30m costs half the candle weight;
	// medium costs a quarter. Capped at the full candle weight so we can't go doubly negative.
	bd.BullishCandlePenalty = scoreBullishPenalty(ind.BullishPatterns, cfg.Weights.Candle)

	// 10. Momentum penalty: applied to composite directly.
	//   BullishMomentum (squeeze risk): −20% of current composite signals.
	//   BearishMomentumWeak (stale setup): −10%.
	//   Both together: −28% (multiplicative).
	bd.MomentumPenalty = scoreMomentumPenalty(ind, bd.FundingScore+bd.OIScore+bd.BTCScore+bd.CandleScore+bd.VolumeScore+bd.ROIScore+bd.VolatilityScore+bd.RSIDivergenceScore+bd.BullishCandlePenalty)

	composite := bd.FundingScore + bd.OIScore + bd.BTCScore +
		bd.CandleScore + bd.VolumeScore + bd.ROIScore + bd.VolatilityScore + bd.RSIDivergenceScore +
		bd.BullishCandlePenalty + bd.MomentumPenalty

	confidence, sizePct := mapScoreToConfidence(cfg.ConfidenceTiers, composite)

	return &domain.ScoredCandidate{
		Candidate:       c,
		Indicators:      ind,
		BTCContext:      btc,
		CompositeScore:  composite,
		Breakdown:       bd,
		Confidence:      confidence,
		PositionSizePct: sizePct,
	}
}

// evalThreshold walks the tiers for a category and returns the multiplier for the first matching tier.
func evalThreshold(cfg *domain.ScoringConfig, category string, value float64) float64 {
	for _, t := range cfg.Thresholds {
		if t.Category != category {
			continue
		}
		if t.MinValue != nil && value < *t.MinValue {
			continue
		}
		if t.MaxValue != nil && value >= *t.MaxValue {
			continue
		}
		return t.Multiplier
	}
	return 0
}

// evalInterpolated handles tiers with linear interpolation (used by funding).
func evalInterpolated(cfg *domain.ScoringConfig, category string, value float64) float64 {
	for _, t := range cfg.Thresholds {
		if t.Category != category {
			continue
		}
		if t.MinValue != nil && value < *t.MinValue {
			continue
		}
		if t.MaxValue != nil && value >= *t.MaxValue {
			continue
		}
		if !t.Interpolate || t.MinValue == nil || t.MaxValue == nil {
			return t.Multiplier
		}
		// linear ramp within [minValue, maxValue)
		lo, hi := *t.MinValue, *t.MaxValue
		if hi == lo {
			return t.Multiplier
		}
		// find next tier's multiplier as the base
		prevMultiplier := 0.0
		for _, prev := range cfg.Thresholds {
			if prev.Category == category && prev.TierOrder == t.TierOrder-1 {
				prevMultiplier = prev.Multiplier
				break
			}
		}
		ratio := (value - lo) / (hi - lo)
		return prevMultiplier + ratio*(t.Multiplier-prevMultiplier)
	}
	return 0
}

// evalBTC resolves the BTC condition string and returns the matching multiplier.
func evalBTC(cfg *domain.ScoringConfig, btc *domain.BTCContext) float64 {
	var condition string
	if btc == nil {
		condition = "neutral"
	} else {
		switch {
		case btc.IsBreakout && btc.Trend == "bullish":
			condition = "breakout_bullish"
		case btc.Trend == "bullish" && btc.MomentumScore > 70:
			condition = "bullish_high_momentum"
		case btc.Trend == "bullish":
			condition = "bullish"
		case btc.Trend == "bearish":
			condition = "bearish"
		default:
			condition = "neutral"
		}
	}

	for _, t := range cfg.Thresholds {
		if t.Category == "btc" && t.Condition == condition {
			return t.Multiplier
		}
	}
	return 0.7 // safe default: neutral-equivalent
}

// evalVolume handles the two-input volume check (spike flag + RSI threshold).
func evalVolume(cfg *domain.ScoringConfig, spike bool, rsi float64) float64 {
	if !spike {
		return evalThreshold(cfg, "volume", -1) // matches the no-spike fallback tier
	}
	return evalThreshold(cfg, "volume", rsi)
}

func buildCandleMaps(weights []domain.CandleWeight) (
	tfWeights map[string]float64,
	strWeights map[string]float64,
	multitfBonus map[int]float64,
) {
	tfWeights = make(map[string]float64)
	strWeights = make(map[string]float64)
	multitfBonus = make(map[int]float64)

	for _, cw := range weights {
		switch cw.Type {
		case "timeframe":
			tfWeights[cw.Label] = cw.Weight
		case "strength":
			strWeights[cw.Label] = cw.Weight
		case "multitf_bonus":
			var n int
			for _, r := range cw.Label {
				n = n*10 + int(r-'0')
			}
			multitfBonus[n] = cw.Weight
		}
	}
	return
}

func scoreCandlePatterns(
	signals []domain.CandleSignal,
	maxScore float64,
	tfWeights map[string]float64,
	strWeights map[string]float64,
	multitfBonus map[int]float64,
) float64 {
	if len(signals) == 0 {
		return 0
	}

	var rawScore float64
	tfHit := make(map[domain.Timeframe]bool)

	hasFailedBreakout := false
	hasUpperWickReject := false
	hasLiquiditySweep := false
	hasBearishEngulfing := false

	for _, sig := range signals {
		pw := bearishPatternWeight[sig.Pattern]
		tw := tfWeights[string(sig.Timeframe)]
		sw := strWeights[string(sig.Strength)]

		rawScore += pw * tw * sw
		tfHit[sig.Timeframe] = true

		switch sig.Pattern {
		case domain.PatternFailedBreakout:
			hasFailedBreakout = true

		case domain.PatternUpperWickReject:
			hasUpperWickReject = true

		case domain.PatternLiquiditySweep:
			hasLiquiditySweep = true

		case domain.PatternBearishEngulfing:
			hasBearishEngulfing = true
		}
	}

	//////////////////////////////////////////////////
	// Synergy bonuses
	//////////////////////////////////////////////////

	if hasFailedBreakout && hasUpperWickReject {
		rawScore *= 1.20
	}

	if hasLiquiditySweep && hasBearishEngulfing {
		rawScore *= 1.20
	}

	//////////////////////////////////////////////////
	// Normalization
	//////////////////////////////////////////////////

	maxRaw := 0.0

	for tf := range tfHit {
		tw := tfWeights[string(tf)]

		// strongest possible pattern
		pw := 6.0

		// strongest strength
		sw := 1.5

		maxRaw += pw * tw * sw
	}

	if maxRaw == 0 {
		return 0
	}

	normalized := rawScore / maxRaw
	if normalized > 1 {
		normalized = 1
	}

	//////////////////////////////////////////////////
	// Multi-timeframe bonus
	//////////////////////////////////////////////////

	bonus := multitfBonus[len(tfHit)]

	score := (normalized + bonus) * maxScore

	if score > maxScore {
		score = maxScore
	}

	return score
}

// evalRSIDivergence resolves the strongest divergence condition across all detected
// divergences and returns the matching multiplier from thresholds.
func evalRSIDivergence(divs []domain.RSIDivergence) float64 {
	condition := rsidivCondition(divs)
	rsiThreshold := []domain.ScoringThreshold{
		// RSI divergence scoring tiers
		{Category: "rsi_divergence", TierOrder: 1, Multiplier: 1.00, Condition: "strong_multitf"},
		{Category: "rsi_divergence", TierOrder: 2, Multiplier: 0.80, Condition: "strong"},
		{Category: "rsi_divergence", TierOrder: 3, Multiplier: 0.60, Condition: "strong_multitf"},
		{Category: "rsi_divergence", TierOrder: 4, Multiplier: 0.40, Condition: "medium"},
		{Category: "rsi_divergence", TierOrder: 5, Multiplier: 0.20, Condition: "weak"},
		{Category: "rsi_divergence", TierOrder: 6, Multiplier: 0.00, Condition: "none"},
	}
	for _, t := range rsiThreshold {
		if t.Category == "rsi_divergence" && t.Condition == condition {
			return t.Multiplier
		}
	}
	return 0
}

// rsidivCondition maps a set of divergences to the highest-priority condition string.
func rsidivCondition(divs []domain.RSIDivergence) string {
	if len(divs) == 0 {
		return "none"
	}

	tfCount := make(map[domain.Timeframe]domain.PatternStrength)
	for _, d := range divs {
		if cur, ok := tfCount[d.Timeframe]; !ok || strengthRank(d.Strength) > strengthRank(cur) {
			tfCount[d.Timeframe] = d.Strength
		}
	}

	strongCount, mediumCount := 0, 0
	for _, s := range tfCount {
		switch s {
		case domain.StrengthStrong:
			strongCount++
		case domain.StrengthMedium:
			mediumCount++
		}
	}

	switch {
	case strongCount >= 2:
		return "strong_multitf"
	case strongCount == 1:
		return "strong"
	case mediumCount >= 2:
		return "medium_multitf"
	case mediumCount == 1:
		return "medium"
	default:
		return "weak"
	}
}

func strengthRank(s domain.PatternStrength) int {
	switch s {
	case domain.StrengthStrong:
		return 3
	case domain.StrengthMedium:
		return 2
	case domain.StrengthWeak:
		return 1
	}
	return 0
}

// scoreBullishPenalty returns a negative score for bullish candle signals on 1h/30m.
// Strong bullish pattern: costs 50% of candle weight. Medium: 25%. Capped at full candle weight.
func scoreBullishPenalty(
	signals []domain.CandleSignal,
	candleWeight float64,
) float64 {
	score := bullishSignalScore(signals)

	var penalty float64

	switch {
	case score >= 16:
		return -candleWeight

	case score >= 12:
		return -(candleWeight * 0.5)

	case score >= 8:
		return -(candleWeight * 0.25)

	default:
		return 0
	}

	return -penalty
}

// scoreMomentumPenalty returns a negative adjustment to composite based on momentum flags.
// BullishMomentum alone: −20%. BearishMomentumWeak alone: −10%. Both: −28% (multiplicative).
func scoreMomentumPenalty(ind *domain.IndicatorSnapshot, preComposite float64) float64 {
	if ind == nil || preComposite <= 0 {
		return 0
	}
	multiplier := 1.0
	if ind.BullishMomentum {
		multiplier *= 0.80
	}
	if ind.BearishMomentumWeak {
		multiplier *= 0.90
	}
	if multiplier == 1.0 {
		return 0
	}
	return preComposite*multiplier - preComposite // always negative
}

func mapScoreToConfidence(tiers []domain.ConfidenceTier, score float64) (string, float64) {
	sorted := make([]domain.ConfidenceTier, len(tiers))
	copy(sorted, tiers)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].MinScore > sorted[j].MinScore
	})
	for _, t := range sorted {
		if score >= t.MinScore {
			return t.Confidence, t.PositionSizePct
		}
	}
	return "SKIP", 0
}

func bullishSignalScore(
	signals []domain.CandleSignal,
) float64 {
	score := 0.0

	for _, sig := range signals {
		s := bullishPatternWeight[sig.Pattern]

		switch sig.Strength {
		case domain.StrengthStrong:
			score += s

		case domain.StrengthMedium:
			score += math.Ceil(float64(s) * 0.5)

		case domain.StrengthWeak:
			score += 1
		}
	}

	return score
}
