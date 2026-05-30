package intent

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"futures/internal/domain"
	"futures/internal/scheduler"
)

type ExecuteFn func(ctx context.Context, sc *domain.ScoredCandidate, decision *domain.LLMDecision) error

// Queue holds multiple pending trade intents ranked by LLM confidence.
//
// Execution rules:
//   - FRONTRUN window: fire the highest-confidence eligible intent every frontrunInterval (default 5m).
//     The interval prevents executing on every 1m scan tick; later scans may surface better candidates.
//   - LAST_MINUTE window: fire the highest-confidence eligible intent exactly once on window entry.
//   - AFTER window: fire the highest-confidence eligible intent exactly once on window entry.
//
// Enqueue always replaces an existing pending intent for the same symbol with the latest
// LLM recommendation, regardless of confidence or entry mode.
type Queue struct {
	mu               sync.Mutex
	intents          []*TradeIntent // sorted by confidence descending
	execFn           ExecuteFn
	frontrunInterval time.Duration
	lastFrontrunExec time.Time
	firedInWindow    map[scheduler.WindowType]bool // one fire allowed per window type per cycle
}

func NewQueue(execFn ExecuteFn, frontrunInterval time.Duration) *Queue {
	return &Queue{
		execFn:           execFn,
		frontrunInterval: frontrunInterval,
		firedInWindow:    make(map[scheduler.WindowType]bool),
	}
}

// Enqueue adds the intent to the ranked pool.
// If a pending intent for the same symbol already exists it is always replaced —
// the latest LLM recommendation for a symbol is always the most current one.
func (q *Queue) Enqueue(intent *TradeIntent) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i, existing := range q.intents {
		if existing.Status == IntentPending && existing.Symbol == intent.Symbol {
			slog.Info("intent replaced",
				"symbol", intent.Symbol,
				"old_mode", existing.TargetEntryMode,
				"new_mode", intent.TargetEntryMode,
				"old_confidence", existing.Decision.Confidence,
				"new_confidence", intent.Decision.Confidence,
			)
			q.intents[i] = intent
			q.sortLocked()
			return
		}
	}

	q.intents = append(q.intents, intent)
	q.sortLocked()

	slog.Info("intent queued",
		"symbol", intent.Symbol,
		"target_mode", intent.TargetEntryMode,
		"confidence", intent.Decision.Confidence,
		"queue_depth", len(q.intents),
		"expires_at", intent.ExpiresAt.Format(time.RFC3339),
	)
}

// Tick is called every 1s by the scheduler.
func (q *Queue) Tick(ctx context.Context, currentWindow scheduler.WindowType) {
	q.mu.Lock()
	q.expireLocked()

	switch currentWindow {
	case scheduler.WindowFrontrun:
		q.tickFrontrunLocked(ctx)
	case scheduler.WindowLastMinute, scheduler.WindowAfter:
		q.tickTransitionLocked(ctx, currentWindow)
	}
	q.mu.Unlock()
}

// tickFrontrunLocked executes the best eligible intent on the FRONTRUN interval.
// Must be called with q.mu held.
func (q *Queue) tickFrontrunLocked(ctx context.Context) {
	if q.firedInWindow[scheduler.WindowFrontrun] {
		return
	}
	if time.Since(q.lastFrontrunExec) < q.frontrunInterval {
		return
	}

	intent := q.bestEligibleLocked(scheduler.WindowFrontrun)
	if intent == nil {
		return
	}

	intent.Status = IntentFired
	q.removeByIndexLocked(intent)
	q.lastFrontrunExec = time.Now()

	slog.Info("intent firing (FRONTRUN interval)",
		"symbol", intent.Symbol,
		"confidence", intent.Decision.Confidence,
		"queue_remaining", len(q.intents),
	)

	// Release lock before calling execFn (may block).
	q.mu.Unlock()
	execStart := time.Now()
	err := q.execFn(ctx, intent.Candidate, intent.Decision)
	latencyMs := float64(time.Since(execStart).Microseconds()) / 1000.0
	q.mu.Lock()
	if errors.Is(err, domain.ErrSkipped) {
		slog.Info("intent skipped (FRONTRUN), window not marked fired", "symbol", intent.Symbol)
	} else {
		q.firedInWindow[scheduler.WindowFrontrun] = true
		if err != nil {
			slog.Error("intent execution failed", "symbol", intent.Symbol, "window", scheduler.WindowFrontrun, "latency_ms", latencyMs, "error", err)
		} else {
			slog.Info("intent executed", "symbol", intent.Symbol, "window", scheduler.WindowFrontrun, "confidence", intent.Decision.Confidence, "latency_ms", latencyMs)
		}
	}
}

// ClaimAfterIntent atomically claims the best eligible AFTER intent and marks it fired.
// Returns nil if none is available or one was already fired. Safe for use by AfterTrigger
// to prevent double-fire with the scheduler's tickTransitionLocked path.
func (q *Queue) ClaimAfterIntent() *TradeIntent {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.firedInWindow[scheduler.WindowAfter] {
		return nil
	}
	intent := q.bestEligibleLocked(scheduler.WindowAfter)
	if intent == nil {
		return nil
	}
	intent.Status = IntentFired
	q.removeByIndexLocked(intent)
	q.firedInWindow[scheduler.WindowAfter] = true
	return intent
}

// PeekAfterSymbol returns the symbol of the best pending AFTER intent without claiming it.
func (q *Queue) PeekAfterSymbol() (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.firedInWindow[scheduler.WindowAfter] {
		return "", false
	}
	intent := q.bestEligibleLocked(scheduler.WindowAfter)
	if intent == nil {
		return "", false
	}
	return intent.Symbol, true
}

