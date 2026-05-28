package telemetry

import (
	"context"
	"time"

	"github.com/google/uuid"

	"futures/internal/domain"
	"futures/internal/storage"
)

type InstrumentedTradeRepo struct {
	inner storage.TradeRepository
}

func NewInstrumentedTradeRepo(r storage.TradeRepository) storage.TradeRepository {
	return &InstrumentedTradeRepo{inner: r}
}

func (i *InstrumentedTradeRepo) Insert(ctx context.Context, trade *domain.Trade) error {
	start := time.Now()
	err := i.inner.Insert(ctx, trade)
	record("TradeRepository.Insert", time.Since(start), err, "symbol", trade.Symbol)
	return err
}

func (i *InstrumentedTradeRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Trade, error) {
	start := time.Now()
	res, err := i.inner.GetByID(ctx, id)
	record("TradeRepository.GetByID", time.Since(start), err, "id", id)
	return res, err
}

func (i *InstrumentedTradeRepo) GetRecent(ctx context.Context, limit int) ([]domain.Trade, error) {
	start := time.Now()
	res, err := i.inner.GetRecent(ctx, limit)
	record("TradeRepository.GetRecent", time.Since(start), err, "limit", limit, "returned", len(res))
	return res, err
}

func (i *InstrumentedTradeRepo) UpdateResult(ctx context.Context, id uuid.UUID, exit domain.ExitInfo) error {
	start := time.Now()
	err := i.inner.UpdateResult(ctx, id, exit)
	record("TradeRepository.UpdateResult", time.Since(start), err, "id", id, "result", exit.Result)
	return err
}

func (i *InstrumentedTradeRepo) GetDailyLossCount(ctx context.Context, date time.Time) (int, error) {
	start := time.Now()
	res, err := i.inner.GetDailyLossCount(ctx, date)
	record("TradeRepository.GetDailyLossCount", time.Since(start), err, "date", date.Format("2006-01-02"), "count", res)
	return res, err
}

func (i *InstrumentedTradeRepo) FindByOpenTimeRange(ctx context.Context, from, to time.Time, isPaper bool) ([]domain.Trade, error) {
	start := time.Now()
	res, err := i.inner.FindByOpenTimeRange(ctx, from, to, isPaper)
	record("TradeRepository.FindByOpenTimeRange", time.Since(start), err, "returned", len(res))
	return res, err
}
