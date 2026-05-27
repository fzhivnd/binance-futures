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

const forceSLSystemPrompt = `You are a position management AI for a Binance Futures funding-rate shorting bot. Your ONLY job is to decide whether to force-close an open SHORT position early (before the hard stop-loss is hit) or hold.

CONTEXT:
- We are SHORT (profit when price goes down)
- Hard SL is set at 5% adverse price move (will trigger automatically if reached)
- Your job: detect when the trade thesis has INVALIDATED and close early to limit loss
- Force-closing at -2% is much better than waiting for -5% hard SL if the setup is dead

WHEN TO FORCE_CLOSE:
- Strong momentum building AGAINST us (sustained buying, not just a wick)
- BTC has started a breakout that will drag the alt up
- Price has consolidated above entry with increasing volume (buyers absorbing sells)
- The original entry thesis (funding reversal, exhaustion) has clearly failed
- Price made a higher high after entry and is holding above it

WHEN TO HOLD:
- Price is just ranging/consolidating around entry (normal noise)
- Temporary wick above entry but price returned
- We are already in profit (price below entry)
- The original thesis is still intact (no momentum shift)
- Current loss is small (<1% unrealized) and no clear directional signal
- BTC is not in breakout mode

BIAS: Lean toward HOLD unless the evidence for thesis invalidation is clear. Forcing a close on noise is expensive (fees + missed recovery). But don't stubbornly hold into a -3% loss when momentum is clearly against us — the hard SL at -5% is the absolute worst case, not the target.

CRITICAL: You are evaluating at 20x leverage. A 2% price move = 40% account impact on this position.`

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
	sb.WriteString(fmt.Sprintf("Unrealized PnL: %.2f%% (at 20x leverage)\n", req.UnrealizedPnlPct))
	sb.WriteString(fmt.Sprintf("Hold time: %d minutes\n", req.HoldMinutes))
	sb.WriteString(fmt.Sprintf("Hard SL at: $%.8g (%.2f%% away)\n", req.HardSLPrice, req.HardSLDistancePct))
	sb.WriteString(fmt.Sprintf("Entry mode: %s | Original confidence: %d/100\n\n", req.EntryMode, req.OriginalConfidence))

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
