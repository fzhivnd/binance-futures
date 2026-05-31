package app

import (
	"context"
	"log/slog"
)

const btcSymbol = "BTCUSDT"

func (a *App) updateKlineSubscriptions(ctx context.Context, old, new []string) {
	timeframes := []string{"5m", "15m", "30m", "1h", "4h"}
	oldSet := make(map[string]bool)
	for _, s := range old {
		oldSet[s] = true
	}
	newSet := make(map[string]bool)
	for _, s := range new {
		newSet[s] = true
	}

	// Always keep BTCUSDT subscribed for BTC context indicators.
	if !newSet[btcSymbol] {
		newSet[btcSymbol] = true
		new = append(new, btcSymbol)
	}

	var toUnsub []string
	var removed []string
	for _, s := range old {
		if !newSet[s] {
			for _, tf := range timeframes {
				toUnsub = append(toUnsub, s+"@kline_"+tf)
			}
			removed = append(removed, s)
		}
	}
	var toSub []string
	for _, s := range new {
		if !oldSet[s] {
			for _, tf := range timeframes {
				toSub = append(toSub, s+"@kline_"+tf)
			}
		}
	}

	if len(toUnsub) > 0 {
		_ = a.wsKlines.Unsubscribe(ctx, toUnsub)
	}
	if len(toSub) > 0 {
		a.backfillCandles(ctx, new, oldSet)
		_ = a.wsKlines.Subscribe(ctx, toSub)
	}

	for _, sym := range removed {
		a.engine.PurgeSymbol(sym)
	}
}

// backfillCandles seeds the candle store for newly added symbols by fetching
// recent history from the REST API. Failure is non-fatal — indicators degrade
// gracefully and the WS stream fills in data going forward.
func (a *App) backfillCandles(ctx context.Context, symbols []string, existing map[string]bool) {
	// Per-timeframe limits: closed candles needed + 1 for the open (forming) candle.
	//   5m  → RSI-7 warmup(8) + VolumeAnomaly baseline + DetectPatterns lookback(13)+1=14 closed → 20+1
	//   15m → RSI-14 warmup(15) + DetectPatterns lookback(9)+1=10 closed → 40+1
	//   30m → DetectPatterns lookback(5)+1=6 closed → 15+1
	//   1h  → ATR-14 warmup(15) + DetectPatterns lookback(4)+1=5 closed → 20+1
	//   4h  → calc24hROI needs 6 closed + 1 open = 8
	backfillPlan := []struct {
		interval string
		limit    int
	}{
		{"5m", 30},
		{"15m", 50},
		{"30m", 20},
		{"1h", 50},
		{"4h", 10},
	}

	// VERIFIED, COMMENT LOG
	//slog.Info("candle backfill started", "symbol", len(symbols))
	for _, sym := range symbols {
		if existing[sym] {
			continue
		}
		for _, p := range backfillPlan {
			candles, err := a.binanceClient.FetchKlines(ctx, sym, p.interval, p.limit)
			if err != nil {
				slog.Warn("candle backfill failed", "symbol", sym, "interval", p.interval, "error", err)
				continue
			}
			a.engine.SeedCandles(candles)
		}
	}
	//slog.Info("candle backfill done", "symbol", len(symbols))
}
