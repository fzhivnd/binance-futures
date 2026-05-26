package indicator

import (
	"fmt"
	"math"

	"futures/internal/domain"
)

// ComputeBTCContext derives BTC market context.
// candles1h: 1h candles for trend/RSI/ATR; candles15m: 15m candles for breakout detection.
func ComputeBTCContext(candles1h []domain.Candle, candles15m []domain.Candle) (*domain.BTCContext, error) {
	if len(candles1h) < 20 {
		return nil, fmt.Errorf("need 20+ BTC 1h candles, got %d", len(candles1h))
	}

	rsi, _ := RSI(candles1h, 14)

	last1h := candles1h[len(candles1h)-1]
	prev1h := candles1h[len(candles1h)-2]
	change1h := (last1h.Close - prev1h.Close) / prev1h.Close * 100

	// Trend based on 1h RSI + 1h price change (macro context)
	var trend string
	switch {
	case rsi > 70 && change1h > 1.5:
		trend = "bullish"
	case rsi < 30 && change1h < -1.5:
		trend = "bearish"
	case rsi > 60 && change1h > 0.5:
		trend = "bullish"
	case rsi < 40 && change1h < -0.5:
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
