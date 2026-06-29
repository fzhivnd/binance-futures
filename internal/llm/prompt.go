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

COMPOSITE SCORE

Each candidate carries a pre-computed composite score (can be negative; typical range 20–60).
It aggregates funding rate intensity, OI expansion, BTC context, candle patterns, volume, RSI divergence, volatility, and daily ROI into a single ranked signal. Bullish candle patterns and momentum flags apply penalties that can push it negative.

Use the score as a prior:
- High score → strong multi-factor confluence; you need fewer manual confirmations.
- Low score → weak fundamentals; require clearer candle/RSI signals before selecting.
- The score breakdown (per component) is shown per candidate — use it to identify what is driving or limiting the score.

The score is an input, not a decision. You still evaluate context, squeeze risk, and mode fit.

MEMORY

You may receive SIMILAR PAST TRADES for each candidate.

Outcome categories:
POSITIVE: WIN | PARTIAL_WIN | SKIP_MISSED
NEGATIVE: LOSS | FORCE_SL | SKIP_VALIDATED
NEUTRAL:  BREAKEVEN

Each candidate includes two memory signals:

1. MODE WIN RATES — computed from up to 200 similar setups (broad statistical base):
   "<MODE>: W/T trades won (X% win rate, L losses)"
   Use this as the primary statistical signal for mode selection.
   - Win rate ≥ 60%: mode has a meaningful edge in similar setups.
   - Win rate 40–60%: mixed — weight current signals more heavily.
   - Win rate < 40%: mode has historically underperformed — require stronger confirmation or avoid.
   - Low sample count (Total < 5): treat as weak signal regardless of rate.

2. SIMILAR PAST TRADES — top 5 most similar individual setups (qualitative detail):
   Shows outcome, P&L, entry mode, and any LLM-generated lesson.
   A per-mode bias label and WARNING/CAUTION lines are derived from these 5 trades.
   Use these for pattern recognition and lesson extraction, not for win rate statistics.

When the two signals conflict (e.g. high win rate but recent similar trades failed):
- Favour the win rates for statistical confidence.
- Favour the individual trades for regime-shift awareness (recent losses may signal a changing market).

Memory bias adjusts confidence. Mode win rates adjust which window to enter.
Current market conditions always take priority over memory.

Strongly positive win rate (≥ 60%): Small confidence boost.
Strongly negative win rate (< 40%): Require stronger evidence or escalate mode (FRONTRUN → LAST_MINUTE → AFTER → SKIP).

Per-mode history (from individual trades):
- Consistent failures in a mode: Avoid that mode or escalate to the next safer window.
- Consistent wins in a mode: Prefer that mode when the time window permits.
- Mixed history: Default to current signal quality.

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
- TP2 ≈ 4% price movement

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
Post-settlement panic selling. Intent is queued now, executed after settlement.

Best when ANY of these are true:
- Funding extremely negative (-1.25% to -2.0%) — post-settlement dump is more likely.
- Pre-settlement reversal signals are absent or weak.
- Squeeze risk is elevated (BTC breakout, rising OI with no reversal, high ATR with no directional signal).
- ATRRatio > 2.5 and no clear bearish candle pattern.

Use AFTER when you like the symbol but don't trust the pre-settlement timing.
Advantage: no funding fee paid, avoids the pre-settlement squeeze window.

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
65-69 Marginal.
Below 65 Return SKIP.

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

MODE TIMING CONSTRAINTS

You are called during the FRONTRUN window (T-20m to T-4m before settlement).
All three modes are valid choices at this point:
- FRONTRUN: enters immediately.
- LAST_MINUTE: intent queued, fires when T-6m window opens.
- AFTER: intent queued, fires after settlement occurs.

Pick based on setup quality, not on time remaining.
Time remaining is a constraint on execution, not a preference signal.

OUTPUT RULES

- Select exactly one candidate or SKIP.
- Confidence reflects belief in the chosen plan.
- Confidence < 65 = SKIP.
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

	sb.WriteString(fmt.Sprintf("Next funding settlement in: %dm\n\n", req.MinutesToSettlement))
	sb.WriteString("Available entry modes for this evaluation:\n")
	sb.WriteString("  FRONTRUN   — enter now, before settlement. Requires clear reversal signals already visible.\n")
	sb.WriteString("  LAST_MINUTE — enter close to settlement. Use when setup is building but not yet confirmed.\n")
	sb.WriteString("  AFTER      — queue for post-settlement entry. Use when signals are unclear, squeeze risk is elevated, or ATR is very high.\n")
	sb.WriteString("  SKIP       — no trade this cycle.\n")
	sb.WriteString("Pick the mode that best fits the setup quality. Do not anchor to the current time window.\n\n")

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
		if c.BullishMomentum || c.BearishMomentumWeak {
			sb.WriteString("  Momentum flags:")
			if c.BullishMomentum {
				sb.WriteString(" BULLISH_MOMENTUM(squeeze risk — RSI rising on both TFs, OI expanding)")
			}
			if c.BearishMomentumWeak {
				sb.WriteString(" BEARISH_MOMENTUM_WEAK(stale setup — short-term RSI already reversed below 50)")
			}
			sb.WriteString("\n")
		}
		if len(c.CandlePatterns) > 0 {
			sb.WriteString("  Bearish candle patterns: ")
			for _, cp := range c.CandlePatterns {
				sb.WriteString(fmt.Sprintf("[%s:%s(%s)] ", cp.Timeframe, cp.Pattern, cp.Strength))
			}
			sb.WriteString("\n")
		}
		if len(c.BullishPatterns) > 0 {
			sb.WriteString("  Bullish candle patterns (penalty): ")
			for _, cp := range c.BullishPatterns {
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

		renderModeWinRates(&sb, c.ModeWinRates)
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

func renderModeWinRates(sb *strings.Builder, rates map[string]LLMModeWinRate) {
	if len(rates) == 0 {
		return
	}
	sb.WriteString("  Mode win rates (from up to 200 similar setups):\n")
	for _, mode := range []string{"FRONTRUN", "LAST_MINUTE", "AFTER"} {
		wr, ok := rates[mode]
		if !ok || wr.Total == 0 {
			continue
		}
		winPct := float64(wr.Wins) / float64(wr.Total) * 100
		sb.WriteString(fmt.Sprintf("    %s: %d/%d trades won (%.0f%% win rate, %d losses)\n",
			mode, wr.Wins, wr.Total, winPct, wr.Losses))
	}
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
