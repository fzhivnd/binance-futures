package scanner

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"futures/internal/config"
	"futures/internal/domain"
	"futures/internal/scheduler"
)

type MarketProvider interface {
	GetAllFundingRates() map[string]float64
	GetPrice(symbol string) (float64, bool)
	GetFundingIntervalHours(symbol string) int
	GetFundingInfo(symbol string) (*domain.FundingRate, bool)
	GetCandles(symbol string, tf domain.Timeframe, limit int) []domain.Candle
}

type FundingScanner struct {
	market MarketProvider
	cfg    *config.FundingConfig
}

func NewFundingScanner(m MarketProvider, cfg *config.FundingConfig) *FundingScanner {
	return &FundingScanner{market: m, cfg: cfg}
}

func (s *FundingScanner) Scan(ctx context.Context) ([]domain.Candidate, error) {
	rates := s.market.GetAllFundingRates()
	if len(rates) == 0 {
		return nil, nil
	}

	var candidates []domain.Candidate
	for symbol, rate := range rates {
		if rate > s.cfg.MaxRate || rate < s.cfg.MinRate {
			continue
		}
		price, ok := s.market.GetPrice(symbol)
		if !ok || price <= 0 {
			continue
		}
		intervalHours := s.market.GetFundingIntervalHours(symbol)
		if intervalHours == 1 {
			slog.Debug("skipping 1h funding interval", "symbol", symbol)
			continue
		}
		if !settlementAlignedWithWindow(s.market, symbol) {
			slog.Debug("skipping: funding not settling in this window", "symbol", symbol, "interval_hours", intervalHours)
			continue
		}
		c := buildCandidate(symbol, rate, price, s.market)
		if c.Score > 0 {
			candidates = append(candidates, c)
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	limit := s.cfg.TopCandidates
	if limit > len(candidates) {
		limit = len(candidates)
	}
	candidates = candidates[:limit]

	slog.Info("scan completed", "total_filtered", len(rates), "candidates", len(candidates))
	return candidates, nil
}

// settlementAlignedWithWindow returns true if the symbol's next funding time
// matches the upcoming settlement window. Filters out 8h-interval symbols that
// are not settling in the current 4h window — e.g. at UTC 04:00 an 8h symbol
// settling at 08:00 is 4h away and should not be traded.
// Tolerance is 65 minutes to cover the full T-30m → T+5m trading window.
func settlementAlignedWithWindow(m MarketProvider, symbol string) bool {
	info, ok := m.GetFundingInfo(symbol)
	if !ok || info.NextFunding.IsZero() {
		return true // no data — allow through, don't over-filter
	}
	nextWindow := scheduler.NextFundingTime(time.Now().UTC())
	diff := info.NextFunding.UTC().Sub(nextWindow)
	if diff < 0 {
		diff = -diff
	}
	return diff <= 65*time.Minute
}
