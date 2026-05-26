package risk

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"futures/internal/domain"
	"futures/internal/storage"
)

// Config holds Phase 2 risk parameters.
type Config struct {
	MaxDailyLosses    int
	MaxDrawdownPct    float64
	MaxATRRatio       float64
	BTCBreakoutReject bool
	MinCompositeScore float64
	MaxPositions      int
	CooldownMinutes   int
}

var DefaultConfig = Config{
	MaxDailyLosses:    2,
	MaxDrawdownPct:    10.0,
	MaxATRRatio:       6.0,
	BTCBreakoutReject: true,
	MinCompositeScore: 60,
	MaxPositions:      2,
	CooldownMinutes:   15,
}

// Engine implements pre-trade risk checks combining Phase 1 and Phase 2 guards.
type Engine struct {
	cache     storage.StateCache
	tradeRepo storage.TradeRepository
	drawdown  *DrawdownTracker
	cfg       Config
}

func NewEngine(cache storage.StateCache, tradeRepo storage.TradeRepository, drawdown *DrawdownTracker, cfg Config) *Engine {
	return &Engine{
		cache:     cache,
		tradeRepo: tradeRepo,
		drawdown:  drawdown,
		cfg:       cfg,
	}
}

// PreCheck runs Phase 1 guards: kill switch, cooldown, position count, daily loss limit.
func (e *Engine) PreCheck(ctx context.Context) error {
	killSwitch, err := e.cache.GetKillSwitch(ctx)
	if err != nil {
		return fmt.Errorf("kill switch check: %w", err)
	}
	if killSwitch {
		return fmt.Errorf("kill switch active")
	}

	onCooldown, err := e.cache.IsOnCooldown(ctx)
	if err != nil {
		return fmt.Errorf("cooldown check: %w", err)
	}
	if onCooldown {
		return fmt.Errorf("on cooldown")
	}

	positions, err := e.cache.GetActivePositions(ctx)
	if err != nil {
		return fmt.Errorf("positions check: %w", err)
	}
	if len(positions) >= e.cfg.MaxPositions {
		return fmt.Errorf("max positions reached (%d)", e.cfg.MaxPositions)
	}

	losses, err := e.tradeRepo.GetDailyLossCount(ctx, time.Now().UTC())
	if err == nil && losses >= e.cfg.MaxDailyLosses {
		return fmt.Errorf("daily loss limit reached (%d)", e.cfg.MaxDailyLosses)
	}

	return nil
}

// EvaluateCandidate runs Phase 2 indicator-aware guards.
func (e *Engine) EvaluateCandidate(ctx context.Context, sc *domain.ScoredCandidate, btc *domain.BTCContext) error {
	if sc.CompositeScore < e.cfg.MinCompositeScore {
		slog.Debug("candidate rejected: score too low",
			"symbol", sc.Candidate.Symbol,
			"score", sc.CompositeScore,
			"min", e.cfg.MinCompositeScore)
		return fmt.Errorf("score %.1f below threshold %.1f", sc.CompositeScore, e.cfg.MinCompositeScore)
	}

	if sc.Indicators != nil && sc.Indicators.ATRRatio > e.cfg.MaxATRRatio {
		return fmt.Errorf("ATR ratio %.2f%% exceeds max %.2f%%", sc.Indicators.ATRRatio, e.cfg.MaxATRRatio)
	}

	if btc != nil && e.cfg.BTCBreakoutReject && btc.IsBreakout && btc.Trend == "bullish" {
		return fmt.Errorf("BTC bullish breakout detected, skipping alt short")
	}

	if dd := e.drawdown.CurrentDrawdownPct(); dd > e.cfg.MaxDrawdownPct {
		return fmt.Errorf("max drawdown %.2f%% breached (limit %.2f%%)", dd, e.cfg.MaxDrawdownPct)
	}

	return nil
}
