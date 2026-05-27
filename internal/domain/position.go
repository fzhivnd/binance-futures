package domain

import (
	"time"

	"github.com/google/uuid"
)

type PositionState string

const (
	PositionStateOpen   PositionState = "OPEN"
	PositionStateClosed PositionState = "CLOSED"
)

type Position struct {
	Symbol         string
	Side           Side
	EntryPrice     float64
	Quantity       float64
	OriginalQty    float64 // full quantity at entry (Phase 5)
	Leverage       int
	EntryMode      EntryMode
	MarkPrice      float64
	StopLoss       float64
	TakeProfit     float64
	SLOrderID      string
	TPOrderID      string
	TradeID        uuid.UUID
	OpenedAt       time.Time
	IsPaper        bool
	BreakevenMoved bool

	// Phase 5: Split TP
	TP1Filled         bool
	TP1FillPrice      float64
	TP1FilledAt       *time.Time
	TrailingOrderID   string
	TrailingSLOrderID string

	// Phase 5: Force-SL
	LastForceSLCheck   time.Time
	ForceSLCount       int
	LLMEntryReasons    []string
	OriginalConfidence int

	// Phase 5: Price tracking for force-SL context
	HighSinceEntry float64
	LowSinceEntry  float64
}
