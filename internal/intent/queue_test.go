package intent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"futures/internal/domain"
	"futures/internal/scheduler"
)

// helpers

func newIntent(symbol string, mode domain.EntryMode, confidence int) *TradeIntent {
	return &TradeIntent{
		Symbol: symbol,
		Candidate: &domain.ScoredCandidate{
			Candidate: domain.Candidate{Symbol: symbol},
		},
		Decision: &domain.LLMDecision{
			Action:     "OPEN_SHORT",
			Symbol:     symbol,
			Confidence: confidence,
			EntryMode:  mode,
		},
		TargetEntryMode: mode,
		CreatedAt:       time.Now(),
		ExpiresAt:       time.Now().Add(10 * time.Minute),
		Status:          IntentPending,
	}
}

func noopExec(_ context.Context, _ *domain.ScoredCandidate, _ *domain.LLMDecision) error {
	return nil
}

// recordingExec appends fired symbol names to a slice via atomic counter
type recorder struct {
	symbols []string
}

func (r *recorder) exec(_ context.Context, sc *domain.ScoredCandidate, _ *domain.LLMDecision) error {
	r.symbols = append(r.symbols, sc.Candidate.Symbol)
	return nil
}

// --- Enqueue ---

func TestEnqueue_AddsNewIntent(t *testing.T) {
	q := NewQueue(noopExec, 5*time.Minute, 20)
	q.Enqueue(newIntent("PEPE", domain.EntryModeFrontrun, 80))

	if q.PendingCount() != 1 {
		t.Fatalf("expected 1 pending, got %d", q.PendingCount())
	}
}

func TestEnqueue_SortsByConfidenceDesc(t *testing.T) {
	q := NewQueue(noopExec, 5*time.Minute, 20)
	q.Enqueue(newIntent("A", domain.EntryModeFrontrun, 70))
	q.Enqueue(newIntent("B", domain.EntryModeFrontrun, 90))
	q.Enqueue(newIntent("C", domain.EntryModeFrontrun, 80))

	q.mu.Lock()
	order := make([]int, len(q.intents))
	for i, ti := range q.intents {
		order[i] = ti.Decision.Confidence
	}
	q.mu.Unlock()

	for i := 1; i < len(order); i++ {
		if order[i] > order[i-1] {
			t.Fatalf("intents not sorted desc at position %d: %v", i, order)
		}
	}
}

func TestEnqueue_DifferentSymbolSameMode_BothKept(t *testing.T) {
	q := NewQueue(noopExec, 5*time.Minute, 20)
	q.Enqueue(newIntent("PEPE", domain.EntryModeFrontrun, 75))
	q.Enqueue(newIntent("WIF", domain.EntryModeFrontrun, 80))

	if q.PendingCount() != 2 {
		t.Fatalf("expected 2 pending, got %d", q.PendingCount())
	}
}

func TestEnqueue_SameSymbol_AlwaysReplaces(t *testing.T) {
	q := NewQueue(noopExec, 5*time.Minute, 20)
	q.Enqueue(newIntent("PEPE", domain.EntryModeFrontrun, 85))
	q.Enqueue(newIntent("PEPE", domain.EntryModeFrontrun, 70)) // lower confidence — still replaces

	if q.PendingCount() != 1 {
		t.Fatalf("expected 1 pending after replace, got %d", q.PendingCount())
	}

	q.mu.Lock()
	conf := q.intents[0].Decision.Confidence
	q.mu.Unlock()

	if conf != 70 {
		t.Fatalf("expected confidence 70 (latest), got %d", conf)
	}
}

func TestEnqueue_SameSymbolDifferentMode_Replaces(t *testing.T) {
	q := NewQueue(noopExec, 5*time.Minute, 20)
	q.Enqueue(newIntent("PEPE", domain.EntryModeFrontrun, 80))
	q.Enqueue(newIntent("PEPE", domain.EntryModeLastMinute, 75)) // different mode — still replaces

	if q.PendingCount() != 1 {
		t.Fatalf("expected 1 pending (same symbol always replaces), got %d", q.PendingCount())
	}

	q.mu.Lock()
	mode := q.intents[0].TargetEntryMode
	q.mu.Unlock()

	if mode != domain.EntryModeLastMinute {
		t.Fatalf("expected LAST_MINUTE (latest), got %v", mode)
	}
}

