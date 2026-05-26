package backtest

import (
	"context"
	"log/slog"
	"time"

	"futures/internal/domain"
	"futures/internal/indicator"
	"futures/internal/scoring"
)

// staticCandleSource adapts a map of candles into the CandleSource interface
// at a fixed point in time (backtesting replay).
type staticCandleSource struct {
	candles map[domain.Timeframe][]domain.Candle
	cutoff  time.Time
}

func (s *staticCandleSource) GetCandles(symbol string, tf domain.Timeframe, limit int) []domain.Candle {
	all := s.candles[tf]
	// filter to candles before cutoff
	var filtered []domain.Candle
	for _, c := range all {
		if !c.OpenTime.After(s.cutoff) {
			filtered = append(filtered, c)
		}
	}
	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	return filtered
}

// Runner replays historical data through the full Phase 2 scoring pipeline.
type Runner struct {
	cfg    Config
	loader *DataLoader
	scorer *scoring.Scorer
}

func NewRunner(cfg Config, loader *DataLoader) *Runner {
	return &Runner{
		cfg:    cfg,
		loader: loader,
		scorer: scoring.NewScorer(cfg.Weights),
	}
}

func (r *Runner) Run(ctx context.Context) (*Result, error) {
	data, err := r.loader.Load(ctx, r.cfg)
	if err != nil {
		return nil, err
	}

	balance := r.cfg.InitialBalance
	peakBalance := balance
	var maxDD float64
	var trades []Trade

	slog.Info("backtest starting",
		"symbols", len(r.cfg.Symbols),
		"windows", len(data.FundingWindows),
	)

	for _, window := range data.FundingWindows {
		if window.Rate >= -0.002 || window.Rate < -0.02 {
			continue
		}

		sym := window.Symbol
		symCandles, ok := data.Candles[sym]
		if !ok {
			continue
		}

		source := &staticCandleSource{candles: symCandles, cutoff: window.Time}

		// Compute indicators at this point in time
		indEng := indicator.NewEngine(source, nil)
		snap, err := indEng.Compute(ctx, sym)
		if err != nil {
			continue
		}

		// BTC context
		btcCandles := data.Candles["BTCUSDT"]
		var btcCtx *domain.BTCContext
		if btcCandles != nil {
			btcSrc := &staticCandleSource{candles: btcCandles, cutoff: window.Time}
			btcCtx, _ = indicator.ComputeBTCContext(btcSrc.GetCandles("BTCUSDT", domain.Timeframe1h, 40))
		}

		// Build candidate
		var price float64
		c1h := source.GetCandles(sym, domain.Timeframe1h, 1)
		if len(c1h) > 0 {
			price = c1h[len(c1h)-1].Close
		}
		if price == 0 {
			continue
		}

		cand := domain.Candidate{
			Symbol:      sym,
			FundingRate: window.Rate,
			MarkPrice:   price,
			DailyROI:    calcROI1D(source, sym),
		}

		sc := r.scorer.Score(cand, snap, btcCtx)

		if sc.CompositeScore < r.cfg.MinScore {
			continue
		}
		if snap.ATRRatio > r.cfg.MaxATRRatio {
			continue
		}

		// Simulate exit by walking forward through subsequent 1h candles
		entry := price
		exitPrice, result, duration := simulateExit(symCandles[domain.Timeframe1h], window.Time, entry, r.cfg)

		posSize := balance * sc.PositionSizePct / 100 * float64(r.cfg.Leverage)
		pnl := (entry - exitPrice) / entry * posSize // SHORT

		balance += pnl
		if balance > peakBalance {
			peakBalance = balance
		}
		dd := (peakBalance - balance) / peakBalance * 100
		if dd > maxDD {
			maxDD = dd
		}

		trades = append(trades, Trade{
			Timestamp:      window.Time,
			Symbol:         sym,
			FundingRate:    window.Rate,
			CompositeScore: sc.CompositeScore,
			Confidence:     sc.Confidence,
			EntryPrice:     entry,
			ExitPrice:      exitPrice,
			PnL:            pnl,
			Result:         result,
			HoldDuration:   duration,
			Breakdown:      sc.Breakdown,
		})
	}

	return buildReport(trades, balance, r.cfg.InitialBalance, maxDD), nil
}

func calcROI1D(source *staticCandleSource, _ string) float64 {
	candles := source.GetCandles("", domain.Timeframe1d, 2)
	if len(candles) < 2 {
		return 0
	}
	prev := candles[len(candles)-2]
	curr := candles[len(candles)-1]
	if prev.Close == 0 {
		return 0
	}
	return (curr.Close - prev.Close) / prev.Close * 100
}

func simulateExit(candles []domain.Candle, entryTime time.Time, entry float64, cfg Config) (exitPrice float64, result string, duration time.Duration) {
	slPct := 5.0 / float64(cfg.Leverage)
	tpPct := 2.0

	stopLoss := entry * (1 + slPct/100)
	takeProfit := entry * (1 - tpPct/100)

	maxHold := 8 * time.Hour
	exitPrice = entry
	result = "TIMEOUT"

	for _, c := range candles {
		if !c.OpenTime.After(entryTime) {
			continue
		}
		if c.OpenTime.Sub(entryTime) > maxHold {
			exitPrice = c.Close
			result = "TIMEOUT"
			duration = c.OpenTime.Sub(entryTime)
			break
		}
		if c.High >= stopLoss {
			exitPrice = stopLoss
			result = "LOSS"
			duration = c.OpenTime.Sub(entryTime)
			return
		}
		if c.Low <= takeProfit {
			exitPrice = takeProfit
			result = "WIN"
			duration = c.OpenTime.Sub(entryTime)
			return
		}
		exitPrice = c.Close
		duration = c.OpenTime.Sub(entryTime)
	}
	return
}
