package memory

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"futures/internal/domain"
	"futures/internal/llm"
	"futures/internal/scheduler"
	"futures/internal/storage"
)

type Engine struct {
	embedder   *Embedder
	retriever  *Retriever
	summarizer *Summarizer
	repo       storage.MemoryRepository
	enabled    bool
}

type EngineConfig struct {
	Enabled      bool
	EmbedderCfg  EmbedderConfig
	RetrieverCfg RetrieverConfig
	LLMClient    *llm.Client
	Repo         storage.MemoryRepository
}

func NewEngine(cfg EngineConfig) *Engine {
	if !cfg.Enabled {
		return &Engine{enabled: false}
	}

	return &Engine{
		embedder:   NewEmbedder(cfg.EmbedderCfg),
		retriever:  NewRetriever(cfg.Repo, cfg.RetrieverCfg),
		summarizer: NewSummarizer(cfg.LLMClient),
		repo:       cfg.Repo,
		enabled:    true,
	}
}

// RecordTrade embeds and stores a completed trade. Called asynchronously after trade closes.
func (e *Engine) RecordTrade(
	ctx context.Context,
	trade *domain.Trade,
	snap *domain.IndicatorSnapshot,
	btc *domain.BTCContext,
	sc *domain.ScoredCandidate,
	decision *domain.LLMDecision,
) error {
	if !e.enabled {
		return nil
	}
	if snap == nil {
		slog.Warn("RecordTrade: missing indicator snapshot, skipping", "trade_id", trade.ID)
		return nil
	}
	if btc == nil {
		btc = &domain.BTCContext{}
	}

	entryMode := ""
	if decision != nil {
		entryMode = string(decision.EntryMode)
	}
	minutesToSettle := minutesToNextFunding(trade.CreatedAt)

	featureText := BuildFeatureText(snap, btc, &sc.Candidate, sc.CompositeScore, entryMode, minutesToSettle)

	start := time.Now()
	embedding, err := e.embedder.Embed(ctx, featureText)
	embedLatency := time.Since(start)
	if err != nil {
		slog.Error("failed to embed trade", "trade_id", trade.ID, "error", err)
		return err
	}

	mem := &domain.TradeMemory{
		ID:              uuid.New(),
		TradeID:         &trade.ID,
		Symbol:          trade.Symbol,
		Action:          domain.ActionTrade,
		CreatedAt:       trade.CreatedAt,
		FundingRate:     trade.FundingRate,
		DailyROI:        trade.Change24h,
		OIDelta1h:       snap.OIDelta1h,
		OIDelta15m:      snap.OIDelta15m,
		RSI14_15m:       snap.RSI14_15m,
		RSI7_5m:         snap.RSI7_5m,
		ATRRatio:        snap.ATRRatio,
		VolChange5m:     snap.VolChange5m,
		VolumeSpike:     snap.VolumeSpike,
		MomentumLoss:    snap.MomentumLoss,
		CandlePatterns:  formatPatterns(snap.Patterns),
		CompositeScore:  sc.CompositeScore,
		EntryMode:       entryMode,
		MinutesToSettle: minutesToSettle,
		BTCTrend:        btc.Trend,
		BTCMomentum:     btc.MomentumScore,
		BTCBreakout:     btc.IsBreakout,
		BTCRSI:          btc.RSI14_1h,
		BTCChange1h:     btc.PriceChange1h,
		Outcome:         trade.Result,
		ProfitPct:       trade.PnL,
		HoldMinutes:     holdMinutes(trade),
		BTCRegime:       BTCRegime(btc.Trend),
		FundingBucket:   FundingBucket(trade.FundingRate * 100),
	}

	if err := e.repo.Insert(ctx, mem, embedding); err != nil {
		return err
	}

	slog.Info("memory_recorded",
		"action", mem.Action,
		"symbol", mem.Symbol,
		"outcome", mem.Outcome,
		"lesson_length", len(mem.Lesson),
		"embed_latency_ms", embedLatency.Milliseconds(),
	)

	return nil
}

