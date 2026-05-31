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
func DetectPatterns(candles []domain.Candle, tf domain.Timeframe, atr1h float64, avgVolume float64) []domain.CandleSignal {
	cfg, ok := tfConfigs[tf]
	if !ok {
		cfg = defaultConfig
	}

	if len(candles) < 3 {
		return nil
	}

	c := candles[len(candles)-1]
	p := candles[len(candles)-2]

	body := c.Close - c.Open
	upperWick := c.High - math.Max(c.Open, c.Close)
	lowerWick := math.Min(c.Open, c.Close) - c.Low
	candleRange := c.High - c.Low
	bodyPosition := (math.Max(c.Open, c.Close) - c.Low) / candleRange

	if candleRange == 0 {
		return nil
	}

	bodySize := math.Abs(body)

	// MinCandleRange filter: skip noise candles with tiny absolute moves
	if c.Close > 0 && candleRange/c.Close < cfg.MinCandleRange {
		return nil
	}

	// ATR-relative filter: pattern candle must be significant vs recent volatility.
	// Only applied when ATR is meaningful (atr1h > 0) and the candle is truly tiny
	// relative to expected moves — threshold lowered to 0.3 to avoid filtering out
	// valid patterns on low-volatility coins.
	if atr1h > 0 {
		tfATR := atr1h * cfg.ATRMultiplier
		if candleRange < tfATR*0.5 {
			return nil
		}
	}

	var signals []domain.CandleSignal

	// Shooting Star: small body at bottom, long upper wick, after uptrend
	if upperWick >= cfg.WickBodyRatio*bodySize &&
		lowerWick <= bodySize*0.5 &&
		bodyPosition > 0.65 &&
		body < 0 &&
		p.Close < c.High {
		if isUptrend(candles, cfg.TrendLookback) {
			sig := domain.CandleSignal{
				Timeframe: tf,
				Pattern:   domain.PatternShootingStar,
				Strength:  domain.StrengthStrong,
			}
			signals = append(signals, adjustStrength(sig, c.Volume, avgVolume))
		}
	}

	// Bearish Engulfing: current red body fully engulfs previous green body
	currBody := math.Abs(c.Close - c.Open)
	prevBody := math.Abs(p.Close - p.Open)
	if body < 0 &&
		prevBody > 0 &&
		currBody > prevBody*1.2 &&
		c.Close < p.Open {
		sig := domain.CandleSignal{
			Timeframe: tf,
			Pattern:   domain.PatternBearishEngulfing,
			Strength:  domain.StrengthStrong,
		}
		signals = append(signals, adjustStrength(sig, c.Volume, avgVolume))
	}

	// Upper Wick Rejection: wick > cfg.WickRejectRatio of range, bearish body
	if upperWick/candleRange > cfg.WickRejectRatio &&
		upperWick > lowerWick*2 &&
		body < 0 {
		sig := domain.CandleSignal{
			Timeframe: tf,
			Pattern:   domain.PatternUpperWickReject,
			Strength:  domain.StrengthStrong,
		}
		signals = append(signals, adjustStrength(sig, c.Volume, avgVolume))
	}

	// Evening Star: big green → small body → big red
	if len(candles) >= 3 {
		pp := candles[len(candles)-3]
		ppBody := pp.Close - pp.Open
		pBodySize := math.Abs(prevBody)
		if ppBody > 0 && body < 0 {
			avgBody := (math.Abs(ppBody) + math.Abs(body)) / 2
			midpoint := pp.Open + (pp.Close-pp.Open)/2
			if pBodySize < avgBody*cfg.EveningStarBody &&
				c.Close < midpoint {
				sig := domain.CandleSignal{
					Timeframe: tf,
					Pattern:   domain.PatternEveningStar,
					Strength:  domain.StrengthStrong,
				}
				signals = append(signals, adjustStrength(sig, c.Volume, avgVolume))
			}
		}
	}

	// Doji After Pump: strong green candle followed by doji-like current
	pRange := math.Abs(p.High - p.Low)
	if prevBody > 0 && pRange > 0 && prevBody/pRange > 0.7 && pRange > candleRange {
		if bodySize/candleRange < cfg.DojiThreshold {
			sig := domain.CandleSignal{
				Timeframe: tf,
				Pattern:   domain.PatternDojiAfterPump,
				Strength:  domain.StrengthMedium,
			}
			signals = append(signals, adjustStrength(sig, c.Volume, avgVolume))
		}
	}

	// Failed Breakout: made new high vs previous but closed below previous high
	if p.High > 0 {
		breakoutPct := (c.High - p.High) / p.High
		if breakoutPct > 0.002 &&
			c.Close < p.High &&
			body < 0 {
			sig := domain.CandleSignal{
				Timeframe: tf,
				Pattern:   domain.PatternFailedBreakout,
				Strength:  domain.StrengthMedium,
			}
			signals = append(signals, adjustStrength(sig, c.Volume, avgVolume))
		}
	}

	return signals
}

// isUptrend returns true if candles show a rising trend over the last lookback candles.
// It requires both a higher close and a majority of green candles.
func isUptrend(candles []domain.Candle, lookback int) bool {
	if len(candles) < lookback+1 {
		return false
	}
	start := candles[len(candles)-lookback-1]
	end := candles[len(candles)-2] // exclude the signal candle itself
	if start.Close <= 0 {
		return false
	}
	gainPct := (end.Close - start.Close) / start.Close
	if gainPct < 0.01 {
		return false
	}
	window := candles[len(candles)-lookback-1 : len(candles)-1]
	return countGreenCandles(window) >= lookback*2/3
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
