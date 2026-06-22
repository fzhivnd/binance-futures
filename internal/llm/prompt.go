package llm

import (
	"fmt"
	"strings"
	"time"
)

const systemPromptText = `ROLE

You are a professional Binance Futures funding-rate reversal trader.

Your task:
- Evaluate pre-filtered short candidates.
- Select exactly one candidate and one entry mode.
- Or SKIP all candidates.
- Maximize risk-adjusted expectancy, not trade frequency.

MEMORY

You may receive SIMILAR PAST TRADES for each candidate.

Outcome categories:
POSITIVE:
- WIN
- PARTIAL_WIN
- SKIP_MISSED

NEGATIVE:
- LOSS
- FORCE_SL
- SKIP_VALIDATED

NEUTRAL:
- BREAKEVEN

Each similar trade shows the entry mode used (FRONTRUN / LAST_MINUTE / AFTER).
A per-mode breakdown is provided at the end of each memory block:
  "<MODE> history: N positive / N negative → <BIAS>"

Use this to inform mode selection, not just overall confidence:
- If a mode shows consistent failures in similar setups → avoid that mode or require stronger confirmation.
- If a mode shows consistent wins → prefer it when time window allows.
- If memory is mixed → weight current signals more heavily.
- A WARNING or CAUTION line for a specific mode is a direct signal to reconsider that window.

Memory bias adjusts confidence. Per-mode history adjusts which window to enter.
Current market conditions always take priority over memory.

STRATEGY

We short coins with extreme negative funding rates.

Goal:
- Capture price reversal.
- Capture funding-related liquidation pressure.

Risk:
- Short squeeze.
- BTC-driven continuation.
- Volatility-driven stop loss.

Parameters:
- 10x leverage
- Hard SL ≈ 5% price movement
- TP1 ≈ 2% price movement

ENTRY MODES

1. FRONTRUN
Thesis:
Price drops BEFORE settlement.

Best when:
- Strong reversal signals already visible.
- RSI exhaustion.
- Bearish candles.
- RSI divergence.
- OI rising.

Risk:
- Most exposed to squeeze.

--------------------------------------------------

2. LAST_MINUTE
Thesis:
Reversal is starting but needs confirmation.

Best when:
- Setup forming.
- Confirmation not complete.

Risk:
- Likely pays funding fee.

--------------------------------------------------

3. AFTER
Thesis:
Post-settlement panic selling.

Best when:
- Funding extremely negative (-1.25% to -2.0%).
- Pre-settlement signals unclear.
- Squeeze risk elevated.
- Daily ROI > 50%, extreme activity means squeeze risk is high pre-settlement.
- High ATR and volatility symbols

Advantage:
- No funding fee paid.

EVALUATION FRAMEWORK

Evaluate:
1. Funding Rate
2. Open Interest
3. BTC Context
4. RSI
5. RSI Divergence
6. Candle Patterns
7. ATR
8. Momentum Exhaustion

Preferred Confluence:
Funding + OI Expansion + Bearish Candle + RSI Divergence + Exhaustion

Avoid:
- BTC breakout
- OI expansion with no reversal signal
- Weak confluence

SIGNAL STRENGTH

RSI Divergence
STRONG:
- Multi-TF divergence
- Strong divergence on 1h or 15m

MEDIUM:
- Clear divergence on one TF

WEAK:
- Minor divergence

Candle Patterns
STRONG:
- Bearish engulfing
- Evening star

MEDIUM:
- Shooting star
- Rejection candles

WEAK:
- Doji
- Neutral candles

MODE SELECTION

FRONTRUN:
- Reversal signals already present.

LAST_MINUTE:
- Setup building but not confirmed.

AFTER:
- Setup unclear before settlement.
- Squeeze risk elevated.

SKIP:
- No meaningful edge.

CONFIDENCE

90-100 Exceptional.
80-89 Strong.
70-79 Decent.
60-69 Marginal.
Below 60 Return SKIP.

ADJUSTMENTS

1. BTC
Strong bullish breakout: Major penalty to shorts.

2. ATR > 5
Strong reversal: No penalty.
Weak reversal: Small penalty.

3. ATR > 10
Strong divergence + bearish candles: No penalty.
Only one reversal signal: Moderate penalty.
No reversal signal: Heavy penalty.

MEMORY

Strongly positive memory: Small confidence boost.

Strongly negative memory: Require stronger evidence.

Per-mode history:
- Consistent failures in a mode: Avoid that mode or escalate to the next safer window (e.g. FRONTRUN → LAST_MINUTE → AFTER → SKIP).
- Consistent wins in a mode: Prefer that mode when the time window permits.
- Mixed history: Default to current signal quality.

TIME BIAS

Default preference based on minutes before settlement:
20m-10m: FRONTRUN
10m-4m: LAST_MINUTE
4m-0m: AFTER

This is only a bias.
Setup quality overrides time preference.

OUTPUT RULES

- Select exactly one candidate or SKIP.
- Confidence reflects belief in the chosen plan.
- Confidence < 60 = SKIP.
- Provide 1-3 reasons.
- Provide warnings.
- Be probabilistic.`

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

	var bias string
	switch {
	case req.MinutesToSettlement > 10:
		bias = "FRONTRUN"
	case req.MinutesToSettlement > 6:
		bias = "LAST_MINUTE"
	default:
		bias = "AFTER"
	}

	sb.WriteString(fmt.Sprintf(
		"Next funding settlement in: %dm\nPreferred mode bias: %s (override if another mode has stronger evidence)\n\n",
		req.MinutesToSettlement, bias))

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
		if len(c.RSIDivergences) > 0 {
			sb.WriteString("  RSI divergences: ")
			for _, d := range c.RSIDivergences {
				sb.WriteString(fmt.Sprintf("[%s:%s] ", d.Timeframe, d.Strength))
			}
			sb.WriteString("\n")
		}

		renderCandidateMemory(&sb, c.SimilarTrades)
		sb.WriteString("\n")
	}

	sb.WriteString("Evaluate these candidates and provide your trade decision.")
	return sb.String()
}

