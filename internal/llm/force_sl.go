package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	openai "github.com/openai/openai-go"
)

const forceSLSystemPrompt = `You are a position management AI for a Binance Futures funding-rate shorting bot.
Your ONLY job: decide whether to force-close an open SHORT position early or hold.

CONTEXT:
- We are SHORT (profit when price goes down)
- Hard SL is set at 5% adverse price move (triggers automatically if reached)
- Your job: detect when the trade thesis has INVALIDATED and close early to limit loss
- Force-closing at -2% is better than waiting for -5% if the setup is dead

FUNDING FEE MECHANICS:
- We short on NEGATIVE funding. Shorts PAY longs at settlement.
- If we held through settlement → we PAID the funding fee (added cost on top of any loss)
- "Effective loss" = unrealized PnL + funding fee paid. This is the TRUE damage.
- A position showing -1.5% raw loss that also paid 0.8% fee = -2.3% effective loss.

═══════════════════════════════════════════════════════════════════════════════════
THESIS INVALIDATION PER ENTRY MODE:
═══════════════════════════════════════════════════════════════════════════════════

FRONTRUN entry thesis: "Price will dump BEFORE settlement due to pre-settlement selling."
  Invalidation signals:
  • Settlement has passed and we're still losing → thesis partially failed (pre-dump didn't happen) AND we paid the fee. Very strong cut signal.
  • If post-settlement AND losing: the pre-dump didn't happen and fee was paid. Cut unless strong delayed reversal signs.
  • If pre-settlement AND losing: still within thesis window. More lenient on hold.

LAST_MINUTE entry thesis: "Dump is starting near settlement, momentum carries through."
  Invalidation signals:
  • Settlement passed + price ABOVE entry → dump didn't materialize, we paid the fee. Lower bar for FORCE_CLOSE.
  • Price consolidating above entry with no downward momentum post-settlement → buyers absorbed selling. Thesis dead.
  • >15 minutes post-settlement with no progress toward TP → momentum exhausted.

AFTER entry thesis: "Post-settlement panic dump pushes price down 2% from entry."
  Invalidation signals:
  • >10 minutes post-entry with price flat or rising → panic dump didn't happen. Lower bar for cut.
  • >30 minutes with < 0.5% favorable move → dump momentum fully exhausted. Strong FORCE_CLOSE signal.
  • Price made higher high after entry → buyers absorbed the panic selling.
  • NOTE: AFTER has NO fee overhead. Raw loss IS the true loss. Slightly more lenient on small losses.

═══════════════════════════════════════════════════════════════════════════════════
GENERAL RULES (apply to all modes):
═══════════════════════════════════════════════════════════════════════════════════

WHEN TO FORCE_CLOSE:
- Strong momentum building AGAINST us (sustained buying, not just a wick)
- BTC has started a breakout that will drag the alt up
- Price consolidated above entry with increasing volume (buyers absorbing sells)
- The original entry thesis has clearly failed (see mode-specific signals above)
- Price made a higher high after entry and is holding above it
- Effective loss (raw + fee) exceeds -3% → thesis deeply underwater, don't wait for -5%

WHEN TO HOLD:
- Price is just ranging/consolidating near entry (normal noise)
- Temporary wick above entry but price returned
- We are in profit (price below entry) — thesis working
- The mode-specific thesis is still intact
- Current loss is small AND no clear directional signal against us
- BTC is not in breakout mode
- For AFTER entries: still within first 10 minutes (thesis needs time to play out)

AGGRESSION CALIBRATION:
- Effective loss > -3%: Be more aggressive about cutting.
- Fee paid + losing: Extra reason to cut (fee is sunk cost, but holding risks more).
- AFTER entry + flat after 30min: Strong cut signal. The dump window is gone.
- FRONTRUN post-settlement + losing: Cut aggressively. Original thesis window passed.
- Low original confidence (60-69): Lower bar for FORCE_CLOSE.

BIAS: Lean toward HOLD when thesis is intact. Lean toward FORCE_CLOSE when:
(a) thesis time window has expired, (b) fee was paid on top of loss, or
(c) effective loss > -3%. The hard SL at -5% is the WORST case, not the target.

CRITICAL: You are evaluating at 20x leverage. A 2% price move = 40% account impact.`

type ForceSLEngine struct {
	client *Client
}

func NewForceSLEngine(client *Client) *ForceSLEngine {
	return &ForceSLEngine{client: client}
}

