package app

import (
	"context"
	"log/slog"
	"time"

	"futures/internal/exchange"
	"futures/internal/scheduler"
)

func (a *App) oiPoller(ctx context.Context, client *exchange.BinanceClient) {
	a.runOICycle(ctx, client)

	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.runOICycle(ctx, client)
		}
	}
}

func (a *App) runOICycle(ctx context.Context, client *exchange.BinanceClient) {
	symbols := a.engine.GetTopNegativeFundingSymbols(20)
	// VERIFIED, COMMENT LOG
	//slog.Info("oi poller cycle started", "symbols", len(symbols))
	updated := 0
	for _, sym := range symbols {
		oi, err := client.GetOpenInterest(ctx, sym)
		if err != nil {
			slog.Warn("oi fetch failed", "symbol", sym, "error", err)
			continue
		}
		a.engine.UpdateOI(sym, oi.OpenInterest)
		updated++
	}
	//slog.Info("oi poller cycle done", "updated", updated, "total", len(symbols))
}

// windowMarketDataManager owns the market-data subscription lifecycle for one
// funding window at a time: at T-windowStartMinutes it locks in the top-20
// negative-funding watchlist (+ BTCUSDT), REST-backfills every timeframe with
// retry, and subscribes kline + bookTicker streams; the list is held fixed
// for the whole window so every symbol's candles stay fresh off the live WS
// feed without ever depending on stale prior-window data. At T+5 everything
// is unsubscribed and purged, so no symbol can carry candle/OI data across
// funding cycles — the next window always starts from a clean REST snapshot.
func (a *App) windowMarketDataManager(ctx context.Context) {
	for {
		open, windowClose, _ := scheduler.NextWindowBounds(time.Now(), a.cfg.Scheduler.WindowStartMinutes)

		if d := time.Until(open); d > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(d):
			}
		}

		symbols := a.engine.GetTopNegativeFundingSymbols(20)
		if !contains(symbols, btcSymbol) {
			symbols = append(symbols, btcSymbol)
		}

		if len(symbols) > 0 {
			slog.Info("funding window opened, subscribing market data", "symbols", len(symbols))
			a.openWindowSubscriptions(ctx, symbols)
		}

		if d := time.Until(windowClose); d > 0 {
			select {
			case <-ctx.Done():
				a.closeWindowSubscriptions(ctx, symbols)
				return
			case <-time.After(d):
			}
		}

		slog.Info("funding window closed, purging market data", "symbols", len(symbols))
		a.closeWindowSubscriptions(ctx, symbols)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// fundingIntervalRefresher re-fetches /fapi/v1/fundingInfo shortly after each
// funding settlement so that interval changes (e.g. 4h → 1h) are picked up
// before the next scan cycle uses the updated daily ROI calculation.
func (a *App) fundingIntervalRefresher(ctx context.Context, client *exchange.BinanceClient) {
	for {
		next := scheduler.NextFundingTime(time.Now().UTC())
		sleepUntil := next.Add(30 * time.Second)
		delay := time.Until(sleepUntil)
		if delay < 0 {
			delay = 30 * time.Second
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		list, err := client.GetFundingInfo(ctx)
		if err != nil {
			slog.Warn("funding interval refresh failed", "error", err)
			continue
		}
		for _, fi := range list {
			a.engine.SetFundingInterval(fi.Symbol, fi.FundingIntervalHours)
		}
		// VERIFIED, COMMENT LOG
		//slog.Info("funding intervals refreshed", "symbols", len(list))
	}
}

// keepAliveListenKey pings /fapi/v1/listenKey every 30 minutes to prevent expiry (Binance timeout is 60 min).
func (a *App) keepAliveListenKey(ctx context.Context, client *exchange.BinanceClient, listenKey string) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := client.KeepAliveListenKey(ctx, listenKey); err != nil {
				slog.Warn("listenKey keepalive failed", "error", err)
			}
		}
	}
}

// startSkipValidator fires once per funding cycle at T+2h after each settlement
// (i.e. 02:00, 06:00, 10:00, 14:00, 18:00, 22:00 UTC). This gives the market 2
// full hours after settlement to reveal whether a skipped setup would have paid off.
func (a *App) startSkipValidator(ctx context.Context) {
	for {
		next := scheduler.NextFundingTime(time.Now().UTC()).Add(2 * time.Hour)
		delay := time.Until(next)
		if delay < 0 {
			delay = time.Second
		}
		slog.Info("skip validator sleeping until T+2h ", "wake_at", delay)

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		if err := a.memoryEngine.ValidateSkips(ctx, a.checkHistoricalPrice); err != nil {
			slog.Error("skip validation failed", "error", err)
		}
	}
}
