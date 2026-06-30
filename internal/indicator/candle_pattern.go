package indicator

import (
	"math"

	"futures/internal/domain"
)

// PatternConfig holds timeframe-specific thresholds for pattern detection.
type PatternConfig struct {
	WickBodyRatio   float64 // shooting star: upperWick >= N * bodySize
	WickRejectRatio float64 // upper wick rejection: wick/range > N
	EveningStarBody float64 // middle candle max body fraction of avg
	DojiThreshold   float64 // max body/range for doji
	TrendLookback   int     // candles to confirm trend direction
	MinCandleRange  float64 // min range as fraction of price (noise filter)
	ATRMultiplier   float64 // scale from 1h ATR to this TF's ATR
}

var tfConfigs = map[domain.Timeframe]PatternConfig{
	domain.Timeframe1h: {
		WickBodyRatio:   2.5,
		WickRejectRatio: 0.60,
		EveningStarBody: 0.25,
		DojiThreshold:   0.08,
		TrendLookback:   5,
		MinCandleRange:  0.0020,
		ATRMultiplier:   1.0,
	},
	domain.Timeframe30m: {
		WickBodyRatio:   2.8,
		WickRejectRatio: 0.62,
		EveningStarBody: 0.25,
		DojiThreshold:   0.08,
		TrendLookback:   6,
		MinCandleRange:  0.0025,
		ATRMultiplier:   0.75,
	},
	domain.Timeframe15m: {
		WickBodyRatio:   3.0,
		WickRejectRatio: 0.65,
		EveningStarBody: 0.22,
		DojiThreshold:   0.07,
		TrendLookback:   8,
		MinCandleRange:  0.0030,
		ATRMultiplier:   0.55,
	},
	domain.Timeframe5m: {
		WickBodyRatio:   3.5,
		WickRejectRatio: 0.70,
		EveningStarBody: 0.20,
		DojiThreshold:   0.06,
		TrendLookback:   12,
		MinCandleRange:  0.0040,
		ATRMultiplier:   0.35,
	},
}

// defaultConfig is used when a timeframe isn't explicitly configured.
var defaultConfig = tfConfigs[domain.Timeframe1h]

