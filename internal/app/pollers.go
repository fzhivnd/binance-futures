package app

import (
	"context"
	"log/slog"
	"time"

	"futures/internal/exchange"
	"futures/internal/scheduler"
)

func (a *App) oiPoller(ctx context.Context, client *exchange.BinanceClient) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			symbols := a.engine.GetTopNegativeFundingSymbols(20)
			slog.Info("oi poller cycle started", "symbols", len(symbols))
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
			slog.Info("oi poller cycle done", "updated", updated, "total", len(symbols))
		}
	}
}

func (a *App) klineSubscriber(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	var currentSymbols []string

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			newSymbols := a.engine.GetTopNegativeFundingSymbols(20)
			if len(newSymbols) == 0 {
				continue
			}
			a.updateKlineSubscriptions(ctx, currentSymbols, newSymbols)
			currentSymbols = newSymbols
		}
	}
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
		slog.Info("funding intervals refreshed", "symbols", len(list))
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

func (a *App) startSkipValidator(ctx context.Context) {
	delay := a.cfg.Memory.GetSkipValidationDelay()
	ticker := time.NewTicker(delay)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.memoryEngine.ValidateSkips(ctx, a.checkHistoricalPrice); err != nil {
				slog.Error("skip validation failed", "error", err)
			}
		}
	}
}