// --- windowReady ---

func TestWindowReady_AllCombinations(t *testing.T) {
	cases := []struct {
		current scheduler.WindowType
		target  domain.EntryMode
		want    bool
	}{
		// FRONTRUN window
		{scheduler.WindowFrontrun, domain.EntryModeFrontrun, true},
		{scheduler.WindowFrontrun, domain.EntryModeLastMinute, false},
		{scheduler.WindowFrontrun, domain.EntryModeAfter, false},
		// LAST_MINUTE window
		{scheduler.WindowLastMinute, domain.EntryModeFrontrun, true},
		{scheduler.WindowLastMinute, domain.EntryModeLastMinute, true},
		{scheduler.WindowLastMinute, domain.EntryModeAfter, false},
		// AFTER window
		{scheduler.WindowAfter, domain.EntryModeFrontrun, true},
		{scheduler.WindowAfter, domain.EntryModeLastMinute, true},
		{scheduler.WindowAfter, domain.EntryModeAfter, true},
		// Unknown window / mode
		{scheduler.WindowNone, domain.EntryModeFrontrun, false},
	}

	for _, tc := range cases {
		got := windowReady(tc.current, tc.target)
		if got != tc.want {
			t.Errorf("windowReady(%s, %s) = %v, want %v", tc.current, tc.target, got, tc.want)
		}
	}
}

// --- Frontrun interval throttle ---

func TestTick_Frontrun_DoesNotFireBeforeInterval(t *testing.T) {
	var fired atomic.Int32
	exec := func(_ context.Context, _ *domain.ScoredCandidate, _ *domain.LLMDecision) error {
		fired.Add(1)
		return nil
	}

	q := NewQueue(exec, 5*time.Minute, 20)
	// Set lastFrontrunExec to now — interval has not elapsed.
	q.lastFrontrunExec = time.Now()

	q.Enqueue(newIntent("PEPE", domain.EntryModeFrontrun, 82))
	q.Tick(context.Background(), scheduler.WindowFrontrun, 19*time.Minute)

	if fired.Load() != 0 {
		t.Fatal("should not have fired before interval elapsed")
	}
	if q.PendingCount() != 1 {
		t.Fatalf("intent should still be pending, got count=%d", q.PendingCount())
	}
}

func TestTick_Frontrun_FiresAfterInterval(t *testing.T) {
	var fired atomic.Int32
	exec := func(_ context.Context, _ *domain.ScoredCandidate, _ *domain.LLMDecision) error {
		fired.Add(1)
		return nil
	}

	q := NewQueue(exec, 1*time.Millisecond, 20) // tiny interval so it elapses immediately
	q.Enqueue(newIntent("PEPE", domain.EntryModeFrontrun, 82))

	time.Sleep(5 * time.Millisecond)
	q.Tick(context.Background(), scheduler.WindowFrontrun, 19*time.Minute)

	if fired.Load() != 1 {
		t.Fatalf("expected 1 fire, got %d", fired.Load())
	}
	if q.PendingCount() != 0 {
		t.Fatalf("fired intent should be removed, got count=%d", q.PendingCount())
	}
}

func TestTick_Frontrun_PicksHighestConfidence(t *testing.T) {
	rec := &recorder{}
	q := NewQueue(rec.exec, 1*time.Millisecond, 20)
	q.Enqueue(newIntent("LOW", domain.EntryModeFrontrun, 65))
	q.Enqueue(newIntent("HIGH", domain.EntryModeFrontrun, 88))
	q.Enqueue(newIntent("MID", domain.EntryModeFrontrun, 75))

	time.Sleep(5 * time.Millisecond)
	q.Tick(context.Background(), scheduler.WindowFrontrun, 19*time.Minute)

	if len(rec.symbols) != 1 || rec.symbols[0] != "HIGH" {
		t.Fatalf("expected HIGH to fire, got %v", rec.symbols)
	}
	// Other two should still be in queue
	if q.PendingCount() != 2 {
		t.Fatalf("expected 2 remaining, got %d", q.PendingCount())
	}
}

