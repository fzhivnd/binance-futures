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
		WickBodyRatio:   2.0,
		WickRejectRatio: 0.60,
		EveningStarBody: 0.30,
		DojiThreshold:   0.10,
		TrendLookback:   4,
		MinCandleRange:  0.001,
		ATRMultiplier:   1.0,
	},
	domain.Timeframe30m: {
		WickBodyRatio:   2.2,
		WickRejectRatio: 0.55,
		EveningStarBody: 0.30,
		DojiThreshold:   0.10,
		TrendLookback:   5,
		MinCandleRange:  0.0015,
		ATRMultiplier:   0.70,
	},
	domain.Timeframe15m: {
		WickBodyRatio:   2.5,
		WickRejectRatio: 0.50,
		EveningStarBody: 0.28,
		DojiThreshold:   0.09,
		TrendLookback:   9,
		MinCandleRange:  0.002,
		ATRMultiplier:   0.50,
	},
	domain.Timeframe5m: {
		WickBodyRatio:   3.0,
		WickRejectRatio: 0.45,
		EveningStarBody: 0.25,
		DojiThreshold:   0.08,
		TrendLookback:   13,
		MinCandleRange:  0.003,
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

	if candleRange == 0 {
		return nil
	}

	bodySize := math.Abs(body)

	// MinCandleRange filter: skip noise candles with tiny absolute moves
	if c.Close > 0 && candleRange/c.Close < cfg.MinCandleRange {
		return nil
	}

	// ATR-relative filter: pattern candle must be significant vs recent volatility
	if atr1h > 0 {
		tfATR := atr1h * cfg.ATRMultiplier
		if candleRange < tfATR*0.5 {
			return nil
		}
	}

	var signals []domain.CandleSignal

	// Shooting Star: small body at bottom, long upper wick, after uptrend
	if upperWick >= cfg.WickBodyRatio*bodySize && lowerWick < bodySize && p.Close < c.High {
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
	prevBody := p.Close - p.Open
	if body < 0 && prevBody > 0 {
		if c.Open >= p.Close && c.Close <= p.Open {
			sig := domain.CandleSignal{
				Timeframe: tf,
				Pattern:   domain.PatternBearishEngulfing,
				Strength:  domain.StrengthStrong,
			}
			signals = append(signals, adjustStrength(sig, c.Volume, avgVolume))
		}
	}

	// Upper Wick Rejection: wick > cfg.WickRejectRatio of range, bearish body
	if upperWick/candleRange > cfg.WickRejectRatio && body < 0 {
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
			if pBodySize < avgBody*cfg.EveningStarBody {
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
	if prevBody > 0 && pRange > 0 && prevBody/pRange > 0.6 {
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
	if c.High > p.High && c.Close < p.High && body < 0 {
		sig := domain.CandleSignal{
			Timeframe: tf,
			Pattern:   domain.PatternFailedBreakout,
			Strength:  domain.StrengthMedium,
		}
		signals = append(signals, adjustStrength(sig, c.Volume, avgVolume))
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
	if end.Close <= start.Close {
		return false
	}
	window := candles[len(candles)-lookback-1 : len(candles)-1]
	return countGreenCandles(window) > lookback/2
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
	if ratio > 2.0 && sig.Strength == domain.StrengthMedium {
		sig.Strength = domain.StrengthStrong
	} else if ratio < 0.5 && sig.Strength == domain.StrengthStrong {
		sig.Strength = domain.StrengthMedium
	}
	return sig
}
