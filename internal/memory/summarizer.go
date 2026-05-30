package memory

import (
	"context"
	"fmt"
	"log/slog"

	"futures/internal/domain"
	"futures/internal/llm"
)

type Summarizer struct {
	client *llm.Client
}

func NewSummarizer(client *llm.Client) *Summarizer {
	return &Summarizer{client: client}
}

const summarizeSystemPrompt = `You generate concise lessons from completed Binance Futures funding-rate reversal trades.

STRATEGY CONTEXT:
We short altcoins with extremely negative funding rates (-0.2% to -2%) on Binance USDT-M Futures at 20x leverage.
The dual thesis: (1) capture a price reversal as overleveraged longs unwind, (2) collect the funding fee yield.
Hard stop-loss: 5% adverse price move. Base TP: ~2% price move (= ~40% ROI at 20x).
Breakeven trigger: SL moves to entry after 1.5% favorable move.

ENTRY MODES — what each trade was trying to capture:
- FRONTRUN: enter 10-30 min before funding settlement; bet that pre-settlement long-closing causes a price dump before the fee hits us.
- LAST_MINUTE: enter 2-6 min before settlement; directional confirmation trade, almost certainly pays the fee, TP widens at T-2m to cover it.
- AFTER: enter 0-1 min post-settlement; bet on post-fee panic dump. No funding fee paid. Weaker entry price but cleaner risk.

OUTCOME DEFINITIONS:
- WIN: Hit TP1 and trailing captured extended move. Full thesis validated.
- PARTIAL_WIN: TP1 hit but price reversed before trailing. Directionally correct, limited follow-through.
- LOSS: Hit hard stop-loss (5%). Setup completely failed.
- FORCE_SL: Risk check closed position early (thesis invalidated before hard SL). Damage limited.
- BREAKEVEN: Closed flat. Inconclusive.
- SKIP_VALIDATED: We skipped — price went up (against the short). Correct skip.
- SKIP_MISSED: We skipped — price dumped anyway. Missed opportunity.

KEY SIGNALS TO REFERENCE:
- Funding rate / bucket: core entry filter. More negative = more fee pressure = stronger thesis.
- OI delta 1h/15m: rising OI = leveraged longs adding = squeeze risk OR stronger eventual reversal.
- RSI(14,15m) and RSI(7,5m): overbought RSI supports reversal thesis; oversold = dangerous for shorts.
- ATR ratio: >3 means normal volatility can hit our tight 5% SL — high-risk for FRONTRUN entries.
- Volume spike / vol change 5m: spike confirms momentum shift or squeeze start.
- Momentum loss: bulls losing steam = supports thesis. Momentum building against us = invalidation.
- BTC trend/regime/RSI/breakout: BTC breakout is the #1 killer of alt shorts (drags everything up).
- Candle patterns (1h > 30m > 15m > 5m): bearish patterns (shooting star, evening star, engulfing) add confluence. Lower timeframes are more actionable.
- Minutes to settle: how much time existed in the thesis window.
- Daily ROI: proxy for cumulative longs being trapped.

LESSON RULES:
- Max 2 sentences. Be blunt.
- Reference actual values, not generic statements.
- For LOSS/FORCE_SL: identify the warning signal(s) present at entry that should have been weighted more.
- For WIN/PARTIAL_WIN: identify what gave the edge (funding level, OI expansion, BTC alignment, candle confirmation).
- For SKIP_VALIDATED: explain what condition correctly flagged the risk.
- For SKIP_MISSED: note what signal was incorrectly discounted that would have supported the trade.
- Do NOT write "the trade" — write specifically about the conditions and values.`

// Summarize generates a lesson from a completed trade memory.
// On LLM failure it returns a short fallback string so the caller always gets something.
func (s *Summarizer) Summarize(ctx context.Context, memory *domain.TradeMemory) (string, error) {
	userMsg := fmt.Sprintf(`Trade setup:
- Symbol: %s | Action: %s | Entry mode: %s
- Funding: %.2f%% (bucket %d) | Daily ROI: %.2f%%
- OI delta 1h: %.1f%% | OI delta 15m: %.1f%%
- RSI(14,15m): %.1f | RSI(7,5m): %.1f
- ATR ratio: %.3f | Vol change 5m: %.1f%% | Volume spike: %v
- Momentum loss: %v | Minutes to settle: %d
- BTC: %s (regime=%s, momentum=%d, breakout=%v, RSI=%.1f, 1h change=%.2f%%)
- Candles: %s
- Composite score: %.0f

Outcome: %s | PnL: %.2f%% | Hold time: %d minutes

Generate a concise lesson (1-2 sentences) about what drove this outcome.`,
		memory.Symbol, memory.Action, memory.EntryMode,
		memory.FundingRate*100, memory.FundingBucket, memory.DailyROI*100,
		memory.OIDelta1h, memory.OIDelta15m,
		memory.RSI14_15m, memory.RSI7_5m,
		memory.ATRRatio, memory.VolChange5m*100, memory.VolumeSpike,
		memory.MomentumLoss, memory.MinutesToSettle,
		memory.BTCTrend, memory.BTCRegime, memory.BTCMomentum, memory.BTCBreakout, memory.BTCRSI, memory.BTCChange1h*100,
		memory.CandlePatterns, memory.CompositeScore,
		memory.Outcome, memory.ProfitPct, memory.HoldMinutes,
	)

	lesson, err := s.client.CallPlain(ctx, summarizeSystemPrompt, userMsg)
	if err != nil {
		slog.Warn("summarizer failed, using fallback", "error", err)
		return fmt.Sprintf("%s: %.1f%% PnL in %s regime with funding %.2f%%",
			memory.Outcome, memory.ProfitPct, memory.BTCRegime, memory.FundingRate*100), nil
	}

	return lesson, nil
}
