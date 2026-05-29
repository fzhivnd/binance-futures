package scoring

import (
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

	composite := bd.FundingScore + bd.OIScore + bd.BTCScore +
		bd.CandleScore + bd.VolumeScore + bd.ROIScore + bd.VolatilityScore

	confidence, sizePct := mapScoreToConfidence(cfg.ConfidenceTiers, composite)

	return &domain.ScoredCandidate{
		Candidate:       c,
		Indicators:      ind,
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

	for _, sig := range signals {
		tw := tfWeights[string(sig.Timeframe)]
		sw := strWeights[string(sig.Strength)]
		rawScore += tw * sw
		tfHit[sig.Timeframe] = true
	}

	bonus := multitfBonus[len(tfHit)]

	// max possible raw: sum of all tf weights × STRONG (1.0)
	maxRaw := 0.0
	for _, w := range tfWeights {
		maxRaw += w
	}
	if maxRaw == 0 {
		return 0
	}

	normalized := rawScore / maxRaw
	if normalized > 1.0 {
		normalized = 1.0
	}

	return (normalized + bonus) * maxScore
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