// DetectPatterns analyzes candles for bearish reversal patterns.
// atr1h is the 14-period ATR on 1h; it will be scaled per timeframe. Pass 0 to skip ATR filtering.
// avgVolume is the average volume over the last ~20 candles; pass 0 to skip volume-based strength adjustment.
func DetectPatterns(
	candles []domain.Candle,
	tf domain.Timeframe,
	atr1h float64,
	avgVolume float64,
) []domain.CandleSignal {
	cfg, ok := tfConfigs[tf]
	if !ok {
		cfg = defaultConfig
	}

	if len(candles) < 5 {
		return nil
	}

	c := candles[len(candles)-1]
	p := candles[len(candles)-2]
	pp := candles[len(candles)-3]

	body := c.Close - c.Open
	bodySize := math.Abs(body)
	candleRange := c.High - c.Low

	if candleRange <= 0 {
		return nil
	}

	upperWick := c.High - math.Max(c.Open, c.Close)
	lowerWick := math.Min(c.Open, c.Close) - c.Low

	if c.Close > 0 &&
		candleRange/c.Close < cfg.MinCandleRange {
		return nil
	}

	// volume above average strengthens signals but doesn't gate them
	highVolume := avgVolume > 0 && c.Volume >= avgVolume*1.2

	var signals []domain.CandleSignal

	////////////////////////////////////////////////////
	// Shooting Star
	// Classic: long upper wick (≥2× body), small lower wick, body at bottom,
	// confirmed by uptrend. Use cfg.WickBodyRatio.
	////////////////////////////////////////////////////

	bodyAtBottom := (math.Max(c.Open, c.Close)-c.Low)/candleRange <= 0.35

	if upperWick >= bodySize*cfg.WickBodyRatio &&
		upperWick/candleRange >= 0.55 &&
		lowerWick <= bodySize*0.5 &&
		bodyAtBottom &&
		isUptrend(candles, cfg.TrendLookback) {

		str := domain.StrengthMedium
		if highVolume || body < 0 {
			str = domain.StrengthStrong
		}
		signals = append(signals, domain.CandleSignal{
			Timeframe: tf, Pattern: domain.PatternShootingStar, Strength: str,
		})
	}

	////////////////////////////////////////////////////
	// Bearish Engulfing
	// Current red candle fully engulfs prior green candle body.
	////////////////////////////////////////////////////

	prevBody := math.Abs(p.Close - p.Open)
	prevRange := p.High - p.Low

	if body < 0 &&
		p.Close > p.Open &&
		prevRange > 0 &&
		prevBody/prevRange >= 0.35 &&
		isUptrend(candles, cfg.TrendLookback) {

		// c.Open at or above p.Close, c.Close at or below p.Open
		tolerance := candleRange * 0.03
		engulf := c.Open >= p.Close-tolerance && c.Close <= p.Open+tolerance

		if engulf {
			str := domain.StrengthMedium
			if bodySize >= prevBody && highVolume {
				str = domain.StrengthStrong
			} else if bodySize >= prevBody*1.2 {
				str = domain.StrengthStrong
			}
			signals = append(signals, domain.CandleSignal{
				Timeframe: tf, Pattern: domain.PatternBearishEngulfing, Strength: str,
			})
		}
	}

	////////////////////////////////////////////////////
	// Upper Wick Reject
	// Large upper wick rejection — works on both red and small green bodies.
	////////////////////////////////////////////////////

	if upperWick/candleRange >= cfg.WickRejectRatio &&
		upperWick >= lowerWick*2.5 &&
		bodySize/candleRange <= 0.35 &&
		isUptrend(candles, cfg.TrendLookback) {

		signals = append(signals, adjustStrength(domain.CandleSignal{
			Timeframe: tf, Pattern: domain.PatternUpperWickReject, Strength: domain.StrengthMedium,
		}, c.Volume, avgVolume))
	}

	////////////////////////////////////////////////////
	// Evening Star
	// 3-candle: strong bull, small-body middle gapping up, bearish close back into first body.
	////////////////////////////////////////////////////

	ppBody := math.Abs(pp.Close - pp.Open)
	pBody := math.Abs(p.Close - p.Open)
	ppRange := pp.High - pp.Low

	if pp.Close > pp.Open &&
		ppRange > 0 &&
		ppBody/ppRange >= 0.5 &&
		body < 0 &&
		isUptrend(candles[:len(candles)-2], cfg.TrendLookback) {

		// Middle candle is small relative to first bull body
		smallMiddle := pBody <= ppBody*0.45

		// Middle trades near or above first candle's top (gap up or high close)
		middleHigh := math.Max(p.Open, p.Close) >= pp.Close-ppBody*0.1

		// Bearish candle closes back into at least half of first bull body
		reversal := c.Close <= pp.Open+(ppBody*0.5)

		if smallMiddle && middleHigh && reversal {
			str := domain.StrengthMedium
			if c.Close <= pp.Open+(ppBody*0.3) || highVolume {
				str = domain.StrengthStrong
			}
			signals = append(signals, domain.CandleSignal{
				Timeframe: tf, Pattern: domain.PatternEveningStar, Strength: str,
			})
		}
	}

	////////////////////////////////////////////////////
	// Doji After Pump
	// Prior candle strong green, current candle near-doji — indecision after push.
	////////////////////////////////////////////////////

	prevBody = math.Abs(p.Close - p.Open)
	prevRange = p.High - p.Low

	if prevRange > 0 &&
		p.Close > p.Open &&
		prevBody/prevRange >= 0.65 &&
		bodySize/candleRange <= cfg.DojiThreshold &&
		isUptrend(candles, cfg.TrendLookback) {

		signals = append(signals, adjustStrength(domain.CandleSignal{
			Timeframe: tf, Pattern: domain.PatternDojiAfterPump, Strength: domain.StrengthMedium,
		}, c.Volume, avgVolume))
	}

	////////////////////////////////////////////////////
	// Failed Breakout
	// Candle wicks above prior high then closes back below it — bull trap.
	// Use stricter threshold to avoid noise; require meaningful breakout attempt.
	////////////////////////////////////////////////////

	if p.High > 0 {
		breakoutPct := (c.High - p.High) / p.High

		if breakoutPct >= 0.004 &&
			c.Close < p.High &&
			c.Close < c.Open &&
			upperWick/candleRange >= 0.40 {

			signals = append(signals, adjustStrength(domain.CandleSignal{
				Timeframe: tf, Pattern: domain.PatternFailedBreakout, Strength: domain.StrengthMedium,
			}, c.Volume, avgVolume))
		}
	}

	////////////////////////////////////////////////////
	// Break Structure
	////////////////////////////////////////////////////

	if bearishBreakOfStructure(candles, cfg.TrendLookback) {
		signals = append(signals, adjustStrength(domain.CandleSignal{
			Timeframe: tf, Pattern: domain.PatternBreakStructure, Strength: domain.StrengthStrong,
		}, c.Volume, avgVolume))
	}

	////////////////////////////////////////////////////
	// Liquidity Sweep
	////////////////////////////////////////////////////

	if bearishLiquiditySweep(candles, cfg.TrendLookback) {
		signals = append(signals, adjustStrength(domain.CandleSignal{
			Timeframe: tf, Pattern: domain.PatternLiquiditySweep, Strength: domain.StrengthStrong,
		}, c.Volume, avgVolume))
	}

	return signals
}

