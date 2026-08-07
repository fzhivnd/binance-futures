package app

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

const btcSymbol = "BTCUSDT"

var klineTimeframes = []string{"5m", "15m", "30m", "1h", "4h"}

// openWindowSubscriptions REST-backfills full candle history (with retry) and
// then WS-subscribes kline + bookTicker streams for every symbol in the
// funding window's locked watchlist. Called once at T-windowStartMinutes;
// the symbol list is not re-diffed again until the window closes.
func (a *App) openWindowSubscriptions(ctx context.Context, symbols []string) {
	a.backfillCandles(ctx, symbols)

	var klineStreams []string
	for _, s := range symbols {
		for _, tf := range klineTimeframes {
			klineStreams = append(klineStreams, s+"@kline_"+tf)
		}
	}
	if err := a.wsMarketData.Subscribe(ctx, klineStreams); err != nil {
		slog.Warn("failed to subscribe kline streams", "error", err)
	}

	if a.wsPublicData != nil {
		var bookTickerStreams []string
		for _, s := range symbols {
			bookTickerStreams = append(bookTickerStreams, strings.ToLower(s)+"@bookTicker")
		}
		if err := a.wsPublicData.Subscribe(ctx, bookTickerStreams); err != nil {
			slog.Warn("failed to subscribe bookTicker streams", "error", err)
		}
	}
}

// closeWindowSubscriptions unsubscribes kline + bookTicker streams for every
// symbol in the just-closed funding window and purges their candle/OI data,
// so no symbol can carry data into the next window — the next open() always
// starts from a clean REST backfill.
func (a *App) closeWindowSubscriptions(ctx context.Context, symbols []string) {
	var klineStreams []string
	for _, s := range symbols {
		for _, tf := range klineTimeframes {
			klineStreams = append(klineStreams, s+"@kline_"+tf)
		}
	}
	if err := a.wsMarketData.Unsubscribe(ctx, klineStreams); err != nil {
		slog.Warn("failed to unsubscribe kline streams", "error", err)
	}

	if a.wsPublicData != nil {
		var bookTickerStreams []string
		for _, s := range symbols {
			bookTickerStreams = append(bookTickerStreams, strings.ToLower(s)+"@bookTicker")
		}
		if err := a.wsPublicData.Unsubscribe(ctx, bookTickerStreams); err != nil {
			slog.Warn("failed to unsubscribe bookTicker streams", "error", err)
		}
	}

	for _, sym := range symbols {
		a.engine.PurgeSymbol(sym)
	}
}

const backfillMaxAttempts = 3

var backfillRetryDelay = time.Second

// backfillCandles seeds full candle history for every symbol in the funding
// window's watchlist from the REST API, retrying transient failures. Called
// once per symbol per window (never skipped for "already subscribed" — every
// window starts every symbol from a fresh REST snapshot).
func (a *App) backfillCandles(ctx context.Context, symbols []string) {
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
		{"5m", 100},
		{"15m", 100},
		{"30m", 100},
		{"1h", 100},
		{"4h", 100},
	}

	for _, sym := range symbols {
		for _, p := range backfillPlan {
			var lastErr error
			for attempt := 1; attempt <= backfillMaxAttempts; attempt++ {
				candles, err := a.binanceClient.FetchKlines(ctx, sym, p.interval, p.limit)
				if err == nil {
					a.engine.SeedCandles(candles)
					lastErr = nil
					break
				}
				lastErr = err
				if attempt < backfillMaxAttempts {
					select {
					case <-ctx.Done():
						return
					case <-time.After(backfillRetryDelay):
					}
				}
			}
			if lastErr != nil {
				slog.Warn("candle backfill failed after retries", "symbol", sym, "interval", p.interval, "attempts", backfillMaxAttempts, "error", lastErr)
			}
		}
	}
}