type modeStat struct {
	positive int // WIN + PARTIAL_WIN + SKIP_MISSED
	negative int // LOSS + FORCE_SL + SKIP_VALIDATED
	score    float64
}

func renderCandidateMemory(sb *strings.Builder, trades []LLMSimilarTrade) {
	if len(trades) == 0 {
		sb.WriteString("  Memory: no similar past trades found.\n")
		return
	}

	sb.WriteString("  Similar past trades:\n")

	winCount, partialWinCount, lossCount, forceSLCount, breakevenCount, skipValidatedCount, skipMissedCount := 0, 0, 0, 0, 0, 0, 0
	memoryScore := 0.0
	modeStats := map[string]*modeStat{}

	for i, t := range trades {
		switch t.Outcome {
		case "WIN":
			winCount++
		case "PARTIAL_WIN":
			partialWinCount++
		case "LOSS":
			lossCount++
		case "FORCE_SL":
			forceSLCount++
		case "BREAKEVEN":
			breakevenCount++
		case "SKIP_VALIDATED":
			skipValidatedCount++
		case "SKIP_MISSED":
			skipMissedCount++
		}

		weight := t.Similarity
		switch {
		case t.DaysAgo <= 1:
			weight *= 1.30
		case t.DaysAgo <= 3:
			weight *= 1.20
		case t.DaysAgo <= 7:
			weight *= 1.10
		}
		s := outcomeScore(t.Outcome)
		memoryScore += s * weight

		// accumulate per-mode stats
		if t.EntryMode != "" {
			if modeStats[t.EntryMode] == nil {
				modeStats[t.EntryMode] = &modeStat{}
			}
			ms := modeStats[t.EntryMode]
			ms.score += s * weight
			switch t.Outcome {
			case "WIN", "PARTIAL_WIN", "SKIP_MISSED":
				ms.positive++
			case "LOSS", "FORCE_SL", "SKIP_VALIDATED":
				ms.negative++
			}
		}

		outcomeLabel := t.Outcome
		switch t.Outcome {
		case "SKIP_VALIDATED":
			outcomeLabel = "SKIP_VALIDATED (skip was correct)"
		case "SKIP_MISSED":
			outcomeLabel = "SKIP_MISSED (missed profitable trade)"
		case "FORCE_SL":
			outcomeLabel = "FORCE_SL (invalidated after entry)"
		}

		entryModeStr := ""
		if t.EntryMode != "" {
			entryModeStr = fmt.Sprintf(" | entry=%s", t.EntryMode)
		}
		fundingStr := ""
		if t.FundingRate != 0 {
			fundingStr = fmt.Sprintf(" | funding=%.2f%%", t.FundingRate)
		}
		profitSign := "+"
		if t.ProfitPct < 0 {
			profitSign = ""
		}

		sb.WriteString(fmt.Sprintf(
			"    [%d] %.0f%% match | %s | P&L: %s%.2f%%%s%s | %dd ago\n",
			i+1, t.Similarity*100, outcomeLabel, profitSign, t.ProfitPct, entryModeStr, fundingStr, t.DaysAgo,
		))
		if t.Lesson != "" {
			sb.WriteString(fmt.Sprintf("        → Lesson: %s\n", t.Lesson))
		}
	}

	positiveEvidence := winCount + partialWinCount + skipMissedCount
	negativeEvidence := lossCount + forceSLCount + skipValidatedCount

	summary := fmt.Sprintf("    Positive: %d (WIN=%d PARTIAL=%d MISSED=%d) | Negative: %d (LOSS=%d FORCE_SL=%d SKIP_OK=%d)",
		positiveEvidence, winCount, partialWinCount, skipMissedCount,
		negativeEvidence, lossCount, forceSLCount, skipValidatedCount)
	if breakevenCount > 0 {
		summary += fmt.Sprintf(" | Neutral: %d BREAKEVEN", breakevenCount)
	}
	sb.WriteString(summary + "\n")

	bias := biasLabel(memoryScore)
	sb.WriteString(fmt.Sprintf("    Memory bias: %s (score=%.2f)\n", bias, memoryScore))

	// Per-mode breakdown — only emit modes that have at least one trade.
	for _, mode := range []string{"FRONTRUN", "LAST_MINUTE", "AFTER"} {
		ms, ok := modeStats[mode]
		if !ok {
			continue
		}
		modeBias := biasLabel(ms.score)
		sb.WriteString(fmt.Sprintf("    %s history: %d positive / %d negative → %s\n",
			mode, ms.positive, ms.negative, modeBias))
	}

	// Actionable entry-mode signals.
	for _, mode := range []string{"FRONTRUN", "LAST_MINUTE", "AFTER"} {
		ms, ok := modeStats[mode]
		if !ok {
			continue
		}
		if ms.negative >= 2 && ms.positive == 0 {
			sb.WriteString(fmt.Sprintf("    WARNING: %s failed consistently in similar setups — avoid or require strong extra confirmation.\n", mode))
		} else if ms.positive >= 2 && ms.negative == 0 {
			sb.WriteString(fmt.Sprintf("    NOTE: %s worked well in similar setups.\n", mode))
		} else if ms.negative > ms.positive {
			sb.WriteString(fmt.Sprintf("    CAUTION: %s has more failures than wins in similar setups.\n", mode))
		}
	}

	if skipValidatedCount >= 2 {
		sb.WriteString("    WARNING: Multiple similar setups were correctly skipped — high failure risk.\n")
	}
	if lossCount+forceSLCount >= 2 {
		sb.WriteString("    WARNING: Multiple similar setups failed after entry.\n")
	}
	if winCount+skipMissedCount >= 3 {
		sb.WriteString("    NOTE: Similar setups frequently worked.\n")
	}
}

func outcomeScore(outcome string) float64 {
	switch outcome {
	case "WIN":
		return 1.0

	case "PARTIAL_WIN":
		return 0.5

	case "SKIP_MISSED":
		return 0.5

	case "LOSS":
		return -1.0

	case "FORCE_SL":
		return -0.75

	case "SKIP_VALIDATED":
		return -1.0

	default:
		return 0
	}
}

func biasLabel(score float64) string {
	switch {
	case score >= 2:
		return "STRONGLY POSITIVE"

	case score >= 0.5:
		return "MODERATELY POSITIVE"

	case score <= -2:
		return "STRONGLY NEGATIVE"

	case score <= -0.5:
		return "MODERATELY NEGATIVE"

	default:
		return "NEUTRAL"
	}
}
