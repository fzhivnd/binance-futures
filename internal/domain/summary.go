package domain

import "time"

type DailySummary struct {
	ID         int       `json:"id"`
	TradeDate  time.Time `json:"trade_date"`
	TradeCount int       `json:"trade_count"`
	WinCount   int       `json:"win_count"`
	LossCount  int       `json:"loss_count"`
	WinRate    float64   `json:"win_rate"`
	TotalPnL   float64   `json:"total_pnl"`
	IsPaper    bool      `json:"is_paper"`
	CreatedAt  time.Time `json:"created_at"`
}
