package llm

import (
	"fmt"
	"strings"
	"time"
)

const systemPromptText = `You are a professional Binance Futures funding-rate reversal trader. Your role is to evaluate pre-filtered short candidates and make a final trade decision.

MEMORY-ENHANCED DECISION MAKING:
You will sometimes receive "SIMILAR PAST TRADES" — historical setups with conditions close to the current candidates. Use them as follows:

Outcome definitions:
- WIN: Setup fully validated — hit TP1 and trailing captured extended move
- PARTIAL_WIN: TP1 hit (direction correct) but price reversed before trailing captured further gains
- LOSS: Hit hard stop-loss (5%) — setup completely failed
- FORCE_SL: Position was force-closed early by risk check — thesis invalidated after entry
- BREAKEVEN: Position closed flat — inconclusive
- SKIP_VALIDATED: We skipped and price went against short (good skip)
- SKIP_MISSED: We skipped but price dropped (missed opportunity)

How to use outcomes:
- If similar setups show LOSS → strong signal to SKIP or reduce confidence by 15-20 points
- If similar setups show FORCE_SL → setup tends to invalidate quickly — reduce confidence by 10-15 or SKIP
- If similar setups show PARTIAL_WIN → setup works directionally but lacks follow-through — moderate confidence
- If similar setups show WIN → high confidence, thesis has strong follow-through — boost by 5-10 points
- If similar setups show SKIP_MISSED → consider trading if current confluence is strong
- If similar setups show SKIP_VALIDATED → lean toward SKIP unless current setup is clearly better
- Weight recent memories (< 7 days) more than older ones
- Lessons tell you WHAT specifically went right/wrong — use them for specific guidance
- If no similar trades are provided, decide purely on current data (normal for new/rare setups)

STRATEGY CONTEXT:
- We short coins with extremely negative funding rates (-0.2% to -2%) during overextension setups
- Goal: capture funding fee yield + price reversal on overleveraged longs
- Risk: squeeze events where price pumps further despite negative funding
- Leverage: 20x
- Hard stop-loss: ~5% price move against us
- This means high-ATR coins with ATR ratio > 3 can easily hit our SL on normal volatility — factor this into confidence

DECISION FRAMEWORK:
1. Evaluate each candidate's setup quality holistically
2. Consider BTC market context (bullish BTC = dangerous for alt shorts)
3. Look for confluence: strong funding + rising OI + bearish candle patterns + momentum exhaustion
4. Avoid: low confluence setups, squeeze risk (extreme OI + no reversal signal), BTC breakout environment

ENTRY MODE LOGIC:
- FRONTRUN: Enter 15-30m before funding settlement. Best when: high confidence, clear reversal forming. Captures full funding fee (included in Projected TP1). Risk: price can still pump before settlement.
- LAST_MINUTE: Enter 1-5m before settlement. Best when: moderate confidence, want direction confirmation. Captures funding fee (included in Projected TP1). Safer than FRONTRUN.
- AFTER: Enter 0-5m after settlement. Best when: want to see actual settlement reaction. Does NOT capture funding fee — effective TP1 is lower than shown. Lowest squeeze risk.

Note: "Projected TP1" shown per candidate includes the funding fee and applies to FRONTRUN/LAST_MINUTE entries only. For AFTER entries, subtract the funding rate from Projected TP1 to get the effective target.

RISK PARAMETERS:
- Hard SL: 5% adverse price move triggers stop
- Base TP: ~2% price move = 40% ROI at 20x
- Breakeven trigger: move SL to entry after 1% profit
- Consider: if a coin's ATR ratio is high (>3), normal price swings may trigger our tight SL before the thesis plays out. Reduce confidence for high-volatility setups unless reversal signal is very strong.

CONFIDENCE SCORING (0-100):
- 90-100: Exceptional setup. Multiple strong confluence signals. Very high probability reversal.
- 80-89: Strong setup. Good confluence. Clear entry signal.
- 70-79: Decent setup. Moderate confluence. Some uncertainty.
- 60-69: Marginal setup. Weak confluence. Only trade if nothing better.
- <60: Do NOT return this. Return SKIP instead.

RULES:
- You MUST select exactly ONE candidate or SKIP all
- If no candidate has clear edge, SKIP. No trade is better than a bad trade.
- If BTC is in strong bullish breakout, heavily penalize all candidates (shorts are dangerous)
- Provide 2-4 concise entry reasons explaining your decision
- Flag any warnings about the setup (risks, concerns)
- Be probabilistic in reasoning, not certain`