func TestTick_Frontrun_DoesNotFireTwiceWithinInterval(t *testing.T) {
	var fired atomic.Int32
	exec := func(_ context.Context, _ *domain.ScoredCandidate, _ *domain.LLMDecision) error {
		fired.Add(1)
		return nil
	}

	q := NewQueue(exec, 1*time.Millisecond, 20)
	q.Enqueue(newIntent("A", domain.EntryModeFrontrun, 80))
	q.Enqueue(newIntent("B", domain.EntryModeFrontrun, 75))

	time.Sleep(5 * time.Millisecond)
	q.Tick(context.Background(), scheduler.WindowFrontrun, 19*time.Minute) // fires A (highest)
	q.Tick(context.Background(), scheduler.WindowFrontrun, 19*time.Minute) // interval not yet elapsed again → no fire

	if fired.Load() != 1 {
		t.Fatalf("expected exactly 1 fire, got %d", fired.Load())
	}
}

func TestTick_Frontrun_SkipsNonFrontrunIntents(t *testing.T) {
	var fired atomic.Int32
	exec := func(_ context.Context, _ *domain.ScoredCandidate, _ *domain.LLMDecision) error {
		fired.Add(1)
		return nil
	}

	q := NewQueue(exec, 1*time.Millisecond, 20)
	// Only AFTER-mode intent — not eligible in FRONTRUN window.
	q.Enqueue(newIntent("PEPE", domain.EntryModeAfter, 90))

	time.Sleep(5 * time.Millisecond)
	q.Tick(context.Background(), scheduler.WindowFrontrun, 19*time.Minute)

	if fired.Load() != 0 {
		t.Fatal("AFTER-mode intent should not fire during FRONTRUN window")
	}
}

// --- LAST_MINUTE / AFTER window transitions ---

func TestTick_LastMinute_FiresOnWindowEntry(t *testing.T) {
	rec := &recorder{}
	q := NewQueue(rec.exec, 5*time.Minute, 20)
	q.Enqueue(newIntent("PEPE", domain.EntryModeLastMinute, 78))

	q.Tick(context.Background(), scheduler.WindowLastMinute, 0)

	if len(rec.symbols) != 1 || rec.symbols[0] != "PEPE" {
		t.Fatalf("expected PEPE to fire on LAST_MINUTE, got %v", rec.symbols)
	}
}

func TestTick_LastMinute_FiresOnlyOnce(t *testing.T) {
	var fired atomic.Int32
	exec := func(_ context.Context, _ *domain.ScoredCandidate, _ *domain.LLMDecision) error {
		fired.Add(1)
		return nil
	}

	q := NewQueue(exec, 5*time.Minute, 20)
	q.Enqueue(newIntent("PEPE", domain.EntryModeLastMinute, 78))

	q.Tick(context.Background(), scheduler.WindowLastMinute, 0)
	q.Tick(context.Background(), scheduler.WindowLastMinute, 0) // second tick same window
	q.Tick(context.Background(), scheduler.WindowLastMinute, 0) // third tick

	if fired.Load() != 1 {
		t.Fatalf("expected exactly 1 fire in LAST_MINUTE, got %d", fired.Load())
	}
}

func TestTick_LastMinute_PicksHighestConfidenceEligible(t *testing.T) {
	rec := &recorder{}
	q := NewQueue(rec.exec, 5*time.Minute, 20)
	// FRONTRUN and LAST_MINUTE intents are both eligible in LAST_MINUTE window.
	q.Enqueue(newIntent("FRONTRUN_COIN", domain.EntryModeFrontrun, 72))
	q.Enqueue(newIntent("LM_COIN", domain.EntryModeLastMinute, 88)) // highest
	q.Enqueue(newIntent("LM_LOW", domain.EntryModeLastMinute, 65))

	q.Tick(context.Background(), scheduler.WindowLastMinute, 0)

	if len(rec.symbols) != 1 || rec.symbols[0] != "LM_COIN" {
		t.Fatalf("expected LM_COIN (confidence 88) to fire, got %v", rec.symbols)
	}
}

func TestTick_After_FiresOnWindowEntry(t *testing.T) {
	rec := &recorder{}
	q := NewQueue(rec.exec, 5*time.Minute, 20)
	q.Enqueue(newIntent("WIF", domain.EntryModeAfter, 74))

	q.Tick(context.Background(), scheduler.WindowAfter, 0)

	if len(rec.symbols) != 1 || rec.symbols[0] != "WIF" {
		t.Fatalf("expected WIF to fire on AFTER, got %v", rec.symbols)
	}
}

