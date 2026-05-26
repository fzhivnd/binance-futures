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
	Tick(ctx context.Context, currentWindow WindowType)
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
			s.intentQueue.Tick(ctx, window)
		}
	}

	if window == WindowNone {
		return
	}

	interval := s.cfg.GetScanIntervalEarly()
	if window == WindowLastMinute || window == WindowAfter {
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
