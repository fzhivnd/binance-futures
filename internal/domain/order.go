package domain

import "time"

type Side string

const (
	SideBuy  Side = "BUY"
	SideSell Side = "SELL"
)

type OrderType string

const (
	OrderTypeMarket         OrderType = "MARKET"
	OrderTypeLimit          OrderType = "LIMIT"
	OrderTypeStop           OrderType = "STOP"                 // stop-limit SL (Binance Futures)
	OrderTypeStopMarket     OrderType = "STOP_MARKET"          // stop-market SL — guaranteed fill, no limit price
	OrderTypeTakeProfit     OrderType = "TAKE_PROFIT"          // take-profit-limit TP (Binance Futures)
	OrderTypeTrailingStop   OrderType = "TRAILING_STOP_MARKET" // native Binance trailing stop
)

type EntryMode string

const (
	EntryModeFrontrun   EntryMode = "FRONTRUN"
	EntryModeLastMinute EntryMode = "LAST_MINUTE"
	EntryModeAfter      EntryMode = "AFTER"
)

type OrderRequest struct {
	Symbol     string
	Side       Side
	Type       OrderType
	Quantity   float64
	Price      float64  // limit fill price
	StopPrice  float64  // trigger price for STOP / TAKE_PROFIT orders
	ReduceOnly bool
}

type OrderResult struct {
	OrderID   string
	Symbol    string
	Side      Side
	FillPrice float64
	Quantity  float64
	Status    string
	IsPaper   bool
	Timestamp time.Time
}

type Balance struct {
	Asset            string
	TotalBalance     float64
	AvailableBalance float64
}
