package storage

import (
	"context"
	"time"

	"github.com/google/uuid"
	pgvector "github.com/pgvector/pgvector-go"

	"futures/internal/domain"
)

type StateCache interface {
	GetActivePositions(ctx context.Context) ([]domain.Position, error)
	GetActivePosition(ctx context.Context, symbol string) (*domain.Position, error)
	SetActivePosition(ctx context.Context, pos domain.Position) error
	SetActivePositionIfAbsent(ctx context.Context, pos domain.Position) (bool, error)
	RemovePosition(ctx context.Context, symbol string) error
	IsOnCooldown(ctx context.Context) (bool, error)
	SetCooldown(ctx context.Context, duration time.Duration) error
	IsSymbolOnCooldown(ctx context.Context, symbol string) (bool, error)
	SetSymbolCooldown(ctx context.Context, symbol string, duration time.Duration) error
	GetKillSwitch(ctx context.Context) (bool, error)
	SetKillSwitch(ctx context.Context, active bool) error
	AcquireSchedulerLock(ctx context.Context, window string, ttl time.Duration) (bool, error)
	SetFundingSnapshot(ctx context.Context, rates map[string]float64) error
	GetFundingSnapshot(ctx context.Context) (map[string]float64, error)

	// Phase 7: market order placed, waiting for WS fill confirmation before placing SL/TP
	SetPendingEntry(ctx context.Context, entry domain.PendingEntry) error
	GetPendingEntry(ctx context.Context, tradeID string) (*domain.PendingEntry, error)
	RemovePendingEntry(ctx context.Context, tradeID string) error
	GetAllPendingEntries(ctx context.Context) ([]domain.PendingEntry, error)

	// Phase 7: position open but SL/TP placement failed, needs retry
	SetPendingProtection(ctx context.Context, p domain.PendingProtection) error
	GetPendingProtection(ctx context.Context, symbol string) (*domain.PendingProtection, error)
	RemovePendingProtection(ctx context.Context, symbol string) error

	// Daily loss counter (resets at midnight UTC via TTL)
	GetDailyLossCount(ctx context.Context) (int, error)
	IncrDailyLossCount(ctx context.Context) error
}

type TradeRepository interface {
	Insert(ctx context.Context, trade *domain.Trade) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Trade, error)
	GetRecent(ctx context.Context, limit int) ([]domain.Trade, error)
	UpdateResult(ctx context.Context, id uuid.UUID, exit domain.ExitInfo) error
	GetDailyLossCount(ctx context.Context, date time.Time) (int, error)
	FindByOpenTimeRange(ctx context.Context, from, to time.Time, isPaper bool) ([]domain.Trade, error)
}

type MemorySearchResult struct {
	Memory     domain.TradeMemory
	Similarity float64 // 1 - cosine_distance (1.0 = identical)
}

type MemoryRepository interface {
	Insert(ctx context.Context, memory *domain.TradeMemory, embedding pgvector.Vector) error
	FindSimilar(ctx context.Context, embedding pgvector.Vector, limit int) ([]MemorySearchResult, error)
	UpdateOutcome(ctx context.Context, id uuid.UUID, outcome string, profitPct float64, holdMinutes int) error
	UpdateOutcomeByTradeID(ctx context.Context, tradeID uuid.UUID, outcome string, profitPct float64, holdMinutes int) error
	UpdateLesson(ctx context.Context, id uuid.UUID, lesson string) error
	GetByTradeID(ctx context.Context, tradeID uuid.UUID) (*domain.TradeMemory, error)
	GetPendingSkipValidations(ctx context.Context, olderThan time.Duration) ([]domain.TradeMemory, error)
}
