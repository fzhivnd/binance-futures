package indicator

import (
	"context"
	"time"

	"futures/internal/domain"
)

// CandleSource fetches candles from the market engine.
type CandleSource interface {
	GetCandles(symbol string, tf domain.Timeframe, limit int) []domain.Candle
}

// Engine computes all technical indicators for a symbol.
type Engine struct {
	market    CandleSource
	oiHistory *OIHistory
}

func NewEngine(market CandleSource, oiHistory *OIHistory) *Engine {
	return &Engine{market: market, oiHistory: oiHistory}
}

// Compute builds an IndicatorSnapshot for the given symbol.
func (e *Engine) Compute(_ context.Context, symbol string) (*domain.IndicatorSnapshot, error) {
	candles1h := e.market.GetCandles(symbol, domain.Timeframe1h, 40)

	snap := &domain.IndicatorSnapshot{
		Symbol:    symbol,
		Timestamp: time.Now(),
	}

	if len(candles1h) >= 15 {
		rsi, err := RSI(candles1h, 14)
		if err == nil {
			snap.RSI14_1h = rsi
		}
	}

	if len(candles1h) >= 15 {
		atr, err := ATR(candles1h, 14)
		if err == nil {
			snap.ATR14_1h = atr
			if len(candles1h) > 0 {
				snap.ATRRatio = ATRRatio(atr, candles1h[len(candles1h)-1].Close)
			}
		}
	}

	ratio, spike := VolumeAnomaly(candles1h)
	snap.VolChange1h = ratio
	snap.VolumeSpike = spike

	if e.oiHistory != nil {
		d1h, err := e.oiHistory.Delta(symbol, time.Hour)
		if err == nil {
			snap.OIDelta1h = d1h
		}
		d4h, err := e.oiHistory.Delta(symbol, 4*time.Hour)
		if err == nil {
			snap.OIDelta4h = d4h
		}
	}

	patternTimeframes := []domain.Timeframe{
		domain.Timeframe1h,
		domain.Timeframe30m,
		domain.Timeframe15m,
		domain.Timeframe5m,
	}
	for _, tf := range patternTimeframes {
		candles := e.market.GetCandles(symbol, tf, 5)
		signals := DetectPatterns(candles, tf)
		snap.Patterns = append(snap.Patterns, signals...)
	}

	// MomentumLoss: RSI declining + OI flat/down
	snap.MomentumLoss = snap.RSI14_1h < 50 && snap.OIDelta1h <= 0

	return snap, nil
}

// ComputeBTCContext derives the BTC market context from 1h candles.
func (e *Engine) ComputeBTCContext(_ context.Context) (*domain.BTCContext, error) {
	candles := e.market.GetCandles("BTCUSDT", domain.Timeframe1h, 40)
	return ComputeBTCContext(candles)
}
