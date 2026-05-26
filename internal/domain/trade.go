package domain

import (
	"time"

	"github.com/google/uuid"
)

type Trade struct {
	ID          uuid.UUID
	Symbol      string
	Side        Side
	EntryMode   EntryMode
	Leverage    int
	Confidence  int
	FundingRate float64
	DailyROI    float64
	EntryPrice  float64
	ExitPrice   float64
	PnL         float64
	Result      string
	IsPaper     bool
	CreatedAt   time.Time
	ClosedAt    *time.Time
}

type ExitInfo struct {
	ExitPrice float64
	PnL       float64
	Result    string
	ClosedAt  time.Time
}
