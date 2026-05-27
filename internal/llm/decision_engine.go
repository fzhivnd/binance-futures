package llm

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"futures/internal/domain"
)

type DecisionEngine struct {
	client *Client
	prompt *PromptBuilder
}

func NewDecisionEngine(client *Client) *DecisionEngine {
	return &DecisionEngine{
		client: client,
		prompt: NewPromptBuilder(),
	}
}

// Evaluate takes top scored candidates and returns a trade decision.
// tpPct is the base take-profit percentage from config (used to compute projected TP1).
// On any LLM failure it returns a SKIP decision (safe fallback).
func (e *DecisionEngine) Evaluate(
	ctx context.Context,
	candidates []*domain.ScoredCandidate,
	btc *domain.BTCContext,
	tpPct float64,
) (*domain.LLMDecision, error) {
	req := MapToLLMRequest(candidates, btc, tpPct)

	systemPrompt := e.prompt.SystemPrompt()
	userMessage := e.prompt.UserMessage(req)

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
	if decision.Action == "OPEN_SHORT" && decision.Confidence < 60 {
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
