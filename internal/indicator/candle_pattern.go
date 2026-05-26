package indicator

import (
	"math"

	"futures/internal/domain"
)

// DetectPatterns analyzes the last 3 closed candles for bearish reversal patterns.
func DetectPatterns(candles []domain.Candle, tf domain.Timeframe) []domain.CandleSignal {
	if len(candles) < 3 {
		return nil
	}

	var signals []domain.CandleSignal
	c := candles[len(candles)-1]
	p := candles[len(candles)-2]
	pp := candles[len(candles)-3]

	body := c.Close - c.Open
	upperWick := c.High - math.Max(c.Open, c.Close)
	lowerWick := math.Min(c.Open, c.Close) - c.Low
	candleRange := c.High - c.Low

	if candleRange == 0 {
		return nil
	}

	bodySize := math.Abs(body)

	// Shooting Star: small body at bottom, long upper wick, after uptrend
	if upperWick >= 2*bodySize && lowerWick < bodySize && p.Close < c.High {
		if p.Close > pp.Close {
			signals = append(signals, domain.CandleSignal{
				Timeframe: tf,
				Pattern:   domain.PatternShootingStar,
				Strength:  domain.StrengthStrong,
			})
		}
	}

	// Bearish Engulfing: current red body fully engulfs previous green body
	prevBody := p.Close - p.Open
	if body < 0 && prevBody > 0 {
		if c.Open >= p.Close && c.Close <= p.Open {
			signals = append(signals, domain.CandleSignal{
				Timeframe: tf,
				Pattern:   domain.PatternBearishEngulfing,
				Strength:  domain.StrengthStrong,
			})
		}
	}

	// Upper Wick Rejection: wick > 60% of range, bearish body
	if upperWick/candleRange > 0.6 && body < 0 {
		signals = append(signals, domain.CandleSignal{
			Timeframe: tf,
			Pattern:   domain.PatternUpperWickReject,
			Strength:  domain.StrengthStrong,
		})
	}

	// Evening Star: big green → small body → big red
	ppBody := pp.Close - pp.Open
	pBodySize := math.Abs(prevBody)
	if ppBody > 0 && body < 0 {
		avgBody := (math.Abs(ppBody) + math.Abs(body)) / 2
		if pBodySize < avgBody*0.3 {
			signals = append(signals, domain.CandleSignal{
				Timeframe: tf,
				Pattern:   domain.PatternEveningStar,
				Strength:  domain.StrengthStrong,
			})
		}
	}

	// Doji After Pump: strong green candle followed by doji-like current
	if prevBody > 0 && prevBody/math.Abs(p.High-p.Low) > 0.6 {
		if bodySize/candleRange < 0.1 {
			signals = append(signals, domain.CandleSignal{
				Timeframe: tf,
				Pattern:   domain.PatternDojiAfterPump,
				Strength:  domain.StrengthMedium,
			})
		}
	}

	// Failed Breakout: made new high vs previous but closed below previous high
	if c.High > p.High && c.Close < p.High && body < 0 {
		signals = append(signals, domain.CandleSignal{
			Timeframe: tf,
			Pattern:   domain.PatternFailedBreakout,
			Strength:  domain.StrengthMedium,
		})
	}

	return signals
}
