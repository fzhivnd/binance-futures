package summary

import (
	"context"
	"log/slog"
	"time"

	"futures/internal/domain"
)

type DailyScheduler struct {
	service    *Service
	onComplete func(context.Context, *domain.DailySummary)
}

func NewDailyScheduler(service *Service, onComplete func(context.Context, *domain.DailySummary)) *DailyScheduler {
	return &DailyScheduler{
		service:    service,
		onComplete: onComplete,
	}
}

// Run blocks until context is cancelled. Fires at 00:00 UTC daily.
func (d *DailyScheduler) Run(ctx context.Context) {
	for {
		now := time.Now().UTC()
		nextMidnight := now.Truncate(24 * time.Hour).Add(24 * time.Hour)
		delay := nextMidnight.Sub(now)

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		// Generate summary for the day that just ended.
		yesterday := time.Now().UTC().Add(-1 * time.Second).Truncate(24 * time.Hour)
		sum, err := d.service.GenerateAndStore(ctx, yesterday)
		if err != nil {
			slog.Error("daily summary generation failed", "date", yesterday, "error", err)
			continue
		}

		if d.onComplete != nil {
			d.onComplete(ctx, sum)
		}
	}
}
