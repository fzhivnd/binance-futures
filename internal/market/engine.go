package market

import (
	"log/slog"
	"time"

	"futures/internal/domain"
	"futures/internal/indicator"
)

// MarketEngine holds all in-memory market data caches.
// All methods are goroutine-safe.
type MarketEngine struct {
	funding *FundingCache
	ticker  *TickerCache
	candles *CandleStore
	oi      *OICache
}

func NewMarketEngine() *MarketEngine {
	return &MarketEngine{
		funding: NewFundingCache(),
		ticker:  NewTickerCache(),
		candles: NewCandleStore(),
		oi:      NewOICache(),
	}
}

func (e *MarketEngine) UpdateFunding(symbol string, rate float64, nextFunding time.Time, markPrice float64) {
	e.funding.Update(symbol, rate, nextFunding, markPrice)
	e.ticker.Update(symbol, markPrice)
	slog.Debug("funding updated", "symbol", symbol, "rate", rate)
}

func (e *MarketEngine) SetFundingInterval(symbol string, intervalHours int) {
	e.funding.SetFundingInterval(symbol, intervalHours)
}

func (e *MarketEngine) GetFundingIntervalHours(symbol string) int {
	return e.funding.GetIntervalHours(symbol)
}

func (e *MarketEngine) UpdateCandle(c domain.Candle) {
	e.candles.Update(c)
}

func (e *MarketEngine) UpdateOI(symbol string, oi float64) {
	e.oi.Update(symbol, oi)
}

func (e *MarketEngine) GetFundingRate(symbol string) (float64, bool) {
	return e.funding.GetRate(symbol)
}

func (e *MarketEngine) GetPrice(symbol string) (float64, bool) {
	return e.ticker.GetPrice(symbol)
}

func (e *MarketEngine) GetCandles(symbol string, tf domain.Timeframe, limit int) []domain.Candle {
	return e.candles.Get(symbol, tf, limit)
}

func (e *MarketEngine) GetOpenInterest(symbol string) (float64, bool) {
	return e.oi.Get(symbol)
}

func (e *MarketEngine) GetAllFundingRates() map[string]float64 {
	return e.funding.GetAll()
}

func (e *MarketEngine) GetTopNegativeFundingSymbols(n int) []string {
	return e.funding.TopNegative(n)
}

func (e *MarketEngine) GetFundingInfo(symbol string) (*domain.FundingRate, bool) {
	return e.funding.GetInfo(symbol)
}

func (e *MarketEngine) TickerCache() *TickerCache {
	return e.ticker
}

// OIHistory returns the OI ring-buffer history for use by the indicator engine.
func (e *MarketEngine) OIHistory() *indicator.OIHistory {
	return e.oi.History()
}