func TestTick_After_FiresOnlyOnce(t *testing.T) {
	var fired atomic.Int32
	exec := func(_ context.Context, _ *domain.ScoredCandidate, _ *domain.LLMDecision) error {
		fired.Add(1)
		return nil
	}

	q := NewQueue(exec, 5*time.Minute, 20)
	q.Enqueue(newIntent("WIF", domain.EntryModeAfter, 74))

	q.Tick(context.Background(), scheduler.WindowAfter, 0)
	q.Tick(context.Background(), scheduler.WindowAfter, 0)

	if fired.Load() != 1 {
		t.Fatalf("expected exactly 1 fire in AFTER, got %d", fired.Load())
	}
}

func TestTick_LastMinuteFire_DoesNotBlockAfterWindow(t *testing.T) {
	// LAST_MINUTE and AFTER are independent window types — each may fire once per cycle.
	rec := &recorder{}
	q := NewQueue(rec.exec, 5*time.Minute, 20)
	q.Enqueue(newIntent("LM_COIN", domain.EntryModeLastMinute, 80))
	q.Enqueue(newIntent("AFTER_COIN", domain.EntryModeAfter, 70))

	q.Tick(context.Background(), scheduler.WindowLastMinute, 0) // fires LM_COIN
	q.Tick(context.Background(), scheduler.WindowAfter, 0)      // fires AFTER_COIN independently

	if len(rec.symbols) != 2 {
		t.Fatalf("expected both LM_COIN and AFTER_COIN to fire, got %v", rec.symbols)
	}
	if q.PendingCount() != 0 {
		t.Fatalf("both intents should be gone, got count=%d", q.PendingCount())
	}
}

func TestTick_FrontrunFire_DoesNotBlockLastMinuteOrAfter(t *testing.T) {
	// Each window type (FRONTRUN, LAST_MINUTE, AFTER) fires independently — one per cycle each.
	rec := &recorder{}
	q := NewQueue(rec.exec, 1*time.Millisecond, 20)
	q.Enqueue(newIntent("FR_COIN", domain.EntryModeFrontrun, 82))
	q.Enqueue(newIntent("LM_COIN", domain.EntryModeLastMinute, 78))
	q.Enqueue(newIntent("AFTER_COIN", domain.EntryModeAfter, 74))

	time.Sleep(5 * time.Millisecond)
	q.Tick(context.Background(), scheduler.WindowFrontrun, 19*time.Minute) // fires FR_COIN
	q.Tick(context.Background(), scheduler.WindowLastMinute, 0)            // fires LM_COIN independently
	q.Tick(context.Background(), scheduler.WindowAfter, 0)                 // fires AFTER_COIN independently

	if len(rec.symbols) != 3 {
		t.Fatalf("expected all 3 window types to fire independently, got %v", rec.symbols)
	}
	if rec.symbols[0] != "FR_COIN" {
		t.Fatalf("first fire should be FR_COIN, got %s", rec.symbols[0])
	}
}

func TestTick_OneFirePerWindowTypePerCycle(t *testing.T) {
	// Each window type fires at most once per funding cycle, independent of others.
	// FRONTRUN fires A (conf 90). LAST_MINUTE fires C (conf 88, highest eligible). AFTER fires B (conf 85, highest eligible remaining).
	var fired atomic.Int32
	exec := func(_ context.Context, _ *domain.ScoredCandidate, _ *domain.LLMDecision) error {
		fired.Add(1)
		return nil
	}

	q := NewQueue(exec, 1*time.Millisecond, 20)
	q.Enqueue(newIntent("A", domain.EntryModeFrontrun, 90))
	q.Enqueue(newIntent("B", domain.EntryModeFrontrun, 85))
	q.Enqueue(newIntent("C", domain.EntryModeLastMinute, 88))
	q.Enqueue(newIntent("D", domain.EntryModeAfter, 76))

	time.Sleep(5 * time.Millisecond)
	// FRONTRUN: fires once (A), subsequent ticks blocked.
	for i := 0; i < 5; i++ {
		q.Tick(context.Background(), scheduler.WindowFrontrun, 19*time.Minute)
	}
	if fired.Load() != 1 {
		t.Fatalf("expected 1 fire after FRONTRUN ticks, got %d", fired.Load())
	}

	// LAST_MINUTE: fires once (C, highest eligible), subsequent ticks blocked.
	for i := 0; i < 5; i++ {
		q.Tick(context.Background(), scheduler.WindowLastMinute, 0)
	}
	if fired.Load() != 2 {
		t.Fatalf("expected 2 fires after LAST_MINUTE ticks, got %d", fired.Load())
	}

	// AFTER: fires once (B, highest eligible remaining), subsequent ticks blocked.
	for i := 0; i < 5; i++ {
		q.Tick(context.Background(), scheduler.WindowAfter, 0)
	}
	if fired.Load() != 3 {
		t.Fatalf("expected 3 fires after AFTER ticks, got %d", fired.Load())
	}
}

