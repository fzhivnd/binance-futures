package execution

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"futures/internal/config"
	"futures/internal/domain"
	"futures/internal/exchange"
	"futures/internal/intent"
	"futures/internal/market"
)

// FundingInfoSource is the subset of MarketEngine needed by AfterTrigger to resolve
// next funding time for a symbol.
type FundingInfoSource interface {
	GetFundingInfo(symbol string) (*domain.FundingRate, bool)
}

// ServerTimeProvider can fetch Binance server time for clock synchronisation.
type ServerTimeProvider interface {
	GetServerTime(ctx context.Context) (time.Time, error)
}

// AfterTrigger fires AFTER intents at T+0 with sub-10ms precision.
// It watches the intent queue, subscribes @bookTicker at T-5s for the target symbol,
// syncs the local clock against Binance server time, and fires at T+0.
//
// Because ClaimAfterIntent atomically sets firedInWindow[WindowAfter] = true, the
// scheduler's tickTransitionLocked will see the flag and skip — preventing double-fire.
// If this goroutine panics or times out, the scheduler remains as a fallback (fires on
// the next 1-second tick, up to ~1s late).
type AfterTrigger struct {
	intentQueue *intent.Queue
	strategy    *AfterExecutionStrategy
	bookTicker  *market.BookTickerCache
	wsConn      *exchange.WSConnection // combined market stream for subscribe/unsubscribe
	binance     ServerTimeProvider
	fundingInfo FundingInfoSource
	cfg         *config.Config

	mu            sync.Mutex
	clockOffset   time.Duration
	lastClockSync time.Time
}

func NewAfterTrigger(
	queue *intent.Queue,
	strategy *AfterExecutionStrategy,
	bookTicker *market.BookTickerCache,
	wsConn *exchange.WSConnection,
	binance ServerTimeProvider,
	fundingInfo FundingInfoSource,
	cfg *config.Config,
) *AfterTrigger {
	return &AfterTrigger{
		intentQueue: queue,
		strategy:    strategy,
		bookTicker:  bookTicker,
		wsConn:      wsConn,
		binance:     binance,
		fundingInfo: fundingInfo,
		cfg:         cfg,
	}
}

// Run is the main loop goroutine.
func (t *AfterTrigger) Run(ctx context.Context) {
	if !t.cfg.AfterExecution.Enabled {
		slog.Info("AfterTrigger disabled by config")
		return
	}

	slog.Info("AfterTrigger started")
	t.syncClock(ctx)

	for {
		if ctx.Err() != nil {
			return
		}

		// Step 1: wait until there is a pending AFTER intent.
		if !t.intentQueue.HasAfterIntent() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}

		// Step 2: determine the next funding time across all tracked symbols.
		// We use the funding data for the most-negative-funding symbol as proxy.
		// The scheduler already filtered symbols; the intent queue has the best candidate.
		nextFunding, symbol, ok := t.resolveNextFunding()
		if !ok {
			time.Sleep(time.Second)
			continue
		}

		now := t.adjustedNow()
		timeUntil := nextFunding.Sub(now)
		subscribeLeadTime := time.Duration(t.cfg.AfterExecution.SubscribeBeforeSeconds) * time.Second

		// Step 3: sleep until T-subscribeLeadTime.
		if timeUntil > subscribeLeadTime {
			sleepDur := timeUntil - subscribeLeadTime
			slog.Info("AfterTrigger: sleeping until T-subscribeLeadTime",
				"symbol", symbol,
				"sleep", sleepDur.Round(time.Millisecond),
				"next_funding", nextFunding,
			)
			select {
			case <-ctx.Done():
				return
			case <-time.After(sleepDur):
			}
		}

		// Step 4: subscribe @bookTicker for the target symbol.
		streamName := strings.ToLower(symbol) + "@bookTicker"
		if t.wsConn != nil {
			if err := t.wsConn.Subscribe(ctx, []string{streamName}); err != nil {
				slog.Warn("AfterTrigger: subscribe bookTicker failed, will proceed without depth",
					"symbol", symbol, "error", err)
			} else {
				slog.Info("AfterTrigger: subscribed bookTicker", "stream", streamName)
			}
		}

		// Step 5: sync clock and arm timer for T+0.
		t.syncClockIfStale(ctx)
		now = t.adjustedNow()
		timeUntilFiring := nextFunding.Sub(now)

		if timeUntilFiring > 0 {
			slog.Info("AfterTrigger: armed, sleeping until T+0",
				"symbol", symbol,
				"sleep", timeUntilFiring.Round(time.Millisecond),
			)
			select {
			case <-ctx.Done():
				t.unsubscribeBookTicker(ctx, symbol)
				return
			case <-time.After(timeUntilFiring):
			}
		}

		// Step 6: fire — atomically claim the intent.
		fireTime := time.Now()
		ti := t.intentQueue.ClaimAfterIntent()
		if ti == nil {
			// Scheduler already fired this cycle, or queue was drained.
			slog.Info("AfterTrigger: intent already claimed by scheduler (fallback fired)", "symbol", symbol)
			t.unsubscribeBookTicker(ctx, symbol)
			time.Sleep(2 * time.Second)
			continue
		}

		sym := ti.Candidate.Candidate.Symbol
		expectedBid := t.strategy.ExpectedBidAtEntry(sym)

		if err := t.strategy.Execute(ctx, ti); err != nil {
			slog.Error("AfterTrigger: strategy execute failed", "symbol", sym, "error", err)
		}

		latencyMs := float64(time.Since(fireTime).Microseconds()) / 1000.0
		slog.Info("after_trigger_fired",
			"symbol", sym,
			"latency_ms", latencyMs,
			"expected_bid", expectedBid,
		)

		// Step 7: unsubscribe @bookTicker after AFTER window closes (T+60s).
		go func(s string) {
			select {
			case <-ctx.Done():
			case <-time.After(60 * time.Second):
			}
			t.unsubscribeBookTicker(ctx, s)
		}(sym)

		// Reset for next cycle.
		time.Sleep(2 * time.Second)
	}
}

