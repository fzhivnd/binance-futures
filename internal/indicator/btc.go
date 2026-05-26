package indicator

import (
	"fmt"
	"math"

	"futures/internal/domain"
)

// ComputeBTCContext derives BTC market context from 1h candles.
func ComputeBTCContext(candles1h []domain.Candle) (*domain.BTCContext, error) {
	if len(candles1h) < 20 {
		return nil, fmt.Errorf("need 20+ BTC candles, got %d", len(candles1h))
	}

	rsi, _ := RSI(candles1h, 14)

	last := candles1h[len(candles1h)-1]
	prev1h := candles1h[len(candles1h)-2]
	change1h := (last.Close - prev1h.Close) / prev1h.Close * 100

	var change4h float64
	if len(candles1h) >= 5 {
		prev4h := candles1h[len(candles1h)-5]
		change4h = (last.Close - prev4h.Close) / prev4h.Close * 100
	}

	var trend string
	switch {
	case rsi > 70 && change4h > 3:
		trend = "bullish"
	case rsi < 30 && change4h < -3:
		trend = "bearish"
	case rsi > 60 && change4h > 1:
		trend = "bullish"
	case rsi < 40 && change4h < -1:
		trend = "bearish"
	default:
		trend = "neutral"
	}

	momentum := int(math.Abs(rsi-50) * 2)
	if momentum > 100 {
		momentum = 100
	}

	atr, _ := ATR(candles1h, 14)
	atrRatio := ATRRatio(atr, last.Close)
	var volatility string
	switch {
	case atrRatio > 3:
		volatility = "high"
	case atrRatio > 1.5:
		volatility = "medium"
	default:
		volatility = "low"
	}

	_, volumeSpike := VolumeAnomaly(candles1h)
	isBreakout := math.Abs(change1h) > 2 && volumeSpike

	return &domain.BTCContext{
		Trend:         trend,
		MomentumScore: momentum,
		Volatility:    volatility,
		RSI14_1h:      rsi,
		PriceChange1h: change1h,
		PriceChange4h: change4h,
		IsBreakout:    isBreakout,
	}, nil
}
