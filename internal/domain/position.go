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
	Leverage       int
	EntryMode      EntryMode
	MarkPrice      float64 // last known mark price, updated each poll
	StopLoss       float64
	TakeProfit     float64
	SLOrderID      string
	TPOrderID      string
	TradeID        uuid.UUID
	OpenedAt       time.Time
	IsPaper        bool
	BreakevenMoved bool
}
