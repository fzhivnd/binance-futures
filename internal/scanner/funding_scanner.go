package scanner

import (
	"context"
	"log/slog"
	"sort"

	"futures/internal/config"
	"futures/internal/domain"
)

type MarketProvider interface {
	GetAllFundingRates() map[string]float64
	GetPrice(symbol string) (float64, bool)
	GetFundingIntervalHours(symbol string) int
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