// DetectBullishPatterns detects strong bullish candle patterns on higher timeframes (1h, 30m).
// These are used as penalty signals — their presence means a short is fighting the momentum.
// Only called for 1h and 30m; 5m/15m noise would produce too many false positives.
func DetectBullishPatterns(
	candles []domain.Candle,
	tf domain.Timeframe,
	atr1h float64,
	avgVolume float64,
) []domain.CandleSignal {
	cfg, ok := tfConfigs[tf]
	if !ok {
		cfg = defaultConfig
	}

	if len(candles) < 5 {
		return nil
	}

	c := candles[len(candles)-1]
	p := candles[len(candles)-2]
	pp := candles[len(candles)-3]

	body := c.Close - c.Open
	bodySize := math.Abs(body)
	candleRange := c.High - c.Low

	if candleRange <= 0 {
		return nil
	}

	upperWick := c.High - math.Max(c.Open, c.Close)
	lowerWick := math.Min(c.Open, c.Close) - c.Low

	if c.Close > 0 &&
		candleRange/c.Close < cfg.MinCandleRange {
		return nil
	}

	var tfATR float64
	if atr1h > 0 {
		tfATR = atr1h * cfg.ATRMultiplier
	}

	// volume above average strengthens signals but doesn't gate them
	highVolume := avgVolume > 0 && c.Volume >= avgVolume*1.2

	atrExpansion := tfATR <= 0 || candleRange >= tfATR

	var signals []domain.CandleSignal

	////////////////////////////////////////////////////////////////////
	// Bullish Engulfing
	// Current green candle fully engulfs prior red candle body.
	////////////////////////////////////////////////////////////////////

	prevBody := math.Abs(p.Close - p.Open)
	prevRange := p.High - p.Low

	if body > 0 &&
		p.Close < p.Open &&
		prevRange > 0 &&
		prevBody/prevRange >= 0.35 &&
		isDowntrend(candles, cfg.TrendLookback) {

		tolerance := candleRange * 0.03
		engulf := c.Open <= p.Close+tolerance && c.Close >= p.Open-tolerance

		if engulf {
			str := domain.StrengthMedium
			if bodySize >= prevBody && highVolume {
				str = domain.StrengthStrong
			} else if bodySize >= prevBody*1.2 {
				str = domain.StrengthStrong
			}
			signals = append(signals, domain.CandleSignal{
				Timeframe: tf, Pattern: domain.PatternBullishEngulfing, Strength: str,
			})
		}
	}

	////////////////////////////////////////////////////////////////////
	// Hammer
	// Long lower wick, small body at the top, little upper wick.
	////////////////////////////////////////////////////////////////////

	bodyPos := (math.Max(c.Open, c.Close) - c.Low) / candleRange

	isHammer :=
		bodyPos >= 0.60 &&
			lowerWick >= bodySize*cfg.WickBodyRatio &&
			upperWick <= candleRange*0.25 &&
			bodySize/candleRange <= 0.45

	if isHammer && isDowntrend(candles, cfg.TrendLookback) {
		str := domain.StrengthMedium
		if highVolume || body > 0 {
			str = domain.StrengthStrong
		}
		signals = append(signals, domain.CandleSignal{
			Timeframe: tf, Pattern: domain.PatternHammer, Strength: str,
		})
	}

	////////////////////////////////////////////////////////////////////
	// Strong Momentum
	// Large green body, closes near high — volume or ATR expansion required.
	////////////////////////////////////////////////////////////////////

	if body > 0 &&
		bodySize/candleRange >= 0.60 &&
		upperWick/candleRange <= 0.20 &&
		c.Close >= c.High-candleRange*0.15 &&
		(highVolume || atrExpansion) {

		signals = append(signals, adjustStrength(domain.CandleSignal{
			Timeframe: tf, Pattern: domain.PatternStrongMomentum, Strength: domain.StrengthMedium,
		}, c.Volume, avgVolume))
	}

	////////////////////////////////////////////////////////////////////
	// Morning Star
	// 3-candle: strong bear, small-body middle near the low, bullish recovery.
	////////////////////////////////////////////////////////////////////

	ppBody := math.Abs(pp.Close - pp.Open)
	pBody := math.Abs(p.Close - p.Open)
	ppRange := pp.High - pp.Low

	if pp.Close < pp.Open &&
		ppRange > 0 &&
		ppBody/ppRange >= 0.5 &&
		body > 0 &&
		isDowntrend(candles[:len(candles)-2], cfg.TrendLookback) {

		smallMiddle := pBody <= ppBody*0.45

		// Middle trades near or below first candle's low (gap down or low close)
		middleLow := math.Min(p.Open, p.Close) <= pp.Close+ppBody*0.1

		// Bullish candle recovers at least half of the first bear body
		recovery := c.Close >= pp.Close+(ppBody*0.5)

		if smallMiddle && middleLow && recovery {
			str := domain.StrengthMedium
			if c.Close >= pp.Close+(ppBody*0.7) || highVolume {
				str = domain.StrengthStrong
			}
			signals = append(signals, domain.CandleSignal{
				Timeframe: tf, Pattern: domain.PatternMorningStar, Strength: str,
			})
		}
	}

	////////////////////////////////////////////////////////////////////
	// Break Of Structure
	////////////////////////////////////////////////////////////////////

	if bullishBreakOfStructure(candles, cfg.TrendLookback) {
		signals = append(signals, adjustStrength(domain.CandleSignal{
			Timeframe: tf, Pattern: domain.PatternBreakOfStructure, Strength: domain.StrengthStrong,
		}, c.Volume, avgVolume))
	}

	////////////////////////////////////////////////////////////////////
	// Liquidity Sweep
	////////////////////////////////////////////////////////////////////

	if bullishLiquiditySweep(candles, cfg.TrendLookback) {
		signals = append(signals, adjustStrength(domain.CandleSignal{
			Timeframe: tf, Pattern: domain.PatternLiquiditySweep, Strength: domain.StrengthStrong,
		}, c.Volume, avgVolume))
	}

	////////////////////////////////////////////////////////////////////
	// Lower Wick Reject
	// Large lower wick rejection — works on both green and small-body doji candles.
	////////////////////////////////////////////////////////////////////

	if lowerWick/candleRange >= cfg.WickRejectRatio &&
		lowerWick >= upperWick*2.5 &&
		bodySize/candleRange <= 0.35 &&
		isDowntrend(candles, cfg.TrendLookback) {

		signals = append(signals, adjustStrength(domain.CandleSignal{
			Timeframe: tf, Pattern: domain.PatternLowerWickReject, Strength: domain.StrengthMedium,
		}, c.Volume, avgVolume))
	}

	return signals
}

