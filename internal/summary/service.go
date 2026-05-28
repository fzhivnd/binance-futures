package summary

import (
	"context"
	"log/slog"
	"time"

	"futures/internal/domain"
	"futures/internal/storage"
)

type Service struct {
	tradeRepo   storage.TradeRepository
	summaryRepo *storage.PGSummaryRepository
	isPaper     bool
}

func NewService(tradeRepo storage.TradeRepository, summaryRepo *storage.PGSummaryRepository, isPaper bool) *Service {
	return &Service{
		tradeRepo:   tradeRepo,
		summaryRepo: summaryRepo,
		isPaper:     isPaper,
	}
}

func (s *Service) GenerateAndStore(ctx context.Context, date time.Time) (*domain.DailySummary, error) {
	dayStart := date.Truncate(24 * time.Hour)
	dayEnd := dayStart.Add(24 * time.Hour)

	trades, err := s.tradeRepo.FindByOpenTimeRange(ctx, dayStart, dayEnd, s.isPaper)
	if err != nil {
		return nil, err
	}

	sum := s.aggregate(trades, dayStart)

	if err := s.summaryRepo.Upsert(ctx, sum); err != nil {
		return nil, err
	}

	slog.Info("daily_summary_generated",
		"date", dayStart.Format("2006-01-02"),
		"trades", sum.TradeCount,
		"wins", sum.WinCount,
		"losses", sum.LossCount,
		"win_rate", sum.WinRate,
		"pnl", sum.TotalPnL,
	)

	return sum, nil
}

func (s *Service) aggregate(trades []domain.Trade, date time.Time) *domain.DailySummary {
	sum := &domain.DailySummary{
		TradeDate:  date,
		TradeCount: len(trades),
		IsPaper:    s.isPaper,
	}

	for _, t := range trades {
		if t.ClosedAt == nil {
			continue
		}

		switch t.Result {
		case "WIN", "PARTIAL_WIN":
			sum.WinCount++
		case "LOSS", "FORCE_SL":
			sum.LossCount++
		}

		if t.PnL != 0 {
			sum.TotalPnL += t.PnL
		}
	}

	decided := sum.WinCount + sum.LossCount
	if decided > 0 {
		sum.WinRate = float64(sum.WinCount) / float64(decided) * 100
	}

	return sum
}

func (s *Service) BackfillIfNeeded(ctx context.Context) error {
	yesterday := time.Now().UTC().Add(-24 * time.Hour).Truncate(24 * time.Hour)
	exists, err := s.summaryRepo.Exists(ctx, yesterday, s.isPaper)
	if err != nil {
		return err
	}
	if !exists {
		_, err = s.GenerateAndStore(ctx, yesterday)
		return err
	}
	return nil
}
