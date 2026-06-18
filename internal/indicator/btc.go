package indicator

import (
	"fmt"
	"math"

	"futures/internal/domain"
)

// ComputeBTCContext derives BTC market context.
// candles1h: 1h candles for trend/RSI/ATR; candles15m: 15m candles for breakout detection.
func ComputeBTCContext(candles1h []domain.Candle, candles15m []domain.Candle) (*domain.BTCContext, error) {
	if len(candles1h) < 22 {
		return nil, fmt.Errorf("need 22+ BTC 1h candles, got %d", len(candles1h))
	}

	rsi, _ := RSI(candles1h, 14)

	last1h := candles1h[len(candles1h)-1]
	prev1h := candles1h[len(candles1h)-2]
	change1h := (last1h.Close - prev1h.Close) / prev1h.Close * 100

	// 3-candle slope: cumulative 3h move, more stable than single-candle change.
	base := candles1h[len(candles1h)-4].Close
	slope := (last1h.Close - base) / base * 100

	ema9 := ema(candles1h, 9)
	ema21 := ema(candles1h, 21)

	// 2-of-3 voting: RSI, 3h slope, EMA9 vs EMA21.
	bullSignals := 0
	bearSignals := 0
	if rsi > 55 {
		bullSignals++
	}
	if slope > 0.3 {
		bullSignals++
	}
	if ema9 > ema21 {
		bullSignals++
	}
	if rsi < 45 {
		bearSignals++
	}
	if slope < -0.3 {
		bearSignals++
	}
	if ema9 < ema21 {
		bearSignals++
	}

	var trend string
	switch {
	case bullSignals >= 2:
		trend = "bullish"
	case bearSignals >= 2:
		trend = "bearish"
	default:
		trend = "neutral"
	}

	momentum := int(math.Abs(rsi-50) * 2)
	if momentum > 100 {
		momentum = 100
	}

	atr, _ := ATR(candles1h, 14)
	atrRatio := ATRRatio(atr, last1h.Close)
	var volatility string
	switch {
	case atrRatio > 3:
		volatility = "high"
	case atrRatio > 1.5:
		volatility = "medium"
	default:
		volatility = "low"
	}

	// Breakout detection on 15m: a move happening right now in the funding window
	var change15m float64
	var isBreakout bool
	if len(candles15m) >= 2 {
		last15m := candles15m[len(candles15m)-1]
		prev15m := candles15m[len(candles15m)-2]
		change15m = (last15m.Close - prev15m.Close) / prev15m.Close * 100
		_, volumeSpike := VolumeAnomaly(candles15m)
		isBreakout = math.Abs(change15m) > 0.8 && volumeSpike
	}

	return &domain.BTCContext{
		Trend:          trend,
		MomentumScore:  momentum,
		Volatility:     volatility,
		RSI14_1h:       rsi,
		PriceChange1h:  change1h,
		PriceChange15m: change15m,
		IsBreakout:     isBreakout,
	}, nil
}

// ema computes the exponential moving average for the last candle using Wilder-style smoothing.
func ema(candles []domain.Candle, period int) float64 {
	if len(candles) < period {
		return candles[len(candles)-1].Close
	}
	k := 2.0 / float64(period+1)
	val := candles[len(candles)-period].Close
	for i := len(candles) - period + 1; i < len(candles); i++ {
		val = candles[i].Close*k + val*(1-k)
	}
	return val
}
