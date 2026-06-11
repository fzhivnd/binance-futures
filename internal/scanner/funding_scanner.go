package scanner

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"futures/internal/config"
	"futures/internal/domain"
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
			//slog.Info("funding filter skip", "symbol", symbol, "rate", rate, "min", s.cfg.MinRate, "max", s.cfg.MaxRate)
			continue
		}
		price, ok := s.market.GetPrice(symbol)
		if !ok || price <= 0 {
			//slog.Info("price filter skip", "symbol", symbol, "rate", rate, "min", s.cfg.MinRate, "max", s.cfg.MaxRate)
			continue
		}
		intervalHours := s.market.GetFundingIntervalHours(symbol)
		if intervalHours == 1 {
			//slog.Info("skipping 1h funding interval", "symbol", symbol)
			continue
		}
		if !settlementAlignedWithWindow(s.market, symbol) {
			//slog.Info("skipping: funding not settling in this window", "symbol", symbol, "interval_hours", intervalHours)
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

// settlementAlignedWithWindow returns true if the symbol is settling within the
// active trading window (T-30m to T+1m). This correctly handles 8h-interval
// symbols by checking the symbol's actual NextFunding time directly against now,
// rather than comparing against the scheduler's generic 4h window boundary.
func settlementAlignedWithWindow(m MarketProvider, symbol string) bool {
	info, ok := m.GetFundingInfo(symbol)
	if !ok || info.NextFunding.IsZero() {
		return true // no data — allow through, don't over-filter
	}
	until := time.Until(info.NextFunding.UTC())
	// Allow if settlement is within 30m in the future or up to 1m in the past.
	return until <= 30*time.Minute && until >= -1*time.Minute
}
