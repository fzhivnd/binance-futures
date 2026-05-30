package telemetry

import (
	"context"
	"time"

	"github.com/google/uuid"
	pgvector "github.com/pgvector/pgvector-go"

	"futures/internal/domain"
	"futures/internal/storage"
)

type InstrumentedMemoryRepo struct {
	inner storage.MemoryRepository
}

func NewInstrumentedMemoryRepo(r storage.MemoryRepository) storage.MemoryRepository {
	return &InstrumentedMemoryRepo{inner: r}
}

func (i *InstrumentedMemoryRepo) Insert(ctx context.Context, memory *domain.TradeMemory, embedding pgvector.Vector) error {
	start := time.Now()
	err := i.inner.Insert(ctx, memory, embedding)
	record("MemoryRepository.Insert", time.Since(start), err, "symbol", memory.Symbol, "action", memory.Action)
	return err
}

func (i *InstrumentedMemoryRepo) FindSimilar(ctx context.Context, embedding pgvector.Vector, btcRegime string, fundingBucket int, limit int) ([]storage.MemorySearchResult, error) {
	start := time.Now()
	res, err := i.inner.FindSimilar(ctx, embedding, btcRegime, fundingBucket, limit)
	record("MemoryRepository.FindSimilar", time.Since(start), err,
		"btc_regime", btcRegime, "funding_bucket", fundingBucket, "returned", len(res))
	return res, err
}

func (i *InstrumentedMemoryRepo) UpdateOutcome(ctx context.Context, id uuid.UUID, outcome string, profitPct float64, holdMinutes int) error {
	start := time.Now()
	err := i.inner.UpdateOutcome(ctx, id, outcome, profitPct, holdMinutes)
	record("MemoryRepository.UpdateOutcome", time.Since(start), err, "id", id, "outcome", outcome)
	return err
}

func (i *InstrumentedMemoryRepo) UpdateOutcomeByTradeID(ctx context.Context, tradeID uuid.UUID, outcome string, profitPct float64, holdMinutes int) error {
	start := time.Now()
	err := i.inner.UpdateOutcomeByTradeID(ctx, tradeID, outcome, profitPct, holdMinutes)
	record("MemoryRepository.UpdateOutcomeByTradeID", time.Since(start), err, "trade_id", tradeID, "outcome", outcome)
	return err
}

func (i *InstrumentedMemoryRepo) UpdateLesson(ctx context.Context, id uuid.UUID, lesson string) error {
	start := time.Now()
	err := i.inner.UpdateLesson(ctx, id, lesson)
	record("MemoryRepository.UpdateLesson", time.Since(start), err, "id", id)
	return err
}

func (i *InstrumentedMemoryRepo) GetByTradeID(ctx context.Context, tradeID uuid.UUID) (*domain.TradeMemory, error) {
	start := time.Now()
	res, err := i.inner.GetByTradeID(ctx, tradeID)
	record("MemoryRepository.GetByTradeID", time.Since(start), err, "trade_id", tradeID, "found", res != nil)
	return res, err
}

func (i *InstrumentedMemoryRepo) GetPendingSkipValidations(ctx context.Context, olderThan time.Duration) ([]domain.TradeMemory, error) {
	start := time.Now()
	res, err := i.inner.GetPendingSkipValidations(ctx, olderThan)
	record("MemoryRepository.GetPendingSkipValidations", time.Since(start), err, "returned", len(res))
	return res, err
}