// isDowntrend returns true if candles show a falling trend over the last lookback candles.
// Uses a scoring approach: price drop + majority of lower-highs or lower-lows or red candles.
func isDowntrend(candles []domain.Candle, lookback int) bool {
	if len(candles) < lookback+1 {
		return false
	}

	start := len(candles) - lookback - 1
	end := len(candles) - 2

	first := candles[start]
	last := candles[end]

	if first.Close <= 0 {
		return false
	}

	dropPct := (first.Close - last.Close) / first.Close
	if dropPct < 0.005 {
		return false
	}

	lowerHighs := 0
	lowerLows := 0
	redCandles := 0
	n := end - start

	for i := start + 1; i <= end; i++ {
		if candles[i].High < candles[i-1].High {
			lowerHighs++
		}
		if candles[i].Low < candles[i-1].Low {
			lowerLows++
		}
		if candles[i].Close < candles[i].Open {
			redCandles++
		}
	}

	threshold := n / 3
	if threshold < 1 {
		threshold = 1
	}
	conditions := 0
	if lowerHighs >= threshold {
		conditions++
	}
	if lowerLows >= threshold {
		conditions++
	}
	if redCandles >= n/2+1 {
		conditions++
	}

	return conditions >= 2
}

// isUptrend returns true if candles show a rising trend over the last lookback candles.
// Uses a scoring approach: price gain + majority of higher-highs or higher-lows or green candles.
func isUptrend(candles []domain.Candle, lookback int) bool {
	if len(candles) < lookback+1 {
		return false
	}

	start := len(candles) - lookback - 1
	end := len(candles) - 2

	first := candles[start]
	last := candles[end]

	if first.Close <= 0 {
		return false
	}

	gainPct := (last.Close - first.Close) / first.Close
	if gainPct < 0.005 {
		return false
	}

	higherHighs := 0
	higherLows := 0
	greenCandles := 0
	n := end - start

	for i := start + 1; i <= end; i++ {
		if candles[i].High > candles[i-1].High {
			higherHighs++
		}
		if candles[i].Low > candles[i-1].Low {
			higherLows++
		}
		if candles[i].Close > candles[i].Open {
			greenCandles++
		}
	}

	// At least 2 of 3 structural conditions must be met (majority threshold)
	threshold := n / 3
	if threshold < 1 {
		threshold = 1
	}
	conditions := 0
	if higherHighs >= threshold {
		conditions++
	}
	if higherLows >= threshold {
		conditions++
	}
	if greenCandles >= n/2+1 {
		conditions++
	}

	return conditions >= 2
}