// resolveNextFunding returns the earliest next-funding time across symbols tracked by the
// funding cache. This is an approximation — the AfterTrigger fires for the soonest event.
func (t *AfterTrigger) resolveNextFunding() (time.Time, string, bool) {
	// We use the intent queue's HasAfterIntent result as a gate; here we scan
	// funding info to find which symbol fires soonest.
	// Since we cannot enumerate the queue without claiming, we find the global
	// minimum next funding time from the market engine.
	//
	// The funding cache stores a next-funding time per symbol updated from
	// the !markPrice@arr@1s stream (every second). We want the soonest one
	// that's still in the future.
	if t.fundingInfo == nil {
		return time.Time{}, "", false
	}

	// We ask the fundingInfo for a broad set of well-known symbols that will
	// be in the queue. Since we can't enumerate all symbols from the interface,
	// we rely on the intent queue's HasAfterIntent flag being set which means
	// at least one symbol has an AFTER intent with a known next funding time.
	// We use a small trick: the symbols that triggered AFTER intents are known
	// to the queue; the trigger fires for the globally next settlement.
	//
	// Practical implementation: we call GetFundingInfo on top symbols tracked
	// by the engine. Since the engine maintains all symbols from the stream,
	// we can't enumerate them here without adding an extra interface method.
	// Instead, we use the FundingInfoAll approach below via a type assertion.
	type allFundingProvider interface {
		GetAllFundingRates() map[string]float64
		GetFundingInfo(symbol string) (*domain.FundingRate, bool)
	}

	afp, ok := t.fundingInfo.(allFundingProvider)
	if !ok {
		return time.Time{}, "", false
	}

	all := afp.GetAllFundingRates()
	var earliest time.Time
	var earliestSymbol string
	now := time.Now()

	for sym := range all {
		fi, ok := afp.GetFundingInfo(sym)
		if !ok || fi.NextFunding.IsZero() {
			continue
		}
		if fi.NextFunding.Before(now) {
			continue
		}
		if earliest.IsZero() || fi.NextFunding.Before(earliest) {
			earliest = fi.NextFunding
			earliestSymbol = sym
		}
	}

	if earliest.IsZero() {
		return time.Time{}, "", false
	}
	return earliest, earliestSymbol, true
}

func (t *AfterTrigger) unsubscribeBookTicker(ctx context.Context, symbol string) {
	if t.wsConn == nil {
		return
	}
	stream := strings.ToLower(symbol) + "@bookTicker"
	if err := t.wsConn.Unsubscribe(ctx, []string{stream}); err != nil {
		slog.Warn("AfterTrigger: unsubscribe bookTicker failed", "symbol", symbol, "error", err)
	}
}

func (t *AfterTrigger) syncClock(ctx context.Context) {
	if t.binance == nil {
		return
	}
	before := time.Now()
	serverTime, err := t.binance.GetServerTime(ctx)
	if err != nil {
		slog.Warn("AfterTrigger: clock sync failed, using previous offset", "error", err)
		return
	}
	rtt := time.Since(before)
	localMidpoint := before.Add(rtt / 2)
	offset := serverTime.Sub(localMidpoint)

	t.mu.Lock()
	t.clockOffset = offset
	t.lastClockSync = time.Now()
	t.mu.Unlock()

	slog.Info("after_clock_offset_ms",
		"offset_ms", float64(offset.Milliseconds()),
		"rtt_ms", float64(rtt.Milliseconds()),
	)
}

func (t *AfterTrigger) syncClockIfStale(ctx context.Context) {
	t.mu.Lock()
	interval := time.Duration(t.cfg.AfterExecution.ClockSyncIntervalSeconds) * time.Second
	stale := time.Since(t.lastClockSync) > interval
	t.mu.Unlock()
	if stale {
		t.syncClock(ctx)
	}
}

func (t *AfterTrigger) adjustedNow() time.Time {
	t.mu.Lock()
	offset := t.clockOffset
	t.mu.Unlock()
	return time.Now().Add(offset)
}
