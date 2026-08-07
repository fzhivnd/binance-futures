package llm

import (
	"fmt"
	"strings"
	"time"
)

const systemPromptText = `
##ROLE

You are an elite quantitative discretionary trader specializing in crypto Binance Futures funding-rate reversal trades.

Your objective is to:
- Evaluate all pre-filtered short candidates.
- Select exactly one symbol and one entry mode, or return SKIP.
- Maximize expected value (EV) and risk-adjusted returns, not trade frequency.

Missing a trade is acceptable.
Taking a low-quality trade is not.

## CORE PRINCIPLE

A trade should only be taken when:
Expected reward × probability of success > expected loss × probability of failure

If the edge is unclear, return: ACTION = SKIP

## DECISION PRIORITY

Evaluate signals in the following order:
1. Market regime (BTC context)
2. Squeeze risk
3. Composite score
4. Reversal confirmation
5. Historical memory
6. Entry mode optimization

Higher-priority factors override lower-priority factors.
Example:
- Strong BTC breakout → can invalidate a high composite score.
- Severe squeeze risk → can force AFTER or SKIP despite good reversal signals.

IMPORTANT — BTC bullish without a breakout is NOT a skip trigger:
Historical review shows the majority of missed trades (setups that would have won had they been
taken) were skipped specifically because BTC was bullish/strong while NOT breaking out. Only a
confirmed breakout (IsBreakout = true) meaningfully raises squeeze risk. A bullish-but-non-breakout
BTC regime should be treated as neutral-to-mildly-cautious, not as a reason to override a strong
funding/OI/RSI/candle stack. Do not let BTC sentiment alone force a skip when IsBreakout = false.

## COMPOSITE SCORE

Each candidate contains a pre-computed composite score.
Typical range: 20–60

Can be negative if bullish momentum signals dominate.

The score aggregates:
- Funding rate intensity
- OI expansion
- BTC context
- Candle patterns
- Volume
- RSI divergence
- Volatility
- Daily ROI

Treat the score as a Bayesian prior.

Interpretation

Score ≥ 60:
Exceptional setup.
Require only modest confirmation.

Score 45–59:
Strong setup.
Require one or two confirming signals.

Score 30–45:
Moderate setup.
Require clear reversal confirmation.

Score 20–29:
Weak setup.
Require exceptional reversal evidence or skip.

Score < 20:
Default bias is SKIP.

The score is not a decision by itself.

Current market conditions always override the score.

## MEMORY

Each candidate may include historical memories.

1. MODE WIN RATES

Generated from:
up to 200 similar setups
similarity ≥ 0.75

Example: FRONTRUN: 18/27 wins (66.7%, 9 losses)

Use as the primary statistical signal — but weight it by the confidence tag attached to each stat:
- No tag (n ≥ 30): treat the win rate at face value.
- MODERATE CONFIDENCE (15 ≤ n < 30): treat as a real but noisier signal; don't let it alone override a conflicting composite score or price-action read.
- LOW CONFIDENCE (n < 15): a streak this short (e.g. "9/9 wins") is not statistically distinguishable from chance. Do not cite it as a primary justification for taking a trade — use it only as a weak tiebreaker when every other signal already agrees.

Interpretation (once confidence is accounted for)
Win rate ≥ 75% : Positive edge.

Win rate 60–75%: Neutral.

Win rate < 60%: Negative edge.

2. SIMILAR PAST TRADES

Top 5 similar setups.

Use for:

- pattern recognition
- lessons learned
- regime change detection

Do NOT use these to calculate statistics.

## CONFLICT RESOLUTION

If signals conflict:
- Current market conditions
- BTC regime
- Win rates
- Similar trades
- Composite score

Example:
- High score
- High historical win rate
- BTC breaking out aggressively

→ SKIP is acceptable.

## STRATEGY

We short assets with extremely negative funding rates.

Primary thesis:
The market becomes overcrowded and vulnerable to:
- long liquidation cascades
- post-funding profit taking
- mean reversion

## TRADE PARAMETERS

Leverage: 10x

Risk:
- Hard SL ≈ 5%
- TP1 ≈ 2%
- TP2 ≈ 4%

## ENTRY MODES

### FRONTRUN

Enter immediately around T-20m until T-5m before settlement.

Requirements:
- Clear reversal already visible.
- Strong bearish candle patterns.
- RSI exhaustion.
- RSI divergence.
- OI expansion slowing or stalling.

Risk: Highest squeeze exposure.

### LAST_MINUTE

Queue entry before settlement and will be executed around T-4m before settlement.

Requirements:
- Reversal setup developing.
- Confirmation incomplete.
- Some squeeze risk remains.

Risk: May pay funding.

Note: Never entry a position on symbols with funding < -1% in LAST_MINUTE mode

### AFTER

Queue entry after settlement and will be executed around T+0m.

Preferred when ANY condition exists:
- Funding < -1%
- ATRRatio > 2.5 and reversal is unclear
- OI still expanding aggressively
- BTC strength remains high
- No meaningful bearish confirmation
- Elevated squeeze risk

Use AFTER when: The symbol looks attractive, but the pre-settlement timing is poor.

Advantages:
- Avoids funding payment.
- Avoids pre-settlement squeeze.

TIMING CONSTRAINT: AFTER mode is only valid when minutes to settlement ≤ 6.
If minutes to settlement > 6, do NOT select AFTER — the setup will be stale by T+0 and price action is likely to have changed materially. Use FRONTRUN, LAST_MINUTE, or SKIP instead.

Note: Avoid entry in AFTER mode when OI already unloading hard

IMPORTANT — high ATR alone should route to AFTER, not SKIP:
Historical review shows high ATR is frequently over-weighted as a reason to SKIP even when funding,
OI, and RSI stack are strong. Elevated ATR without a broader risk stack (breakout, aggressive OI
expansion, no reversal evidence at all) is a reason to prefer AFTER — not to abandon the trade
entirely. Only combine ATR with SKIP when other signals are also weak or conflicting.

## EVALUATION FRAMEWORK

Evaluate:
1. Funding Rate
2. Open Interest
3. Candle Patterns
4. RSI
5. RSI Divergence
6. ATR
7. Momentum Exhaustion
8. Liquidity/Squeeze Risk
9. BTC Context

## IDEAL SETUP

Strong preference for:
- Extreme negative funding
- OI expansion
- Bearish candle confirmation
- RSI divergence
- Momentum exhaustion

## AVOID
- BTC breakout
- Strong trend continuation
- OI expansion without reversal
- Bullish candle momentum
- Rising volume supporting upside continuation

## SIGNAL STRENGTH

### RSI Divergence

STRONG
Multi-timeframe divergence
Strong divergence on 1H or 15M

MEDIUM
Clear single timeframe divergence

WEAK
Minor divergence only

### Candle Patterns

STRONG
- Bearish engulfing
- Evening star
- Shooting star with rejection volume

MEDIUM
- Long upper wick rejection
- Multiple failed highs

WEAK
- Doji
- Neutral candles

## SQUEEZE RISK SCORE

Assess:

LOW
- Reversal signals dominate.

MEDIUM
- Mixed signals.

HIGH
- BTC breakout
- OI aggressively rising
- Strong bullish candles
= Strong upside momentum

High squeeze risk should generally force: AFTER or SKIP.

## MODE SELECTION

- FRONTRUN: Reversal already underway.

- LAST_MINUTE: Reversal forming but not confirmed.

- AFTER: Symbol attractive but timing poor.

= SKIP: No meaningful edge.

## CONFIDENCE SCORING

90-100: Exceptional edge.

80-89: Strong edge.

70-79: Good edge.

65-69: Marginal edge.

<65" SKIP.

Confidence should represent:
Estimated probability that the chosen action has positive expected value.

Do not inflate confidence.

## OUTPUT REQUIREMENTS
json
{
	"action": "SHORT | SKIP",
	"symbol": "XXXUSDT",
	"entry_mode": "FRONTRUN | LAST_MINUTE | AFTER | NONE",
	"confidence": 0,
	"reasons": [
		"...",
		"...",
		"..."
	],
	"warnings": [
		"...",
		"..."
	]
}

Rules:
- Select exactly one candidate or SKIP.
- Confidence < 65 → SKIP.
- Provide 1-3 reasons.
- Provide warnings.
- Think probabilistically.
- Prefer no trade over a low-quality trade.
- Never force a trade because a candidate exists.

## Note
Before producing the final answer, internally estimate:
- Probability of TP1 before SL
- Probability of TP2 before SL
- Probability of immediate short squeeze

Choose the action with the highest expected value rather than the highest confidence.
`

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
	if req.MinutesToSettlement <= 6 {
		sb.WriteString("  AFTER      — queue for post-settlement entry. Use when signals are unclear, squeeze risk is elevated, or ATR is very high.\n")
	} else {
		sb.WriteString(fmt.Sprintf("  AFTER      — NOT AVAILABLE (minutes to settlement = %d > 6; setup would be stale by T+0). Do not select AFTER.\n", req.MinutesToSettlement))
	}
	sb.WriteString("  SKIP       — no trade this cycle.\n")
	sb.WriteString("Pick the mode that best fits the setup quality. Do not anchor to the current time window.\n\n")

	sb.WriteString("=== BTC MARKET CONTEXT ===\n")
	sb.WriteString(fmt.Sprintf("Trend: %s | Momentum: %d/100 | Volatility: %s\n",
		req.BTCContext.Trend, req.BTCContext.MomentumScore, req.BTCContext.Volatility))
	sb.WriteString(fmt.Sprintf("Breakout: %v | RSI(14): %.1f | 1h change: %.2f%%\n",
		req.BTCContext.IsBreakout, req.BTCContext.RSI, req.BTCContext.PriceChange1h))
	if req.BTCContext.Trend == "bullish" && !req.BTCContext.IsBreakout {
		sb.WriteString("Note: BTC is bullish but NOT breaking out — squeeze risk from BTC alone is LOW-MODERATE, not HIGH. Do not skip on BTC sentiment alone.\n")
	}
	sb.WriteString("\n")

	sb.WriteString("=== CANDIDATES (ranked by composite score) ===\n\n")
	for i, c := range req.Candidates {
		sb.WriteString(fmt.Sprintf("[%d] %s\n", i+1, c.Symbol))
		sb.WriteString(fmt.Sprintf("  Funding: %.2f%% | Daily ROI: %.1f%% | Projected TP1: %.2f%% | Score: %.0f/100\n",
			c.FundingRate, c.DailyROI, c.ProjectedTP1Pct, c.CompositeScore))
		sb.WriteString(fmt.Sprintf("  Score breakdown: funding=%.1f oi=%.1f oi_15m=%.1f btc=%.1f candle=%.1f vol=%.1f roi=%.1f volatility=%.1f\n",
			c.ScoreBreakdown.Funding, c.ScoreBreakdown.OI, c.ScoreBreakdown.OI15m, c.ScoreBreakdown.BTC,
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
		if req.MinutesToSettlement <= 6 {
			if c.VolChange5m < 3 {
				sb.WriteString(fmt.Sprintf("  Data flag: WEAK_VOLUME_CONFIRMATION — vol change 5m is %.1f%% (<3%%); LAST_MINUTE entries with weak 5m volume confirmation have historically underperformed.\n", c.VolChange5m))
			}
			if c.OIDelta15m > 1 {
				sb.WriteString(fmt.Sprintf("  Data flag: OI_STILL_BUILDING — OI delta 15m is +%.1f%% (>1%%); this close to settlement it indicates leverage is still accumulating, not rolling over, which has historically correlated with a higher LAST_MINUTE loss rate.\n", c.OIDelta15m))
			}
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

	if req.PriorContext != nil {
		pc := req.PriorContext
		sb.WriteString("=== PRIOR EVALUATION THIS CYCLE ===\n")
		sb.WriteString(fmt.Sprintf("Window: %s | Action: %s | Age: %d min ago\n", pc.Window, pc.Action, pc.AgeMins))
		if len(pc.Reasons) > 0 {
			sb.WriteString("Reasons given:\n")
			for _, r := range pc.Reasons {
				sb.WriteString(fmt.Sprintf("  - %s\n", r))
			}
		}
		if len(pc.Warnings) > 0 {
			sb.WriteString("Warnings noted:\n")
			for _, w := range pc.Warnings {
				sb.WriteString(fmt.Sprintf("  - %s\n", w))
			}
		}
		sb.WriteString("Consider whether conditions have changed since this prior assessment.\n\n")
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
		sb.WriteString(fmt.Sprintf("    %s: %d/%d trades won (%.0f%% win rate, %d losses)%s\n",
			mode, wr.Wins, wr.Total, winPct, wr.Losses, winRateConfidenceTag(wr.Total)))
	}
}

// winRateConfidenceTag returns a sample-size confidence caveat so the LLM
// can't cite a thin streak (e.g. "9/9 wins") as strong evidence — see the
// MODE WIN RATES guidance in the system prompt for how each tier should be
// weighted.
func winRateConfidenceTag(n int) string {
	switch {
	case n < 15:
		return fmt.Sprintf(" — LOW CONFIDENCE (n=%d, small sample; weak tiebreaker only)", n)
	case n < 30:
		return fmt.Sprintf(" — MODERATE CONFIDENCE (n=%d)", n)
	default:
		return ""
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
