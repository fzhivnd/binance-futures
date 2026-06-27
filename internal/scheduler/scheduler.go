package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"futures/internal/config"
	"futures/internal/storage"
)

type ScanFunc func(ctx context.Context, window WindowType) error

// IntentQueue is satisfied by intent.Queue, avoiding a circular import.
type IntentQueue interface {
	Tick(ctx context.Context, currentWindow WindowType, timeUntilFunding time.Duration)
	ClearAfterSettlement()
}

type Scheduler struct {
	cfg         *config.SchedulerConfig
	cache       storage.StateCache
	scanFn      ScanFunc
	intentQueue IntentQueue
	lastWindow  WindowType
}

func NewScheduler(cfg *config.SchedulerConfig, cache storage.StateCache, scanFn ScanFunc) *Scheduler {
	return &Scheduler{cfg: cfg, cache: cache, scanFn: scanFn}
}

// SetIntentQueue wires in the Phase 3 intent queue so it is ticked every second.
func (s *Scheduler) SetIntentQueue(q IntentQueue) {
	s.intentQueue = q
}

func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	window := CurrentWindow(time.Now(), s.cfg.WindowStartMinutes)

	// Always tick the intent queue, even outside scan intervals.
	if s.intentQueue != nil {
		if window == WindowNone {
			s.intentQueue.ClearAfterSettlement()
		} else {
			now := time.Now()
			timeUntilFunding := NextFundingTime(now).Sub(now)
			s.intentQueue.Tick(ctx, window, timeUntilFunding)
		}
	}

	if window == WindowNone {
		return
	}

	// The AFTER window belongs to AfterTrigger — no new candidates can be usefully
	// evaluated after settlement has already fired.
	if window == WindowAfter {
		return
	}

	// Stop scanning for new candidates from T-3m to settlement.
	// Intent firing above continues uninterrupted during this window.
	next := NextFundingTime(time.Now().UTC())
	if until := next.Sub(time.Now().UTC()); until > 0 && until <= 30*time.Second {
		return
	}

	interval := s.cfg.GetScanIntervalEarly()
	if window == WindowLastMinute {
		interval = s.cfg.GetScanIntervalLate()
	}

	lockKey := fmt.Sprintf("lock:scan:%d", time.Now().Truncate(interval).UnixMilli())
	acquired, err := s.cache.AcquireSchedulerLock(ctx, lockKey, interval)
	if err != nil || !acquired {
		return
	}

	slog.Info("scheduler triggered scan", "window", window)
	if err := s.scanFn(ctx, window); err != nil {
		slog.Error("scan cycle error", "window", window, "error", err)
	}
}
