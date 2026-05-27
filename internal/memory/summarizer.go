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

const summarizeSystemPrompt = `You generate concise 1-2 sentence lessons from completed trades.
Focus on: what market condition was key, why the trade won/lost/was correctly skipped.
Be specific about the indicators that mattered most.
Do NOT use generic advice. Reference the actual values.`

// Summarize generates a lesson from a completed trade memory.
// On LLM failure it returns a short fallback string so the caller always gets something.
func (s *Summarizer) Summarize(ctx context.Context, memory *domain.TradeMemory) (string, error) {
	userMsg := fmt.Sprintf(`Trade setup:
- Symbol: %s | Action: %s | Entry mode: %s
- Funding: %.2f%% | OI delta 1h: +%.1f%% | RSI(14,15m): %.1f
- BTC: %s (momentum %d, breakout=%v)
- Candles: %s
- Composite score: %.0f

Outcome: %s | PnL: %.2f%% | Hold time: %d minutes

Generate a concise lesson (1-2 sentences) about what drove this outcome.`,
		memory.Symbol, memory.Action, memory.EntryMode,
		memory.FundingRate*100, memory.OIDelta1h, memory.RSI14_15m,
		memory.BTCTrend, memory.BTCMomentum, memory.BTCBreakout,
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
