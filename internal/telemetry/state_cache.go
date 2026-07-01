package telemetry

import (
	"context"
	"sync/atomic"
	"time"

	"futures/internal/domain"
	"futures/internal/storage"
)

type InstrumentedStateCache struct {
	inner      storage.StateCache
	pollTick   atomic.Uint64
	sampleRate uint64 // log GetActivePositions 1 in N calls (it runs every 1s)
}

// NewInstrumentedStateCache wraps c. sampleRate controls how often
// GetActivePositions is logged — pass 60 to log roughly once per minute.
func NewInstrumentedStateCache(c storage.StateCache, sampleRate uint64) storage.StateCache {
	if sampleRate == 0 {
		sampleRate = 1
	}
	return &InstrumentedStateCache{inner: c, sampleRate: sampleRate}
}

func (i *InstrumentedStateCache) GetActivePositions(ctx context.Context) ([]domain.Position, error) {
	start := time.Now()
	res, err := i.inner.GetActivePositions(ctx)
	if n := i.pollTick.Add(1); n%i.sampleRate == 0 {
		record("StateCache.GetActivePositions", time.Since(start), err, "count", len(res))
	}
	return res, err
}

func (i *InstrumentedStateCache) GetActivePosition(ctx context.Context, symbol string) (*domain.Position, error) {
	start := time.Now()
	res, err := i.inner.GetActivePosition(ctx, symbol)
	if n := i.pollTick.Add(1); n%i.sampleRate == 0 {
		record("StateCache.GetActivePosition", time.Since(start), err, "symbol", symbol)
	}
	return res, err
}

func (i *InstrumentedStateCache) SetActivePosition(ctx context.Context, pos domain.Position) error {
	start := time.Now()
	err := i.inner.SetActivePosition(ctx, pos)
	record("StateCache.SetActivePosition", time.Since(start), err, "symbol", pos.Symbol)
	return err
}

func (i *InstrumentedStateCache) SetActivePositionIfAbsent(ctx context.Context, pos domain.Position) (bool, error) {
	start := time.Now()
	set, err := i.inner.SetActivePositionIfAbsent(ctx, pos)
	record("StateCache.SetActivePositionIfAbsent", time.Since(start), err, "symbol", pos.Symbol, "written", set)
	return set, err
}

func (i *InstrumentedStateCache) RemovePosition(ctx context.Context, symbol string) error {
	start := time.Now()
	err := i.inner.RemovePosition(ctx, symbol)
	record("StateCache.RemovePosition", time.Since(start), err, "symbol", symbol)
	return err
}

func (i *InstrumentedStateCache) IsOnCooldown(ctx context.Context) (bool, error) {
	start := time.Now()
	res, err := i.inner.IsOnCooldown(ctx)
	record("StateCache.IsOnCooldown", time.Since(start), err, "on_cooldown", res)
	return res, err
}

func (i *InstrumentedStateCache) SetCooldown(ctx context.Context, d time.Duration) error {
	start := time.Now()
	err := i.inner.SetCooldown(ctx, d)
	record("StateCache.SetCooldown", time.Since(start), err, "duration", d)
	return err
}

func (i *InstrumentedStateCache) IsSymbolOnCooldown(ctx context.Context, symbol string) (bool, error) {
	start := time.Now()
	res, err := i.inner.IsSymbolOnCooldown(ctx, symbol)
	record("StateCache.IsSymbolOnCooldown", time.Since(start), err, "symbol", symbol, "on_cooldown", res)
	return res, err
}

func (i *InstrumentedStateCache) SetSymbolCooldown(ctx context.Context, symbol string, d time.Duration) error {
	start := time.Now()
	err := i.inner.SetSymbolCooldown(ctx, symbol, d)
	record("StateCache.SetSymbolCooldown", time.Since(start), err, "symbol", symbol, "duration", d)
	return err
}

func (i *InstrumentedStateCache) GetKillSwitch(ctx context.Context) (bool, error) {
	start := time.Now()
	res, err := i.inner.GetKillSwitch(ctx)
	record("StateCache.GetKillSwitch", time.Since(start), err, "active", res)
	return res, err
}

func (i *InstrumentedStateCache) SetKillSwitch(ctx context.Context, active bool) error {
	start := time.Now()
	err := i.inner.SetKillSwitch(ctx, active)
	record("StateCache.SetKillSwitch", time.Since(start), err, "active", active)
	return err
}

