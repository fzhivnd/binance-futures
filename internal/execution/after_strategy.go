package execution

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"futures/internal/config"
	"futures/internal/intent"
	"futures/internal/market"
	"futures/internal/notify"
)

// AfterExecutionStrategy places a bid-depth-aware market order for AFTER-mode intents.
type AfterExecutionStrategy struct {
	bookTicker *market.BookTickerCache
	execEng    *ExecutionEngine
	notifier   *notify.Notifier
	cfg        *config.Config
}

func NewAfterExecutionStrategy(
	bookTicker *market.BookTickerCache,
	execEng *ExecutionEngine,
	notifier *notify.Notifier,
	cfg *config.Config,
) *AfterExecutionStrategy {
	return &AfterExecutionStrategy{
		bookTicker: bookTicker,
		execEng:    execEng,
		notifier:   notifier,
		cfg:        cfg,
	}
}

// Execute fires the AFTER intent with bid-depth sizing and staleness validation.
func (s *AfterExecutionStrategy) Execute(ctx context.Context, ti *intent.TradeIntent) error {
	start := time.Now()
	candidate := ti.Candidate.Candidate
	decision := ti.Decision

	// Staleness guard: validate that conditions are still favorable.
	currentPrice, ok := s.execEng.market.GetPrice(candidate.Symbol)
	if !ok || currentPrice == 0 {
		currentPrice = candidate.MarkPrice
	}
	if err := s.validateEntry(ti, currentPrice); err != nil {
		slog.Info("after_entry_skipped",
			"symbol", candidate.Symbol,
			"reason", err.Error(),
			"latency_ms", time.Since(start).Milliseconds(),
		)
		return nil
	}

	//positionSizePct := ti.Candidate.PositionSizePct
	//adjustedSizePct := s.adjustSizeForDepth(ctx, candidate.Symbol, positionSizePct, currentPrice)
	//if adjustedSizePct == 0 {
	//	slog.Info("after_entry_skipped",
	//		"symbol", candidate.Symbol,
	//		"reason", "thin_book",
	//		"window", "AFTER",
	//		"latency_ms", time.Since(start).Milliseconds(),
	//	)
	//	s.notifier.NotifyRiskEvent(ctx, notify.RiskEvent{
	//		Type:    "after_entry_skipped",
	//		Message: fmt.Sprintf("symbol: %s reason: thin_book", candidate.Symbol),
	//	})
	//	return nil
	//}

	// Build a modified candidate with any size adjustments
	sc := ti.Candidate
	//sc.PositionSizePct = adjustedSizePct

	err := s.execEng.ExecuteScoredWithLLM(ctx, sc, decision)
	slog.Info("after_execute",
		"symbol", candidate.Symbol,
		"latency_ms", time.Since(start).Milliseconds(),
		"error", err,
	)
	return err
}

// adjustSizeForDepth checks bid depth and returns the effective position size percentage.
func (s *AfterExecutionStrategy) adjustSizeForDepth(ctx context.Context, symbol string, requestedSizePct, currentPrice float64) float64 {
	afCfg := &s.cfg.AfterExecution

	// Estimate balance for order sizing
	balance, err := s.execEng.executor.GetAccountBalance(ctx)
	if err != nil || balance.AvailableBalance == 0 {
		slog.Warn("after_bid_depth: cannot get balance, using full size", "symbol", symbol)
		return requestedSizePct
	}

	desiredUSDT := balance.AvailableBalance * requestedSizePct / 100 * float64(s.cfg.Trading.Leverage)
	_ = currentPrice // used for context

	bidDepthUSDT, ok := s.bookTicker.BidDepthUSDT(symbol)
	if !ok || bidDepthUSDT == 0 {
		slog.Warn("after_bid_depth: no bookTicker data, using full size", "symbol", symbol)
		return requestedSizePct
	}

	slog.Info("after_bid_depth_usdt",
		"symbol", symbol,
		"depth_usdt", bidDepthUSDT,
		"desired_usdt", desiredUSDT,
	)

	spread, _ := s.bookTicker.SpreadBps(symbol)
	slog.Info("after_spread_bps", "symbol", symbol, "spread", spread)

	fullThreshold := afCfg.FullSizeDepthMultiplier * desiredUSDT
	halfThreshold := 1.5 * desiredUSDT
	minThreshold := afCfg.MinBidDepthMultiplier * desiredUSDT

	var adjustedPct float64
	var reason string

	switch {
	case bidDepthUSDT >= fullThreshold:
		adjustedPct = requestedSizePct
		reason = "full_size"
	case bidDepthUSDT >= halfThreshold:
		adjustedPct = requestedSizePct * 0.75
		reason = "reduced_75pct"
	case bidDepthUSDT >= minThreshold:
		adjustedPct = requestedSizePct * (afCfg.ReducedSizePct / 100)
		reason = "reduced_50pct"
	default:
		// Book is too thin — use minimum viable position or skip
		slog.Warn("after_entry_skipped",
			"symbol", symbol,
			"reason", "thin_book",
			"depth_usdt", bidDepthUSDT,
			"min_required", minThreshold,
		)
		return 0 // caller (execEng) will handle 0 size
	}

	if math.Abs(adjustedPct-requestedSizePct) > 0.01 {
		slog.Info("after_size_adjusted",
			"symbol", symbol,
			"original_pct", requestedSizePct,
			"adjusted_pct", adjustedPct,
			"reason", reason,
		)
	}

	return adjustedPct
}

// validateEntry checks whether market conditions are still valid since the LLM decision.
func (s *AfterExecutionStrategy) validateEntry(ti *intent.TradeIntent, currentPrice float64) error {
	decisionPrice := ti.Candidate.Candidate.MarkPrice
	if decisionPrice == 0 {
		return nil // can't validate, allow
	}

	priceDeltaPct := (currentPrice - decisionPrice) / decisionPrice * 100

	// Price pumped — better short entry, proceed
	if priceDeltaPct > 0 {
		slog.Info("after_entry_favorable",
			"symbol", ti.Candidate.Candidate.Symbol,
			"delta_pct", priceDeltaPct,
		)
		return nil
	}

	// Price dropped moderately — expected, log and proceed
	if priceDeltaPct < -1.0 {
		slog.Warn("after_entry_price_dropped",
			"symbol", ti.Candidate.Candidate.Symbol,
			"delta_pct", priceDeltaPct,
		)
	}

	// Only skip if price already moved more than 2x the expected dump
	fundingRate := ti.Candidate.Candidate.FundingRate
	maxExpectedDump := math.Abs(fundingRate) * 200 // 2x funding as heuristic
	if maxExpectedDump > 0 && priceDeltaPct < -maxExpectedDump {
		return fmt.Errorf("price already moved %.2f%% (> %.2f%% max expected)",
			priceDeltaPct, -maxExpectedDump)
	}

	return nil
}

// ExpectedBidAtEntry returns the best bid price at the time of order placement, for slippage tracking.
func (s *AfterExecutionStrategy) ExpectedBidAtEntry(symbol string) float64 {
	bt, ok := s.bookTicker.Get(symbol)
	if !ok {
		return 0
	}
	if time.Since(bt.UpdatedAt) > 2*time.Second {
		slog.Warn("after_bid_stale",
			"symbol", symbol,
			"age_ms", time.Since(bt.UpdatedAt).Milliseconds(),
		)
	}
	return bt.BidPrice
}