func TestTick_HoldsAfterIntentDuringFrontrun(t *testing.T) {
	var fired atomic.Int32
	exec := func(_ context.Context, _ *domain.ScoredCandidate, _ *domain.LLMDecision) error {
		fired.Add(1)
		return nil
	}

	q := NewQueue(exec, 1*time.Millisecond, 20)
	// Only an AFTER-mode intent — should not fire in FRONTRUN or LAST_MINUTE windows.
	q.Enqueue(newIntent("WAIT_COIN", domain.EntryModeAfter, 80))

	time.Sleep(5 * time.Millisecond)
	q.Tick(context.Background(), scheduler.WindowFrontrun, 19*time.Minute)
	q.Tick(context.Background(), scheduler.WindowLastMinute, 0)

	if fired.Load() != 0 {
		t.Fatal("AFTER-mode intent should not fire before AFTER window")
	}
	if q.PendingCount() != 1 {
		t.Fatalf("intent should still be pending, got count=%d", q.PendingCount())
	}

	// Now fires in AFTER window.
	q.Tick(context.Background(), scheduler.WindowAfter, 0)
	if fired.Load() != 1 {
		t.Fatal("AFTER-mode intent should fire in AFTER window")
	}
}

// --- Expiry ---

func TestTick_ExpiredIntentNotFired(t *testing.T) {
	var fired atomic.Int32
	exec := func(_ context.Context, _ *domain.ScoredCandidate, _ *domain.LLMDecision) error {
		fired.Add(1)
		return nil
	}

	q := NewQueue(exec, 1*time.Millisecond, 20)
	expired := newIntent("OLD", domain.EntryModeFrontrun, 90)
	expired.ExpiresAt = time.Now().Add(-1 * time.Second) // already expired

	q.Enqueue(expired)
	time.Sleep(5 * time.Millisecond)
	q.Tick(context.Background(), scheduler.WindowFrontrun, 19*time.Minute)

	if fired.Load() != 0 {
		t.Fatal("expired intent should not fire")
	}
	if q.PendingCount() != 0 {
		t.Fatalf("expired intent should be removed, got count=%d", q.PendingCount())
	}
}

func TestTick_OnlyNonExpiredIntentFires(t *testing.T) {
	rec := &recorder{}
	q := NewQueue(rec.exec, 1*time.Millisecond, 20)

	expired := newIntent("OLD", domain.EntryModeFrontrun, 95) // highest confidence but expired
	expired.ExpiresAt = time.Now().Add(-1 * time.Second)
	q.Enqueue(expired)

	valid := newIntent("NEW", domain.EntryModeFrontrun, 80)
	q.Enqueue(valid)

	time.Sleep(5 * time.Millisecond)
	q.Tick(context.Background(), scheduler.WindowFrontrun, 19*time.Minute)

	if len(rec.symbols) != 1 || rec.symbols[0] != "NEW" {
		t.Fatalf("expected only NEW to fire, got %v", rec.symbols)
	}
}

// --- ClearAfterSettlement ---

func TestClearAfterSettlement_RemovesAllIntents(t *testing.T) {
	q := NewQueue(noopExec, 5*time.Minute, 20)
	q.Enqueue(newIntent("A", domain.EntryModeFrontrun, 80))
	q.Enqueue(newIntent("B", domain.EntryModeLastMinute, 75))

	q.ClearAfterSettlement()

	if q.PendingCount() != 0 {
		t.Fatalf("expected 0 after settlement, got %d", q.PendingCount())
	}
}

