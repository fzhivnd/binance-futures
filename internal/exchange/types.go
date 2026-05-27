package exchange

import "time"

// MarkPriceEvent is one entry from !markPrice@arr@1s
type MarkPriceEvent struct {
	EventType       string `json:"e"`
	EventTime       int64  `json:"E"`
	Symbol          string `json:"s"`
	MarkPrice       string `json:"p"`
	IndexPrice      string `json:"i"`
	EstimatedPrice  string `json:"P"`
	FundingRate     string `json:"r"`
	NextFundingTime int64  `json:"T"`
}

// KlineEvent is a single kline/candlestick update
type KlineEvent struct {
	EventType string    `json:"e"`
	EventTime int64     `json:"E"`
	Symbol    string    `json:"s"`
	Kline     KlineData `json:"k"`
}

type KlineData struct {
	StartTime           int64  `json:"t"`
	EndTime             int64  `json:"T"`
	Symbol              string `json:"s"`
	Interval            string `json:"i"`
	FirstTradeID        int64  `json:"f"`
	LastTradeID         int64  `json:"L"`
	Open                string `json:"o"`
	Close               string `json:"c"`
	High                string `json:"h"`
	Low                 string `json:"l"`
	Volume              string `json:"v"`
	NumberOfTrades      int    `json:"n"`
	IsKlineClosed       bool   `json:"x"`
	QuoteAssetVolume    string `json:"q"`
	TakerBuyBaseVolume  string `json:"V"`
	TakerBuyQuoteVolume string `json:"Q"`
}

// ExchangeInfoResponse from /fapi/v1/exchangeInfo
type ExchangeInfoResponse struct {
	Symbols []SymbolInfo `json:"symbols"`
}

type SymbolInfo struct {
	Symbol       string   `json:"symbol"`
	Status       string   `json:"status"`
	ContractType string   `json:"contractType"`
	QuoteAsset   string   `json:"quoteAsset"`
	Filters      []Filter `json:"filters"`
}

type Filter struct {
	FilterType string `json:"filterType"`
	StepSize   string `json:"stepSize"`
	TickSize   string `json:"tickSize"`
	MinQty     string `json:"minQty"`
	MaxQty     string `json:"maxQty"`
}

// NewOrderRequest for POST /fapi/v1/order
type NewOrderRequest struct {
	Symbol       string
	Side         string
	Type         string
	Quantity     string
	Price        string
	StopPrice    string
	ReduceOnly   bool
	CallbackRate string // for TRAILING_STOP_MARKET orders (Phase 5)
}

// NewOrderResponse from Binance after placing order
type NewOrderResponse struct {
	OrderID     int64   `json:"orderId"`
	Symbol      string  `json:"symbol"`
	Status      string  `json:"status"`
	Side        string  `json:"side"`
	Type        string  `json:"type"`
	AvgPrice    float64 `json:"avgPrice,string"`
	ExecutedQty float64 `json:"executedQty,string"`
	UpdateTime  int64   `json:"updateTime"`
}

// OpenInterestResponse from /fapi/v1/openInterest
type OpenInterestResponse struct {
	Symbol       string  `json:"symbol"`
	OpenInterest float64 `json:"openInterest,string"`
	Time         int64   `json:"time"`
}

// AccountResponse from /fapi/v2/account
type AccountResponse struct {
	Assets []AssetBalance `json:"assets"`
}

type AssetBalance struct {
	Asset            string  `json:"asset"`
	WalletBalance    float64 `json:"walletBalance,string"`
	AvailableBalance float64 `json:"availableBalance,string"`
}

// PositionRiskResponse from GET /fapi/v2/positionRisk
type PositionRiskResponse struct {
	Symbol           string  `json:"symbol"`
	PositionAmt      float64 `json:"positionAmt,string"`
	EntryPrice       float64 `json:"entryPrice,string"`
	UnRealizedProfit float64 `json:"unRealizedProfit,string"`
	MarkPrice        float64 `json:"markPrice,string"`
}

// WsSubscribeRequest for Binance combined stream subscribe/unsubscribe
type WsSubscribeRequest struct {
	Method string   `json:"method"`
	Params []string `json:"params"`
	ID     int      `json:"id"`
}

// FundingInfoItem is one entry from GET /fapi/v1/fundingInfo
type FundingInfoItem struct {
	Symbol               string `json:"symbol"`
	FundingIntervalHours int    `json:"fundingIntervalHours"`
}

// HistoricalFundingRate is one entry from GET /fapi/v1/fundingRate
type HistoricalFundingRate struct {
	Symbol      string    `json:"-"`
	FundingTime time.Time `json:"-"`
	FundingRate float64   `json:"-"`
}

type fundingRateRaw struct {
	Symbol      string `json:"symbol"`
	FundingRate string `json:"fundingRate"`
	FundingTime int64  `json:"fundingTime"`
}

// UserDataEvent wraps ORDER_TRADE_UPDATE from the user data stream.
type UserDataEvent struct {
	EventType string           `json:"e"`
	EventTime int64            `json:"E"`
	Order     OrderTradeUpdate `json:"o"`
}

// OrderTradeUpdate is the "o" payload inside a USER_DATA_STREAM ORDER_TRADE_UPDATE event.
type OrderTradeUpdate struct {
	Symbol        string  `json:"s"`
	OrderID       int64   `json:"i"`
	ClientOrderID string  `json:"c"`
	Side          string  `json:"S"`
	OrderType     string  `json:"o"`
	OrderStatus   string  `json:"X"` // NEW, PARTIALLY_FILLED, FILLED, CANCELED, …
	AvgPrice      float64 `json:"ap,string"`
	Quantity      float64 `json:"q,string"`
	FilledQty     float64 `json:"z,string"`
	ReduceOnly    bool    `json:"R"`
}
