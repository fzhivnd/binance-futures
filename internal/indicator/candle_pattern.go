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

	volumeConfirmed := avgVolume <= 0 ||
		c.Volume >= avgVolume*1.2

	var signals []domain.CandleSignal

	////////////////////////////////////////////////////
	// Shooting Star
	////////////////////////////////////////////////////

	bodyAtBottom :=
		(math.Max(c.Open, c.Close)-c.Low)/candleRange <= 0.4

	if upperWick >= bodySize*2 &&
		lowerWick <= bodySize &&
		bodyAtBottom &&
		isUptrend(candles, cfg.TrendLookback) {

		signals = append(
			signals,
			adjustStrength(domain.CandleSignal{
				Timeframe: tf,
				Pattern:   domain.PatternShootingStar,
				Strength:  domain.StrengthStrong,
			}, c.Volume, avgVolume),
		)
	}

	////////////////////////////////////////////////////
	// Bearish Engulfing
	////////////////////////////////////////////////////

	prevBody := math.Abs(p.Close - p.Open)
	prevRange := p.High - p.Low

	if body < 0 &&
		p.Close > p.Open &&
		prevRange > 0 &&
		prevBody/prevRange >= 0.4 &&
		isUptrend(candles, cfg.TrendLookback) {

		tolerance := candleRange * 0.05

		engulf :=
			c.Open >= p.Close-tolerance &&
				c.Close <= p.Open+tolerance

		if engulf && volumeConfirmed {
			signals = append(
				signals,
				adjustStrength(domain.CandleSignal{
					Timeframe: tf,
					Pattern:   domain.PatternBearishEngulfing,
					Strength:  domain.StrengthStrong,
				}, c.Volume, avgVolume),
			)
		}
	}

	////////////////////////////////////////////////////
	// Upper Wick Reject
	////////////////////////////////////////////////////

	if upperWick/candleRange >= 0.55 &&
		upperWick >= lowerWick*2 &&
		body <= 0 &&
		isUptrend(candles, cfg.TrendLookback) {

		signals = append(
			signals,
			adjustStrength(domain.CandleSignal{
				Timeframe: tf,
				Pattern:   domain.PatternUpperWickReject,
				Strength:  domain.StrengthMedium,
			}, c.Volume, avgVolume),
		)
	}

	////////////////////////////////////////////////////
	// Evening Star
	////////////////////////////////////////////////////

	ppBody := math.Abs(pp.Close - pp.Open)
	pBody := math.Abs(p.Close - p.Open)

	if pp.Close > pp.Open &&
		body < 0 &&
		isUptrend(candles[:len(candles)-1], cfg.TrendLookback) {

		firstBull :=
			pp.High > pp.Low &&
				ppBody/(pp.High-pp.Low) >= 0.5

		smallMiddle :=
			pBody <= ppBody*0.5

		reversal :=
			c.Close <= pp.Open+(ppBody*0.5)

		if firstBull &&
			smallMiddle &&
			reversal &&
			volumeConfirmed {

			signals = append(
				signals,
				adjustStrength(domain.CandleSignal{
					Timeframe: tf,
					Pattern:   domain.PatternEveningStar,
					Strength:  domain.StrengthStrong,
				}, c.Volume, avgVolume),
			)
		}
	}

	////////////////////////////////////////////////////
	// Doji After Pump
	////////////////////////////////////////////////////

	prevBody = math.Abs(p.Close - p.Open)
	prevRange = p.High - p.Low

	if prevRange > 0 &&
		p.Close > p.Open &&
		prevBody/prevRange >= 0.7 &&
		bodySize/candleRange <= cfg.DojiThreshold {

		signals = append(
			signals,
			adjustStrength(domain.CandleSignal{
				Timeframe: tf,
				Pattern:   domain.PatternDojiAfterPump,
				Strength:  domain.StrengthMedium,
			}, c.Volume, avgVolume),
		)
	}

	////////////////////////////////////////////////////
	// Failed Breakout
	////////////////////////////////////////////////////

	if p.High > 0 {
		breakoutPct := (c.High - p.High) / p.High

		if breakoutPct >= 0.002 &&
			c.Close < p.High &&
			body <= 0 {

			signals = append(
				signals,
				adjustStrength(domain.CandleSignal{
					Timeframe: tf,
					Pattern:   domain.PatternFailedBreakout,
					Strength:  domain.StrengthMedium,
				}, c.Volume, avgVolume),
			)
		}
	}

	////////////////////////////////////////////////////
	// Break Structure
	////////////////////////////////////////////////////

	if bearishBreakOfStructure(candles, 5) {
		signals = append(
			signals,
			adjustStrength(domain.CandleSignal{
				Timeframe: tf,
				Pattern:   domain.PatternBreakStructure,
				Strength:  domain.StrengthStrong,
			}, c.Volume, avgVolume),
		)
	}

	////////////////////////////////////////////////////
	// Liquidity Sweep
	////////////////////////////////////////////////////

	if bearishLiquiditySweep(candles) {
		signals = append(
			signals,
			adjustStrength(domain.CandleSignal{
				Timeframe: tf,
				Pattern:   domain.PatternLiquiditySweep,
				Strength:  domain.StrengthStrong,
			}, c.Volume, avgVolume),
		)
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

	volumeConfirmed := avgVolume <= 0 ||
		c.Volume >= avgVolume*1.2

	atrExpansion := tfATR <= 0 ||
		candleRange >= tfATR

	var signals []domain.CandleSignal

	////////////////////////////////////////////////////////////////////
	// Bullish Engulfing
	////////////////////////////////////////////////////////////////////

	prevBody := math.Abs(p.Close - p.Open)
	prevRange := p.High - p.Low

	if body > 0 &&
		p.Close < p.Open &&
		prevRange > 0 &&
		prevBody/prevRange >= 0.4 &&
		isDowntrend(candles, cfg.TrendLookback) {

		tolerance := candleRange * 0.05

		engulf :=
			c.Open <= p.Close+tolerance &&
				c.Close >= p.Open-tolerance

		if engulf && volumeConfirmed {
			signals = append(
				signals,
				adjustStrength(domain.CandleSignal{
					Timeframe: tf,
					Pattern:   domain.PatternBullishEngulfing,
					Strength:  domain.StrengthStrong,
				}, c.Volume, avgVolume),
			)
		}
	}

	////////////////////////////////////////////////////////////////////
	// Hammer
	////////////////////////////////////////////////////////////////////

	bodyAtTop :=
		(math.Max(c.Open, c.Close)-c.Low)/candleRange >= 0.6

	if body > 0 &&
		bodySize/candleRange <= 0.35 &&
		lowerWick >= bodySize*2 &&
		upperWick <= bodySize &&
		bodyAtTop &&
		isDowntrend(candles, cfg.TrendLookback) {

		signals = append(
			signals,
			adjustStrength(domain.CandleSignal{
				Timeframe: tf,
				Pattern:   domain.PatternHammer,
				Strength:  domain.StrengthStrong,
			}, c.Volume, avgVolume),
		)
	}

	////////////////////////////////////////////////////////////////////
	// Strong Momentum
	////////////////////////////////////////////////////////////////////

	if body > 0 &&
		bodySize/candleRange >= 0.7 &&
		upperWick/candleRange <= 0.1 &&
		c.Close > p.High &&
		volumeConfirmed &&
		atrExpansion {

		signals = append(
			signals,
			adjustStrength(domain.CandleSignal{
				Timeframe: tf,
				Pattern:   domain.PatternStrongMomentum,
				Strength:  domain.StrengthMedium,
			}, c.Volume, avgVolume),
		)
	}

	////////////////////////////////////////////////////////////////////
	// Morning Star
	////////////////////////////////////////////////////////////////////

	ppBody := math.Abs(pp.Close - pp.Open)
	pBody := math.Abs(p.Close - p.Open)

	if pp.Close < pp.Open &&
		body > 0 &&
		isDowntrend(candles[:len(candles)-1], cfg.TrendLookback) {

		firstBear :=
			pp.High > pp.Low &&
				ppBody/(pp.High-pp.Low) >= 0.5

		smallMiddle :=
			pBody <= ppBody*0.5

		recovery :=
			c.Close >= pp.Open-(ppBody*0.5)

		if firstBear &&
			smallMiddle &&
			recovery &&
			volumeConfirmed {

			signals = append(
				signals,
				adjustStrength(domain.CandleSignal{
					Timeframe: tf,
					Pattern:   domain.PatternMorningStar,
					Strength:  domain.StrengthStrong,
				}, c.Volume, avgVolume),
			)
		}
	}

	////////////////////////////////////////////////////////////////////
	// Break Of Structure
	////////////////////////////////////////////////////////////////////

	if bullishBreakOfStructure(candles, 5) {
		signals = append(
			signals,
			adjustStrength(domain.CandleSignal{
				Timeframe: tf,
				Pattern:   domain.PatternBreakOfStructure,
				Strength:  domain.StrengthStrong,
			}, c.Volume, avgVolume),
		)
	}

	////////////////////////////////////////////////////////////////////
	// Liquidity Sweep
	////////////////////////////////////////////////////////////////////

	if bullishLiquiditySweep(candles) {
		signals = append(
			signals,
			adjustStrength(domain.CandleSignal{
				Timeframe: tf,
				Pattern:   domain.PatternLiquiditySweep,
				Strength:  domain.StrengthStrong,
			}, c.Volume, avgVolume),
		)
	}

	return signals
}

