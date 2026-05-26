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
	snap := &domain.IndicatorSnapshot{
		Symbol:    symbol,
		Timestamp: time.Now(),
	}

	// RSI-14 on 15m: setup context (~3.5h lookback)
	candles15m := e.market.GetCandles(symbol, domain.Timeframe15m, 40)
	if len(candles15m) >= 15 {
		if rsi, err := RSI(candles15m, 14); err == nil {
			snap.RSI14_15m = rsi
		}
	}

	// RSI-7 on 5m: entry timing (~35 min lookback)
	candles5m := e.market.GetCandles(symbol, domain.Timeframe5m, 20)
	if len(candles5m) >= 8 {
		if rsi, err := RSI(candles5m, 7); err == nil {
			snap.RSI7_5m = rsi
		}
	}

	// ATR-14 on 1h: pre-trade volatility filter only
	candles1h := e.market.GetCandles(symbol, domain.Timeframe1h, 20)
	if len(candles1h) >= 15 {
		if atr, err := ATR(candles1h, 14); err == nil {
			snap.ATR14_1h = atr
			snap.ATRRatio = ATRRatio(atr, candles1h[len(candles1h)-1].Close)
		}
	}

	// Volume anomaly on 5m: catches the surge happening right now at entry
	ratio, spike := VolumeAnomaly(candles5m)
	snap.VolChange5m = ratio
	snap.VolumeSpike = spike

	// OI delta: 1h for setup confirmation, 15m for recent leverage buildup
	if e.oiHistory != nil {
		if d1h, err := e.oiHistory.Delta(symbol, time.Hour); err == nil {
			snap.OIDelta1h = d1h
		}
		if d15m, err := e.oiHistory.Delta(symbol, 15*time.Minute); err == nil {
			snap.OIDelta15m = d15m
		}
	}

	// Candle patterns across all timeframes
	for _, tf := range []domain.Timeframe{domain.Timeframe1h, domain.Timeframe30m, domain.Timeframe15m, domain.Timeframe5m} {
		candles := e.market.GetCandles(symbol, tf, 5)
		snap.Patterns = append(snap.Patterns, DetectPatterns(candles, tf)...)
	}

	// MomentumLoss: RSI on both timeframes declining + OI flat/down
	snap.MomentumLoss = snap.RSI14_15m < 50 && snap.RSI7_5m < 50 && snap.OIDelta1h <= 0

	return snap, nil
}

// ComputeBTCContext derives the BTC market context.
func (e *Engine) ComputeBTCContext(_ context.Context) (*domain.BTCContext, error) {
	candles1h := e.market.GetCandles("BTCUSDT", domain.Timeframe1h, 40)
	candles15m := e.market.GetCandles("BTCUSDT", domain.Timeframe15m, 20)
	return ComputeBTCContext(candles1h, candles15m)
}