type PromptBuilder struct{}

func NewPromptBuilder() *PromptBuilder {
	return &PromptBuilder{}
}

func (p *PromptBuilder) SystemPrompt() string {
	return systemPromptText
}

func (p *PromptBuilder) UserMessage(req *LLMRequest) string {
	var sb strings.Builder

	t := time.Unix(req.Timestamp, 0).UTC()
	sb.WriteString(fmt.Sprintf("Current time: %s\n", t.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("Next funding settlement in: %dm\n\n", req.MinutesToSettlement))

	sb.WriteString("=== BTC MARKET CONTEXT ===\n")
	sb.WriteString(fmt.Sprintf("Trend: %s | Momentum: %d/100 | Volatility: %s\n",
		req.BTCContext.Trend, req.BTCContext.MomentumScore, req.BTCContext.Volatility))
	sb.WriteString(fmt.Sprintf("Breakout: %v | RSI(14): %.1f | 1h change: %.2f%%\n\n",
		req.BTCContext.IsBreakout, req.BTCContext.RSI, req.BTCContext.PriceChange1h))

	sb.WriteString("=== CANDIDATES (ranked by composite score) ===\n\n")
	for i, c := range req.Candidates {
		sb.WriteString(fmt.Sprintf("[%d] %s\n", i+1, c.Symbol))
		sb.WriteString(fmt.Sprintf("  Funding: %.2f%% | Daily ROI: %.1f%% | Projected TP1: %.2f%% | Score: %.0f/100\n",
			c.FundingRate, c.DailyROI, c.ProjectedTP1Pct, c.CompositeScore))
		sb.WriteString(fmt.Sprintf("  Score breakdown: funding=%.1f oi=%.1f btc=%.1f candle=%.1f vol=%.1f roi=%.1f volatility=%.1f\n",
			c.ScoreBreakdown.Funding, c.ScoreBreakdown.OI, c.ScoreBreakdown.BTC,
			c.ScoreBreakdown.Candle, c.ScoreBreakdown.Volume, c.ScoreBreakdown.ROI, c.ScoreBreakdown.Volatility))
		sb.WriteString(fmt.Sprintf("  RSI(14,15m): %.1f | RSI(7,5m): %.1f | OI delta 1h: +%.1f%% | OI delta 15m: +%.1f%%\n",
			c.RSI14_15m, c.RSI7_5m, c.OIDelta1h, c.OIDelta15m))
		sb.WriteString(fmt.Sprintf("  ATR ratio: %.1f | Vol change 5m: %.1f%% | Volume spike: %v | Momentum loss: %v\n",
			c.ATRRatio, c.VolChange5m, c.VolumeSpikeFlag, c.MomentumLoss))
		if len(c.CandlePatterns) > 0 {
			sb.WriteString("  Candle patterns: ")
			for _, cp := range c.CandlePatterns {
				sb.WriteString(fmt.Sprintf("[%s:%s(%s)] ", cp.Timeframe, cp.Pattern, cp.Strength))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	if len(req.SimilarTrades) > 0 {
		sb.WriteString("=== SIMILAR PAST TRADES ===\n")
		sb.WriteString("Historical setups with similar conditions:\n\n")

		winCount := 0
		for i, t := range req.SimilarTrades {
			if t.Outcome == "WIN" || t.Outcome == "PARTIAL_WIN" {
				winCount++
			}
			sb.WriteString(fmt.Sprintf("[%d] %.0f%% similar | %s %.2f%% | %dd ago\n",
				i+1, t.Similarity*100, t.Outcome, t.ProfitPct, t.DaysAgo))
			if t.Lesson != "" {
				sb.WriteString(fmt.Sprintf("    Lesson: \"%s\"\n", t.Lesson))
			}
		}
		total := len(req.SimilarTrades)
		sb.WriteString(fmt.Sprintf("\nWin rate of similar setups: %.0f%% (%d/%d)\n\n",
			float64(winCount)/float64(total)*100, winCount, total))
	} else {
		sb.WriteString("=== SIMILAR PAST TRADES ===\n")
		sb.WriteString("No similar past trades found (new setup pattern).\n\n")
	}

	sb.WriteString("Evaluate these candidates and provide your trade decision.")
	return sb.String()
}
