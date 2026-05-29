package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrSkipped is returned by execution when a trade was not placed due to a
// pre-check (cooldown, max positions, already in position, daily loss limit).
// Distinct from a real error so callers can keep the intent alive for retry.
var ErrSkipped = errors.New("execution skipped")

type Trade struct {
	ID          uuid.UUID
	Symbol      string
	Side        Side
	EntryMode   EntryMode
	Leverage    int
	Confidence  int
	FundingRate float64
	Change24h   float64
	ROIPct      *float64
	EntryPrice  float64
	Quantity    float64
	ExitPrice   float64
	PnL         float64
	Result      string
	IsPaper     bool
	CreatedAt   time.Time
	ClosedAt    *time.Time

	// Phase 3: LLM decision fields (nil/empty when LLM is disabled)
	LLMConfidence   *int
	LLMEntryMode    *string
	LLMEntryReasons []string
	LLMWarnings     []string

	// Phase 5: close tracking
	AvgClosePrice *float64
	CloseReason   *string
}

// Phase 5: simplified close record fields on Trade
// CloseReason: "TP_TRAIL" | "FORCE_SL" | "HARD_SL" | "MANUAL"
// AvgClosePrice: weighted average exit across all legs

type ExitInfo struct {
	AvgClosePrice float64
	PnL           float64
	ROIPct        float64
	Result        string // WIN | LOSS | PARTIAL_WIN | FORCE_SL | BREAKEVEN | MANUAL
	CloseReason   string // TP_TRAIL | FORCE_SL | HARD_SL | MANUAL
	ClosedAt      time.Time
}