// HasAfterIntent returns true if there is at least one pending AFTER-eligible intent.
func (q *Queue) HasAfterIntent() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.firedInWindow[scheduler.WindowAfter] {
		return false
	}
	return q.bestEligibleLocked(scheduler.WindowAfter) != nil
}

// tickTransitionLocked fires the best eligible intent exactly once per window type per funding cycle.
// Must be called with q.mu held.
func (q *Queue) tickTransitionLocked(ctx context.Context, currentWindow scheduler.WindowType) {
	if q.firedInWindow[currentWindow] {
		return
	}

	intent := q.bestEligibleLocked(currentWindow)
	if intent == nil {
		return
	}

	intent.Status = IntentFired
	q.removeByIndexLocked(intent)

	slog.Info("intent firing (window transition)",
		"symbol", intent.Symbol,
		"target_mode", intent.TargetEntryMode,
		"current_window", currentWindow,
		"confidence", intent.Decision.Confidence,
		"queue_remaining", len(q.intents),
	)

	q.mu.Unlock()
	execStart := time.Now()
	err := q.execFn(ctx, intent.Candidate, intent.Decision)
	latencyMs := float64(time.Since(execStart).Microseconds()) / 1000.0
	q.mu.Lock()
	if errors.Is(err, domain.ErrSkipped) {
		slog.Info("intent skipped, window not marked fired", "symbol", intent.Symbol, "window", currentWindow)
	} else {
		q.firedInWindow[currentWindow] = true
		if err != nil {
			slog.Error("intent execution failed", "symbol", intent.Symbol, "window", currentWindow, "latency_ms", latencyMs, "error", err)
		} else {
			slog.Info("intent executed", "symbol", intent.Symbol, "window", currentWindow, "confidence", intent.Decision.Confidence, "latency_ms", latencyMs)
		}
	}
}

// bestEligibleLocked returns the highest-confidence pending intent that can fire
// in the given window, or nil if none exists. Must be called with q.mu held.
// intents is already sorted by confidence desc, so the first match is the best.
func (q *Queue) bestEligibleLocked(currentWindow scheduler.WindowType) *TradeIntent {
	for _, intent := range q.intents {
		if intent.Status == IntentPending && windowReady(currentWindow, intent.TargetEntryMode) {
			return intent
		}
	}
	return nil
}

// expireLocked removes intents that have passed their expiry. Must be called with q.mu held.
func (q *Queue) expireLocked() {
	now := time.Now()
	kept := q.intents[:0]
	for _, intent := range q.intents {
		if intent.IsExpired(now) {
			slog.Info("intent expired", "symbol", intent.Symbol, "mode", intent.TargetEntryMode)
		} else {
			kept = append(kept, intent)
		}
	}
	q.intents = kept
}

// removeByIndexLocked removes a specific intent pointer from the slice.
// Must be called with q.mu held.
func (q *Queue) removeByIndexLocked(target *TradeIntent) {
	filtered := q.intents[:0]
	for _, intent := range q.intents {
		if intent != target {
			filtered = append(filtered, intent)
		}
	}
	q.intents = filtered
}

// sortLocked re-sorts intents by confidence descending. Must be called with q.mu held.
func (q *Queue) sortLocked() {
	sort.Slice(q.intents, func(i, j int) bool {
		return q.intents[i].Decision.Confidence > q.intents[j].Decision.Confidence
	})
}

// HasPendingIntent returns true if at least one intent is still pending.
func (q *Queue) HasPendingIntent() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, intent := range q.intents {
		if intent.Status == IntentPending {
			return true
		}
	}
	return false
}

// PendingCount returns the number of pending intents.
func (q *Queue) PendingCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, intent := range q.intents {
		if intent.Status == IntentPending {
			n++
		}
	}
	return n
}

// CancelBySymbol removes all pending intents for the given symbol.
// Called when the LLM issues a late-cycle SKIP so the stale queued intent
// cannot survive to fire in a later window.
func (q *Queue) CancelBySymbol(symbol string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	kept := q.intents[:0]
	for _, intent := range q.intents {
		if intent.Symbol == symbol && intent.Status == IntentPending {
			slog.Info("intent cancelled (late LLM skip)", "symbol", symbol, "mode", intent.TargetEntryMode)
		} else {
			kept = append(kept, intent)
		}
	}
	q.intents = kept
}

// ClearAfterSettlement resets the queue at the end of a funding cycle.
func (q *Queue) ClearAfterSettlement() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.intents) > 0 {
		slog.Info("clearing queue after settlement", "pending", len(q.intents))
	}
	q.intents = q.intents[:0]
	q.lastFrontrunExec = time.Time{}
	q.firedInWindow = make(map[scheduler.WindowType]bool)
}

// windowReady returns true when the current window is at or past the target entry mode.
// Order: FRONTRUN(0) < LAST_MINUTE(1) < AFTER(2)
func windowReady(current scheduler.WindowType, target domain.EntryMode) bool {
	windowRank := map[scheduler.WindowType]int{
		scheduler.WindowFrontrun:   0,
		scheduler.WindowLastMinute: 1,
		scheduler.WindowAfter:      2,
	}
	modeRank := map[domain.EntryMode]int{
		domain.EntryModeFrontrun:   0,
		domain.EntryModeLastMinute: 1,
		domain.EntryModeAfter:      2,
	}

	cr, ok1 := windowRank[current]
	tr, ok2 := modeRank[target]
	if !ok1 || !ok2 {
		return false
	}
	return cr >= tr
}
