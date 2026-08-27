package llm

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"futures/internal/domain"
)

type DecisionEngine struct {
	client        *Client
	prompt        *PromptBuilder
	minConfidence int
}

func NewDecisionEngine(client *Client, minConfidence int) *DecisionEngine {
	return &DecisionEngine{
		client:        client,
		prompt:        NewPromptBuilder(),
		minConfidence: minConfidence,
	}
}

// Evaluate takes top scored candidates and returns a trade decision.
// tpPct is the base take-profit percentage from config (used to compute projected TP1).
// similarTrades maps each candidate symbol to its retrieved similar past trades.
// On any LLM failure it returns a SKIP decision (safe fallback).
func (e *DecisionEngine) Evaluate(
	ctx context.Context,
	candidates []*domain.ScoredCandidate,
	btc *domain.BTCContext,
	tpPct float64,
	similarTrades map[string][]domain.SimilarTrade,
	modeWinRates map[string]map[string]domain.ModeWinRate,
	priorCtx *LLMPriorContext,
) (*domain.LLMDecision, error) {
	req := MapToLLMRequest(candidates, btc, tpPct, similarTrades, modeWinRates, priorCtx)

	systemPrompt := e.prompt.SystemPrompt()
	userMessage := e.prompt.UserMessage(req)

	slog.Info("llm_request",
		"minutes_to_settlement", req.MinutesToSettlement,
		"btc_trend", req.BTCContext.Trend,
		"btc_momentum", req.BTCContext.MomentumScore,
	)
	for i, c := range req.Candidates {
		slog.Info("llm_request_candidate",
			"idx", i,
			"symbol", c.Symbol,
			"funding_rate_pct", c.FundingRate,
			"daily_roi_pct", c.DailyROI,
			"composite_score", c.CompositeScore,
			"score_funding", c.ScoreBreakdown.Funding,
			"score_oi", c.ScoreBreakdown.OI,
			"score_btc", c.ScoreBreakdown.BTC,
			"score_candle", c.ScoreBreakdown.Candle,
			"score_volume", c.ScoreBreakdown.Volume,
			"score_roi", c.ScoreBreakdown.ROI,
			"score_volatility", c.ScoreBreakdown.Volatility,
			"rsi_14_15m", c.RSI14_15m,
			"rsi_7_5m", c.RSI7_5m,
			"oi_delta_1h_pct", c.OIDelta1h,
			"oi_delta_15m_pct", c.OIDelta15m,
			"atr_ratio", c.ATRRatio,
			"vol_change_5m_pct", c.VolChange5m,
			"volume_spike", c.VolumeSpikeFlag,
			"momentum_loss", c.MomentumLoss,
			"candle_patterns", c.CandlePatterns,
			"rsi_divergence", c.RSIDivergences,
			"similar_trades", len(c.SimilarTrades),
		)
	}

	start := time.Now()
	raw, err := e.client.Call(ctx, systemPrompt, userMessage)
	elapsed := time.Since(start)

	if err != nil {
		slog.Warn("LLM call failed, falling back to SKIP",
			"error", err,
			"latency_ms", elapsed.Milliseconds(),
		)
		return &domain.LLMDecision{Action: "SKIP", SkipReason: "LLM unavailable: " + err.Error()}, nil
	}

	var resp LLMResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		slog.Warn("LLM response parse failed, falling back to SKIP",
			"error", err,
			"raw", raw,
		)
		return &domain.LLMDecision{Action: "SKIP", SkipReason: "LLM response invalid"}, nil
	}

	decision := mapResponseToDecision(resp, candidates)

	// Enforce minimum confidence threshold
	if decision.Action == "OPEN_SHORT" && decision.Confidence < e.minConfidence {
		decision.Action = "SKIP"
		decision.SkipReason = "LLM confidence below threshold"
	}

	candidateSymbols := make([]string, len(candidates))
	for i, sc := range candidates {
		candidateSymbols[i] = sc.Candidate.Symbol
	}

	if decision.Action == "SKIP" {
		slog.Info("llm_skip",
			"reason", decision.SkipReason,
			"candidates", candidateSymbols,
			"latency_ms", elapsed.Milliseconds(),
		)
	} else {
		slog.Info("llm_call",
			"candidates_count", len(candidates),
			"latency_ms", elapsed.Milliseconds(),
			"action", decision.Action,
			"symbol", decision.Symbol,
			"confidence", decision.Confidence,
			"entry_mode", decision.EntryMode,
		)
	}

	return decision, nil
}
