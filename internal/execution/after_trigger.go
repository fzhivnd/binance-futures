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
	"futures/internal/scheduler"
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
	executor    Executor
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
	executor Executor,
	cfg *config.Config,
) *AfterTrigger {
	return &AfterTrigger{
		intentQueue: queue,
		strategy:    strategy,
		bookTicker:  bookTicker,
		wsConn:      wsConn,
		binance:     binance,
		fundingInfo: fundingInfo,
		executor:    executor,
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

		// Step 1: sleep until T-2m (scan freeze point). The scheduler stops enqueuing
		// new candidates at T-3m, so by T-2m the queue is stable and the symbol we
		// peek is the same one that will fire at T+0.
		next := scheduler.NextFundingTime(time.Now().UTC())
		wakeAt := next.Add(-2 * time.Minute)
		if d := time.Until(wakeAt); d > 0 {
			slog.Info("AfterTrigger: sleeping until T-2m", "wake_at", wakeAt, "next_funding", next)
			select {
			case <-ctx.Done():
				return
			case <-time.After(d):
			}
		}

		// Step 2: peek the queue for the best AFTER symbol (queue is frozen at this point).
		symbol, ok := t.intentQueue.PeekAfterSymbol()
		if !ok {
			// No AFTER intent this cycle — sleep until T+1m so the next loop iteration
			// lands at the correct T-2m of the following cycle.
			slog.Debug("AfterTrigger: no AFTER intent at T-2m, skipping cycle")
			sleepUntil := next.Add(1 * time.Minute)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Until(sleepUntil)):
			}
			continue
		}

		fi, found := t.fundingInfo.GetFundingInfo(symbol)
		if !found || fi.NextFunding.IsZero() || fi.NextFunding.Before(time.Now()) {
			slog.Warn("AfterTrigger: no valid funding info for symbol, skipping cycle", "symbol", symbol)
			sleepUntil := next.Add(1 * time.Minute)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Until(sleepUntil)):
			}
			continue
		}
		nextFunding := fi.NextFunding

		// Step 3: pre-warm leverage so SetLeverage is not on the critical path at T+0.
		if err := t.executor.SetLeverage(ctx, symbol, t.cfg.Trading.Leverage); err != nil {
			slog.Warn("AfterTrigger: pre-warm leverage failed, will retry at execution", "symbol", symbol, "error", err)
		} else {
			slog.Info("AfterTrigger: leverage pre-warmed", "symbol", symbol, "leverage", t.cfg.Trading.Leverage)
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

		// Step 4: sync clock and arm timer for T+0.
		t.syncClockIfStale(ctx)
		now := t.adjustedNow()
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

		// Step 5: fire — atomically claim the intent.
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
		if err := t.strategy.Execute(ctx, ti); err != nil {
			slog.Error("AfterTrigger: strategy execute failed", "symbol", sym, "error", err)
		}

		expectedBid := t.strategy.ExpectedBidAtEntry(sym)
		latencyMs := float64(time.Since(fireTime).Microseconds()) / 1000.0
		slog.Info("after_trigger_fired",
			"symbol", sym,
			"latency_ms", latencyMs,
			"expected_bid", expectedBid,
		)

		// Step 6: unsubscribe @bookTicker after AFTER window closes (T+60s).
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
