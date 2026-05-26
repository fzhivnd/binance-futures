package backtest

import (
	"time"

	"futures/internal/domain"
	"futures/internal/scoring"
)

type Config struct {
	StartDate      time.Time
	EndDate        time.Time
	Symbols        []string // empty = top symbols by funding
	InitialBalance float64
	Leverage       int
	Weights        scoring.WeightConfig
	MinScore       float64
	MaxATRRatio    float64
}

type Result struct {
	TotalTrades    int
	WinRate        float64
	AvgProfit      float64
	AvgLoss        float64
	TotalPnL       float64
	MaxDrawdownPct float64
	SharpeRatio    float64
	Trades         []Trade
}

type Trade struct {
	Timestamp      time.Time
	Symbol         string
	FundingRate    float64
	CompositeScore float64
	Confidence     string
	EntryPrice     float64
	ExitPrice      float64
	PnL            float64
	Result         string
	HoldDuration   time.Duration
	Breakdown      domain.ScoreBreakdown
}
