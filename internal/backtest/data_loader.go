package backtest

import (
	"context"
	"time"

	"futures/internal/domain"
	"futures/internal/exchange"
)

// HistoricalData holds pre-fetched candles and funding rates for all symbols.
type HistoricalData struct {
	// symbol → timeframe → ordered candle slice
	Candles map[string]map[domain.Timeframe][]domain.Candle
	// symbol → ordered funding rate snapshots
	FundingRates map[string][]FundingSnapshot
	// time-ordered list of funding settlement windows
	FundingWindows []FundingWindow
}

type FundingSnapshot struct {
	Time time.Time
	Rate float64
}

type FundingWindow struct {
	Time    time.Time
	Symbol  string
	Rate    float64
}

// DataLoader fetches historical market data from the Binance REST API.
type DataLoader struct {
	client *exchange.BinanceClient
}

func NewDataLoader(client *exchange.BinanceClient) *DataLoader {
	return &DataLoader{client: client}
}

// Load fetches candles and funding rates for all symbols over the configured date range.
func (d *DataLoader) Load(ctx context.Context, cfg Config) (*HistoricalData, error) {
	data := &HistoricalData{
		Candles:      make(map[string]map[domain.Timeframe][]domain.Candle),
		FundingRates: make(map[string][]FundingSnapshot),
	}

	timeframes := []domain.Timeframe{
		domain.Timeframe1h,
		domain.Timeframe30m,
		domain.Timeframe15m,
		domain.Timeframe5m,
	}

	for _, sym := range cfg.Symbols {
		data.Candles[sym] = make(map[domain.Timeframe][]domain.Candle)

		for _, tf := range timeframes {
			candles, err := d.client.GetHistoricalKlines(ctx, sym, string(tf), cfg.StartDate, cfg.EndDate)
			if err != nil {
				return nil, err
			}
			data.Candles[sym][tf] = candles
		}

		rates, err := d.client.GetHistoricalFundingRates(ctx, sym, cfg.StartDate, cfg.EndDate)
		if err != nil {
			return nil, err
		}

		for _, r := range rates {
			data.FundingRates[sym] = append(data.FundingRates[sym], FundingSnapshot{
				Time: r.FundingTime,
				Rate: r.FundingRate,
			})
			data.FundingWindows = append(data.FundingWindows, FundingWindow{
				Time:   r.FundingTime,
				Symbol: sym,
				Rate:   r.FundingRate,
			})
		}
	}

	// Sort funding windows by time
	sortFundingWindows(data.FundingWindows)

	return data, nil
}

func sortFundingWindows(windows []FundingWindow) {
	for i := 1; i < len(windows); i++ {
		for j := i; j > 0 && windows[j].Time.Before(windows[j-1].Time); j-- {
			windows[j], windows[j-1] = windows[j-1], windows[j]
		}
	}
}