// RecordSkip embeds and stores a skip decision. Called asynchronously when LLM decides to skip.
func (e *Engine) RecordSkip(
	ctx context.Context,
	candidates []*domain.ScoredCandidate,
	btc *domain.BTCContext,
	skipReason string,
) error {
	if !e.enabled || len(candidates) == 0 {
		return nil
	}

	top := candidates[0]
	snap := top.Indicators
	minutesToSettle := minutesToNextFunding(time.Now().UTC())

	featureText := BuildFeatureText(snap, btc, &top.Candidate, top.CompositeScore, "SKIP", minutesToSettle)

	start := time.Now()
	embedding, err := e.embedder.Embed(ctx, featureText)
	embedLatency := time.Since(start)
	if err != nil {
		slog.Error("failed to embed skip decision", "error", err)
		return err
	}

	mem := &domain.TradeMemory{
		ID:              uuid.New(),
		Symbol:          top.Candidate.Symbol,
		Action:          domain.ActionSkip,
		CreatedAt:       time.Now(),
		FundingRate:     top.Candidate.FundingRate,
		DailyROI:        top.Candidate.DailyROI,
		OIDelta1h:       snap.OIDelta1h,
		OIDelta15m:      snap.OIDelta15m,
		RSI14_15m:       snap.RSI14_15m,
		RSI7_5m:         snap.RSI7_5m,
		ATRRatio:        snap.ATRRatio,
		VolChange5m:     snap.VolChange5m,
		VolumeSpike:     snap.VolumeSpike,
		MomentumLoss:    snap.MomentumLoss,
		CandlePatterns:  formatPatterns(snap.Patterns),
		CompositeScore:  top.CompositeScore,
		EntryMode:       "SKIP",
		MinutesToSettle: minutesToSettle,
		BTCTrend:        btc.Trend,
		BTCMomentum:     btc.MomentumScore,
		BTCBreakout:     btc.IsBreakout,
		BTCRSI:          btc.RSI14_1h,
		BTCChange1h:     btc.PriceChange1h,
		BTCRegime:       BTCRegime(btc.Trend),
		FundingBucket:   FundingBucket(top.Candidate.FundingRate * 100),
		// Outcome left empty — filled by skip validation job
	}

	if err := e.repo.Insert(ctx, mem, embedding); err != nil {
		return err
	}

	slog.Info("memory_recorded",
		"action", mem.Action,
		"symbol", mem.Symbol,
		"skip_reason", skipReason,
		"embed_latency_ms", embedLatency.Milliseconds(),
	)

	return nil
}

// RetrieveSimilar finds similar historical setups for the current candidate.
// Called synchronously before LLM decision (~120ms overhead).
// Returns nil,nil if memory is disabled or embedding fails (non-fatal).
func (e *Engine) RetrieveSimilar(
	ctx context.Context,
	snap *domain.IndicatorSnapshot,
	btc *domain.BTCContext,
	candidate *domain.Candidate,
	score float64,
	entryMode string,
	minutesToSettle int,
) ([]domain.SimilarTrade, error) {
	if !e.enabled {
		return nil, nil
	}

	featureText := BuildFeatureText(snap, btc, candidate, score, entryMode, minutesToSettle)

	start := time.Now()
	embedding, err := e.embedder.Embed(ctx, featureText)
	if err != nil {
		slog.Warn("failed to embed for retrieval, skipping memory", "error", err)
		return nil, nil // non-fatal: proceed without memory
	}

	btcRegime := BTCRegime(btc.Trend)
	fundingBucket := FundingBucket(candidate.FundingRate * 100)

	similar, err := e.retriever.FindSimilar(ctx, embedding, btcRegime, fundingBucket)
	queryLatency := time.Since(start)

	if err != nil {
		return nil, err
	}

	topSimilarity := 0.0
	winCount := 0
	for _, s := range similar {
		if s.Similarity > topSimilarity {
			topSimilarity = s.Similarity
		}
		if s.Outcome == "WIN" || s.Outcome == "PARTIAL_WIN" || s.Outcome == "SKIP_MISSED" {
			winCount++
		}
	}

	slog.Info("memory_retrieved",
		"candidate", candidate.Symbol,
		"similar_count", len(similar),
		"top_similarity", fmt.Sprintf("%.2f", topSimilarity),
		"win_count", winCount,
		"query_latency_ms", queryLatency.Milliseconds(),
	)

	return similar, nil
}

