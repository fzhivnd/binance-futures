package execution

import (
	"context"
	"log/slog"
	"time"

	"futures/internal/domain"
	"futures/internal/risk"
)

type BTCContextProvider interface {
	ComputeBTCContext(ctx context.Context) (*domain.BTCContext, error)
}

// IntentHandler is the execFn wired into the intent queue.
// It runs the fire-time risk check, sets leverage, then delegates to ExecutionEngine.
type IntentHandler struct {
	execEng    *ExecutionEngine
	riskEngine *risk.Engine
	indEngine  BTCContextProvider
	executor   Executor
	leverage   int
	minScore   float64
}

func NewIntentHandler(
	execEng *ExecutionEngine,
	riskEngine *risk.Engine,
	indEngine BTCContextProvider,
	executor Executor,
	leverage int,
	minScore float64,
) *IntentHandler {
	return &IntentHandler{
		execEng:    execEng,
		riskEngine: riskEngine,
		indEngine:  indEngine,
		executor:   executor,
		leverage:   leverage,
		minScore:   minScore,
	}
}

func (h *IntentHandler) Fire(ctx context.Context, sc *domain.ScoredCandidate, decision *domain.LLMDecision) error {
	fireStart := time.Now()

	if decision.EntryMode == domain.EntryModeLastMinute && sc.CompositeScore < h.minScore {
		slog.Info("intent rejected: LAST_MINUTE score too low",
			"symbol", sc.Candidate.Symbol,
			"score", sc.CompositeScore,
			"min", h.minScore,
		)
		return domain.ErrSkipped
	}

	btcAtFire, _ := h.indEngine.ComputeBTCContext(ctx)
	if err := h.riskEngine.EvaluateCandidate(ctx, sc, btcAtFire); err != nil {
		slog.Info("intent rejected by risk engine at fire time",
			"symbol", sc.Candidate.Symbol, "reason", err,
			"latency_ms", time.Since(fireStart).Milliseconds())
		return domain.ErrSkipped
	}

	if err := h.executor.SetLeverage(ctx, sc.Candidate.Symbol, h.leverage); err != nil {
		slog.Warn("set leverage failed before execution, continuing", "symbol", sc.Candidate.Symbol, "error", err)
	}

	err := h.execEng.ExecuteScoredWithLLM(ctx, sc, decision)
	slog.Info("intent_fired",
		"symbol", sc.Candidate.Symbol,
		"latency_ms", time.Since(fireStart).Milliseconds(),
		"error", err)
	return err
}