func countGreenCandles(candles []domain.Candle) int {
	n := 0
	for _, c := range candles {
		if c.Close > c.Open {
			n++
		}
	}
	return n
}

// adjustStrength upgrades or downgrades a signal's strength based on volume ratio.
func adjustStrength(sig domain.CandleSignal, volume float64, avgVolume float64) domain.CandleSignal {
	if avgVolume <= 0 || volume <= 0 {
		return sig
	}
	ratio := volume / avgVolume
	if ratio > 1.5 && sig.Strength == domain.StrengthMedium {
		sig.Strength = domain.StrengthStrong
	} else if ratio < 0.7 && sig.Strength == domain.StrengthStrong {
		sig.Strength = domain.StrengthMedium
	}
	return sig
}

func bullishBreakOfStructure(
	candles []domain.Candle,
	lookback int,
) bool {
	if len(candles) < lookback+2 {
		return false
	}

	c := candles[len(candles)-1]

	// Find the highest high over the lookback window (excluding last candle)
	start := len(candles) - lookback - 1
	highest := candles[start].High
	for i := start + 1; i < len(candles)-1; i++ {
		if candles[i].High > highest {
			highest = candles[i].High
		}
	}

	// Close must clearly break above the structure high
	return c.Close > highest && c.Close > c.Open
}

// bullishLiquiditySweep detects a sweep of a prior swing low followed by recovery:
// candle wicks below the recent swing low but closes back above it bullishly.
func bullishLiquiditySweep(candles []domain.Candle, lookback int) bool {
	if len(candles) < lookback+2 {
		return false
	}

	c := candles[len(candles)-1]

	// Find the lowest low over the lookback window (excluding last candle)
	start := len(candles) - lookback - 1
	lowest := candles[start].Low
	for i := start + 1; i < len(candles)-1; i++ {
		if candles[i].Low < lowest {
			lowest = candles[i].Low
		}
	}

	// Wick below the swing low, then close back above it — bullish recovery
	return c.Low < lowest &&
		c.Close > lowest &&
		c.Close > c.Open
}

func bearishBreakOfStructure(
	candles []domain.Candle,
	lookback int,
) bool {
	if len(candles) < lookback+2 {
		return false
	}

	c := candles[len(candles)-1]

	// Find the lowest low over the lookback window (excluding last candle)
	start := len(candles) - lookback - 1
	lowest := candles[start].Low
	for i := start + 1; i < len(candles)-1; i++ {
		if candles[i].Low < lowest {
			lowest = candles[i].Low
		}
	}

	// Close must clearly break below the structure low
	return c.Close < lowest && c.Close < c.Open
}

// bearishLiquiditySweep detects a sweep of a prior swing high followed by rejection:
// candle wicks above the recent swing high but closes back below it bearishly.
func bearishLiquiditySweep(candles []domain.Candle, lookback int) bool {
	if len(candles) < lookback+2 {
		return false
	}

	c := candles[len(candles)-1]

	// Find the highest high over the lookback window (excluding last candle)
	start := len(candles) - lookback - 1
	highest := candles[start].High
	for i := start + 1; i < len(candles)-1; i++ {
		if candles[i].High > highest {
			highest = candles[i].High
		}
	}

	// Wick above the swing high, then close back below it — bearish rejection
	return c.High > highest &&
		c.Close < highest &&
		c.Close < c.Open
}