func (i *InstrumentedStateCache) AcquireSchedulerLock(ctx context.Context, window string, ttl time.Duration) (bool, error) {
	start := time.Now()
	res, err := i.inner.AcquireSchedulerLock(ctx, window, ttl)
	record("StateCache.AcquireSchedulerLock", time.Since(start), err, "window", window, "acquired", res)
	return res, err
}

func (i *InstrumentedStateCache) SetFundingSnapshot(ctx context.Context, rates map[string]float64) error {
	start := time.Now()
	err := i.inner.SetFundingSnapshot(ctx, rates)
	record("StateCache.SetFundingSnapshot", time.Since(start), err, "symbols", len(rates))
	return err
}

func (i *InstrumentedStateCache) GetFundingSnapshot(ctx context.Context) (map[string]float64, error) {
	start := time.Now()
	res, err := i.inner.GetFundingSnapshot(ctx)
	record("StateCache.GetFundingSnapshot", time.Since(start), err, "symbols", len(res))
	return res, err
}

func (i *InstrumentedStateCache) SetPendingEntry(ctx context.Context, entry domain.PendingEntry) error {
	start := time.Now()
	err := i.inner.SetPendingEntry(ctx, entry)
	record("StateCache.SetPendingEntry", time.Since(start), err, "symbol", entry.Symbol, "order_id", entry.OrderID)
	return err
}

func (i *InstrumentedStateCache) GetPendingEntry(ctx context.Context, orderID string) (*domain.PendingEntry, error) {
	start := time.Now()
	res, err := i.inner.GetPendingEntry(ctx, orderID)
	record("StateCache.GetPendingEntry", time.Since(start), err, "order_id", orderID, "found", res != nil)
	return res, err
}

func (i *InstrumentedStateCache) RemovePendingEntry(ctx context.Context, orderID string) error {
	start := time.Now()
	err := i.inner.RemovePendingEntry(ctx, orderID)
	record("StateCache.RemovePendingEntry", time.Since(start), err, "order_id", orderID)
	return err
}

func (i *InstrumentedStateCache) GetAllPendingEntries(ctx context.Context) ([]domain.PendingEntry, error) {
	start := time.Now()
	res, err := i.inner.GetAllPendingEntries(ctx)
	record("StateCache.GetAllPendingEntries", time.Since(start), err, "count", len(res))
	return res, err
}

func (i *InstrumentedStateCache) SetPendingProtection(ctx context.Context, p domain.PendingProtection) error {
	start := time.Now()
	err := i.inner.SetPendingProtection(ctx, p)
	record("StateCache.SetPendingProtection", time.Since(start), err, "symbol", p.Symbol)
	return err
}

func (i *InstrumentedStateCache) GetPendingProtection(ctx context.Context, symbol string) (*domain.PendingProtection, error) {
	start := time.Now()
	res, err := i.inner.GetPendingProtection(ctx, symbol)
	record("StateCache.GetPendingProtection", time.Since(start), err, "symbol", symbol, "found", res != nil)
	return res, err
}

func (i *InstrumentedStateCache) RemovePendingProtection(ctx context.Context, symbol string) error {
	start := time.Now()
	err := i.inner.RemovePendingProtection(ctx, symbol)
	record("StateCache.RemovePendingProtection", time.Since(start), err, "symbol", symbol)
	return err
}

func (i *InstrumentedStateCache) GetDailyLossCount(ctx context.Context) (int, error) {
	start := time.Now()
	res, err := i.inner.GetDailyLossCount(ctx)
	record("StateCache.GetDailyLossCount", time.Since(start), err, "count", res)
	return res, err
}

func (i *InstrumentedStateCache) IncrDailyLossCount(ctx context.Context) error {
	start := time.Now()
	err := i.inner.IncrDailyLossCount(ctx)
	record("StateCache.IncrDailyLossCount", time.Since(start), err)
	return err
}

func (i *InstrumentedStateCache) GetCachedBalance(ctx context.Context) (*domain.Balance, error) {
	start := time.Now()
	res, err := i.inner.GetCachedBalance(ctx)
	record("StateCache.GetCachedBalance", time.Since(start), err, "found", res != nil)
	return res, err
}

func (i *InstrumentedStateCache) SetCachedBalance(ctx context.Context, b *domain.Balance) error {
	start := time.Now()
	err := i.inner.SetCachedBalance(ctx, b)
	record("StateCache.SetCachedBalance", time.Since(start), err)
	return err
}