// isDowntrend returns true if candles show a falling trend over the last lookback candles.
func isDowntrend(candles []domain.Candle, lookback int) bool {
	if len(candles) < lookback+1 {
		return false
	}

	start := len(candles) - lookback - 1
	end := len(candles) - 2

	lowerHighs := 0
	lowerLows := 0
	redCandles := 0

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

	first := candles[start]
	last := candles[end]

	if first.Close <= 0 {
		return false
	}

	dropPct := (first.Close - last.Close) / first.Close

	return dropPct >= 0.01 &&
		lowerHighs >= lookback/2 &&
		lowerLows >= lookback/2 &&
		redCandles >= lookback/2
}

// isUptrend returns true if candles show a rising trend over the last lookback candles.
// It requires both a higher close and a majority of green candles.
func isUptrend(candles []domain.Candle, lookback int) bool {
	if len(candles) < lookback+1 {
		return false
	}

	start := len(candles) - lookback - 1
	end := len(candles) - 2

	higherHighs := 0
	higherLows := 0
	greenCandles := 0

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

	first := candles[start]
	last := candles[end]

	if first.Close <= 0 {
		return false
	}

	gainPct := (last.Close - first.Close) / first.Close

	return gainPct >= 0.01 &&
		higherHighs >= lookback/2 &&
		higherLows >= lookback/2 &&
		greenCandles >= lookback/2
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

	highest := candles[len(candles)-lookback-1].High

	for i := len(candles) - lookback; i < len(candles)-1; i++ {
		if candles[i].High > highest {
			highest = candles[i].High
		}
	}

	return c.Close > highest
}

func bullishLiquiditySweep(candles []domain.Candle) bool {
	if len(candles) < 5 {
		return false
	}

	c := candles[len(candles)-1]

	lowest := candles[len(candles)-5].Low

	for i := len(candles) - 4; i < len(candles)-1; i++ {
		if candles[i].Low < lowest {
			lowest = candles[i].Low
		}
	}

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

	lowest := candles[len(candles)-lookback-1].Low

	for i := len(candles) - lookback; i < len(candles)-1; i++ {
		if candles[i].Low < lowest {
			lowest = candles[i].Low
		}
	}

	return c.Close < lowest
}

func bearishLiquiditySweep(candles []domain.Candle) bool {
	if len(candles) < 5 {
		return false
	}

	c := candles[len(candles)-1]

	highest := candles[len(candles)-5].High

	for i := len(candles) - 4; i < len(candles)-1; i++ {
		if candles[i].High > highest {
			highest = candles[i].High
		}
	}

	return c.High > highest &&
		c.Close < highest &&
		c.Close < c.Open
}