// EvaluatePosition decides whether to force-close an open position.
// Uses a short timeout (5s) since this runs on a per-minute cadence per position.
// On any failure defaults to HOLD — the hard SL is always the backstop.
func (e *ForceSLEngine) EvaluatePosition(ctx context.Context, req *ForceSLRequest, timeoutSec int) (*ForceSLResponse, error) {
	if timeoutSec <= 0 {
		timeoutSec = 5
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	userMsg := buildForceSLUserMessage(req)

	raw, err := e.callForceSL(callCtx, userMsg)
	if err != nil {
		slog.Warn("force-SL LLM call failed, defaulting to HOLD", "error", err, "symbol", req.Symbol)
		return &ForceSLResponse{Action: "HOLD", Reason: "LLM unavailable"}, nil
	}

	var resp ForceSLResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		slog.Warn("force-SL response parse failed, defaulting to HOLD", "error", err, "raw", raw)
		return &ForceSLResponse{Action: "HOLD", Reason: "parse error"}, nil
	}
	if resp.Action != "HOLD" && resp.Action != "FORCE_CLOSE" {
		slog.Warn("force-SL unexpected action, defaulting to HOLD", "action", resp.Action)
		return &ForceSLResponse{Action: "HOLD", Reason: "unexpected action"}, nil
	}

	slog.Info("force_sl_check",
		"symbol", req.Symbol,
		"action", resp.Action,
		"reason", resp.Reason,
		"unrealized_pnl_pct", req.UnrealizedPnlPct,
		"hold_minutes", req.HoldMinutes,
	)
	return &resp, nil
}

func (e *ForceSLEngine) callForceSL(ctx context.Context, userMsg string) (string, error) {
	if !e.client.limiter.Allow() {
		return "", errors.New("rate limit exceeded")
	}

	schemaBytes, _ := json.Marshal(forceSLSchema())
	resp, err := e.client.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(e.client.cfg.Model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(forceSLSystemPrompt),
			openai.UserMessage(userMsg),
		},
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &openai.ResponseFormatJSONSchemaParam{
				JSONSchema: openai.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:   "force_sl_decision",
					Strict: openai.Bool(true),
					Schema: schemaBytes,
				},
			},
		},
	})
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("empty response from LLM")
	}
	return resp.Choices[0].Message.Content, nil
}

func forceSLSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{"HOLD", "FORCE_CLOSE"},
			},
			"reason": map[string]any{
				"type":        "string",
				"description": "1-2 sentence explanation of the decision",
			},
		},
		"required":             []string{"action", "reason"},
		"additionalProperties": false,
	}
}

func buildForceSLUserMessage(req *ForceSLRequest) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Position: %s SHORT\n", req.Symbol))
	sb.WriteString(fmt.Sprintf("Entry: $%.8g | Current: $%.8g\n", req.EntryPrice, req.CurrentPrice))
	sb.WriteString(fmt.Sprintf("Unrealized PnL: %.2f%% (raw price move at 20x)\n", req.UnrealizedPnlPct))

	// Phase 8: funding fee context
	if req.FundingFeePaid {
		sb.WriteString(fmt.Sprintf("Funding fee PAID: %.2f%% (held through settlement)\n", req.FundingFeePaidPct))
		sb.WriteString(fmt.Sprintf("Effective loss: %.2f%% (PnL + fee paid)\n", req.EffectiveLossPct))
	} else {
		sb.WriteString(fmt.Sprintf("Funding rate at entry: %.2f%% | Fee: NOT yet paid\n", req.FundingRatePct))
	}

	sb.WriteString(fmt.Sprintf("Hold time: %d minutes\n", req.HoldMinutes))
	sb.WriteString(fmt.Sprintf("Hard SL at: $%.8g (%.2f%% away)\n", req.HardSLPrice, req.HardSLDistancePct))
	sb.WriteString(fmt.Sprintf("Entry mode: %s | Original confidence: %d/100\n", req.EntryMode, req.OriginalConfidence))

	// Phase 8: settlement context
	if req.SettlementPassed {
		sb.WriteString(fmt.Sprintf("Settlement: PASSED (%d minutes ago)\n", req.MinutesSinceSettle))
	} else {
		sb.WriteString("Settlement: NOT YET PASSED (still in pre-settlement window)\n")
	}
	if req.TPWidened {
		sb.WriteString("TP status: WIDENED at T-2m (adjusted to cover funding fee)\n")
	}
	sb.WriteString("\n")

	if len(req.EntryReasons) > 0 {
		sb.WriteString("Original entry reasons:\n")
		for _, r := range req.EntryReasons {
			sb.WriteString(fmt.Sprintf("- %s\n", r))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("=== PRICE ACTION SINCE ENTRY ===\n")
	sb.WriteString(fmt.Sprintf("Max adverse move: +%.2f%% (against us)\n", req.PriceAction.HighSinceEntry))
	sb.WriteString(fmt.Sprintf("Max favorable move: -%.2f%% (in our favor)\n", req.PriceAction.LowSinceEntry))
	sb.WriteString(fmt.Sprintf("Current 5m trend: %s\n", req.PriceAction.CurrentTrend5m))
	sb.WriteString(fmt.Sprintf("Momentum shift (reversal against us): %v\n", req.PriceAction.MomentumShift))
	sb.WriteString(fmt.Sprintf("Volume increasing (buying pressure): %v\n\n", req.PriceAction.VolumeIncreasing))

	sb.WriteString("=== BTC CONTEXT ===\n")
	sb.WriteString(fmt.Sprintf("Trend: %s | Momentum: %d/100\n", req.BTCContext.Trend, req.BTCContext.MomentumScore))
	sb.WriteString(fmt.Sprintf("Breakout: %v | RSI(14): %.1f\n\n", req.BTCContext.IsBreakout, req.BTCContext.RSI))

	sb.WriteString("Should we FORCE_CLOSE this position now or HOLD?")
	return sb.String()
}