func TestClearAfterSettlement_ResetsWindowFiredFlags(t *testing.T) {
	var fired atomic.Int32
	exec := func(_ context.Context, _ *domain.ScoredCandidate, _ *domain.LLMDecision) error {
		fired.Add(1)
		return nil
	}

	q := NewQueue(exec, 5*time.Minute, 20)
	q.Enqueue(newIntent("A", domain.EntryModeLastMinute, 80))

	q.Tick(context.Background(), scheduler.WindowLastMinute, 0) // fires A
	if fired.Load() != 1 {
		t.Fatal("should have fired once")
	}

	// Second LAST_MINUTE tick within same cycle must not fire again.
	q.Enqueue(newIntent("A2", domain.EntryModeLastMinute, 78))
	q.Tick(context.Background(), scheduler.WindowLastMinute, 0)
	if fired.Load() != 1 {
		t.Fatal("LAST_MINUTE should not fire twice in same cycle")
	}

	q.ClearAfterSettlement() // resets all firedInWindow flags

	// New cycle — LAST_MINUTE fires again.
	q.Enqueue(newIntent("B", domain.EntryModeLastMinute, 78))
	q.Tick(context.Background(), scheduler.WindowLastMinute, 0)
	if fired.Load() != 2 {
		t.Fatalf("expected 2 total fires after settlement reset, got %d", fired.Load())
	}
}

func TestClearAfterSettlement_ResetsFrontrunTimer(t *testing.T) {
	var fired atomic.Int32
	exec := func(_ context.Context, _ *domain.ScoredCandidate, _ *domain.LLMDecision) error {
		fired.Add(1)
		return nil
	}

	q := NewQueue(exec, 1*time.Millisecond, 20)
	q.Enqueue(newIntent("A", domain.EntryModeFrontrun, 80))
	time.Sleep(5 * time.Millisecond)
	q.Tick(context.Background(), scheduler.WindowFrontrun, 19*time.Minute) // fires, sets lastFrontrunExec
	if fired.Load() != 1 {
		t.Fatal("should have fired once")
	}

	q.ClearAfterSettlement() // resets lastFrontrunExec to zero

	// New cycle — without waiting for interval, fire should still work because zero time elapsed check resets.
	q.Enqueue(newIntent("B", domain.EntryModeFrontrun, 82))
	time.Sleep(5 * time.Millisecond)
	q.Tick(context.Background(), scheduler.WindowFrontrun, 19*time.Minute)

	if fired.Load() != 2 {
		t.Fatalf("expected 2 fires after settlement reset, got %d", fired.Load())
	}
}

// --- HasPendingIntent / PendingCount ---

func TestHasPendingIntent_TrueWhenPending(t *testing.T) {
	q := NewQueue(noopExec, 5*time.Minute, 20)
	q.Enqueue(newIntent("A", domain.EntryModeFrontrun, 80))

	if !q.HasPendingIntent() {
		t.Fatal("expected HasPendingIntent = true")
	}
}

func TestHasPendingIntent_FalseAfterClear(t *testing.T) {
	q := NewQueue(noopExec, 5*time.Minute, 20)
	q.Enqueue(newIntent("A", domain.EntryModeFrontrun, 80))
	q.ClearAfterSettlement()

	if q.HasPendingIntent() {
		t.Fatal("expected HasPendingIntent = false after settlement")
	}
}

func TestPendingCount_AccurateAfterMixedOperations(t *testing.T) {
	rec := &recorder{}
	q := NewQueue(rec.exec, 1*time.Millisecond, 20)

	q.Enqueue(newIntent("A", domain.EntryModeFrontrun, 90))
	q.Enqueue(newIntent("B", domain.EntryModeFrontrun, 75))
	q.Enqueue(newIntent("C", domain.EntryModeLastMinute, 80))

	if q.PendingCount() != 3 {
		t.Fatalf("expected 3, got %d", q.PendingCount())
	}

	time.Sleep(5 * time.Millisecond)
	q.Tick(context.Background(), scheduler.WindowFrontrun, 19*time.Minute) // fires A (highest)

	if q.PendingCount() != 2 {
		t.Fatalf("expected 2 after one fire, got %d", q.PendingCount())
	}
}