// GenerateLessonForTrade fetches the stored memory for a trade, summarizes it, and persists the lesson.
// Called after trade close so the outcome fields are already populated.
func (e *Engine) GenerateLessonForTrade(ctx context.Context, tradeID uuid.UUID) {
	if !e.enabled {
		return
	}
	mem, err := e.repo.GetByTradeID(ctx, tradeID)
	if err != nil {
		slog.Warn("GenerateLessonForTrade: memory not found", "trade_id", tradeID, "error", err)
		return
	}
	lesson, err := e.summarizer.Summarize(ctx, mem)
	if err != nil {
		slog.Warn("lesson summarization failed", "trade_id", tradeID, "error", err)
		return
	}
	if lesson == "" {
		return
	}
	if err := e.repo.UpdateLesson(ctx, mem.ID, lesson); err != nil {
		slog.Error("GenerateLessonForTrade: update lesson", "trade_id", tradeID, "error", err)
	}
}

// ValidateSkips checks skipped trades to determine if the skip was correct.
// checkPrice scans candles over 2h and returns the outcome and hypothetical profit pct.
func (e *Engine) ValidateSkips(ctx context.Context, checkPrice func(symbol string, entryTime time.Time) (string, float64, error)) error {
	if !e.enabled {
		return nil
	}

	pending, err := e.repo.GetPendingSkipValidations(ctx, 110*time.Minute)
	if err != nil {
		return err
	}
	slog.Info("start skip validations", "pending", len(pending))
	for _, mem := range pending {
		slog.Info("skip validation", "symbol", mem.Symbol, "createdAt", mem.CreatedAt)
		outcome, profitPct, err := checkPrice(mem.Symbol, mem.CreatedAt)
		tradeROI := profitPct * 20
		if err != nil {
			slog.Warn("skip validation price check failed", "symbol", mem.Symbol, "error", err)
			continue
		}

		if err := e.repo.UpdateOutcome(ctx, mem.ID, outcome, tradeROI, 0); err != nil {
			slog.Error("failed to update skip outcome", "id", mem.ID, "error", err)
			continue
		}

		mem.Outcome = outcome
		mem.ProfitPct = tradeROI
		lesson, _ := e.summarizer.Summarize(ctx, &mem)
		if lesson != "" {
			_ = e.repo.UpdateLesson(ctx, mem.ID, lesson)
		}

		slog.Info("skip_validated",
			"id", mem.ID,
			"symbol", mem.Symbol,
			"outcome", outcome,
			"hypothetical_pnl", profitPct,
		)
	}

	return nil
}

func formatPatterns(patterns []domain.CandleSignal) string {
	if len(patterns) == 0 {
		return ""
	}
	parts := make([]string, len(patterns))
	for i, p := range patterns {
		parts[i] = fmt.Sprintf("%s:%s(%s)", p.Timeframe, p.Pattern, p.Strength)
	}
	return strings.Join(parts, " ")
}

func holdMinutes(trade *domain.Trade) int {
	if trade.ClosedAt == nil {
		return 0
	}
	return int(trade.ClosedAt.Sub(trade.CreatedAt).Minutes())
}

func minutesToNextFunding(from time.Time) int {
	next := scheduler.NextFundingTime(from)
	if next.IsZero() {
		return 0
	}
	return int(math.Round(next.Sub(from).Minutes()))
}
