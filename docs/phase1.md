# Phase 1: Core Infrastructure — Design Document

---

## 1. Summary

Phase 1 delivers the foundational infrastructure for the Binance Futures Funding-Rate Trading Bot. This phase establishes real-time market data ingestion, funding rate scanning, candlestick aggregation, and order execution capabilities — all without LLM integration.

### What Phase 1 Delivers

| Component | Description                                                                                                                 |
|-----------|-----------------------------------------------------------------------------------------------------------------------------|
| Market Data Engine | WebSocket streaming of funding rates, prices, and klines for all USDT-M perpetuals                                          |
| Funding Rate Scanner | Identifies coins with extreme negative funding (-0.2% to -2%), must have 4h/8h funding time, and nearest funding reset time |
| Candle Aggregation | Multi-timeframe candlestick storage (5m, 15m, 30m, 1h)                                                                      |
| Order Execution | Both paper (simulated) and live (real) trading modes via config toggle                                                      |
| Scheduler | Timing engine around funding windows (T-30m, T-5m, T+0)                                                                     |
| Database Layer | PostgreSQL for trade persistence, Redis for runtime state                                                                   |
| Docker Compose | Local development environment with all dependencies                                                                         |

### What Phase 1 Does NOT Include

- LLM decision engine (Phase 3)
- Indicator scoring system beyond basic funding filter (Phase 2)
- pgvector memory (Phase 4)
- Dynamic SL/trailing stop intelligence (Phase 5)
- Telegram notifications (Phase 6)

### Tech Stack

| Layer | Choice | Reason |
|-------|--------|--------|
| Language | Go 1.25 | Low latency, native concurrency, small binary |
| Database | PostgreSQL 16 | Trade records, risk state |
| Cache | Redis 7 | Runtime state, locks, market snapshots |
| WebSocket | `github.com/coder/websocket` | Modern, context-aware Go WS client |
| DB Driver | `github.com/jackc/pgx/v5` | Fastest Go Postgres driver |
| Redis Client | `github.com/redis/go-redis/v9` | Standard Go Redis client |
| Config | `gopkg.in/yaml.v3` | YAML parsing with env expansion |
| Migrations | `github.com/golang-migrate/migrate/v4` | SQL migration runner |
| Logging | `log/slog` (stdlib) | Structured logging, no external dep |
| Deployment | Docker Compose | Local-first development |

---

## 2. Project Structure

```
futures/
├── cmd/
│   └── bot/
│       └── main.go                 # Entry point, DI wiring, graceful shutdown
├── internal/
│   ├── config/
│   │   ├── config.go               # Config struct + YAML loader
│   │   └── validate.go             # Config validation rules
│   ├── domain/
│   │   ├── candle.go               # Candle, Timeframe types
│   │   ├── funding.go              # FundingRate, FundingSnapshot types
│   │   ├── order.go                # Order, OrderType, Side enums
│   │   ├── position.go             # Position, PositionState types
│   │   ├── symbol.go               # Symbol info, trading rules
│   │   └── ticker.go               # TickerPrice, MarkPrice types
│   ├── exchange/
│   │   ├── binance_client.go       # REST API client (exchange info, orders)
│   │   ├── binance_ws.go           # WebSocket connection manager
│   │   ├── stream_router.go        # Demux WS messages to typed handlers
│   │   └── types.go                # Raw Binance API response structs
│   ├── market/
│   │   ├── engine.go               # MarketEngine orchestrator
│   │   ├── funding_cache.go        # In-memory funding rate cache
│   │   ├── candle_store.go         # Candle ring buffer per symbol/timeframe
│   │   ├── ticker_cache.go         # Latest price cache
│   │   └── oi_cache.go             # Open interest cache (REST-polled)
│   ├── scanner/
│   │   ├── funding_scanner.go      # Extreme funding detection + ranking
│   │   └── candidate.go            # Candidate struct, filter criteria
│   ├── execution/
│   │   ├── engine.go               # Execution orchestrator
│   │   ├── executor.go             # Executor interface definition
│   │   ├── live_executor.go        # Real Binance order placement
│   │   ├── paper_executor.go       # Simulated order fills
│   │   └── position_manager.go     # Position lifecycle (open/close, SL/TP)
│   ├── scheduler/
│   │   ├── scheduler.go            # Funding window scheduler loop
│   │   └── window.go               # Time calculations (next funding, window type)
│   ├── storage/
│   │   ├── postgres.go             # PostgreSQL connection pool + health check
│   │   ├── trade_repo.go           # Trades CRUD operations
│   │   ├── risk_repo.go            # Risk state CRUD operations
│   │   └── redis.go                # Redis client + state cache implementation
│   └── app/
│       └── app.go                  # Application lifecycle orchestrator
├── migrations/
│   ├── 001_create_trades.up.sql
│   ├── 001_create_trades.down.sql
│   ├── 002_create_risk_state.up.sql
│   └── 002_create_risk_state.down.sql
├── config/
│   ├── config.yaml                 # Default configuration
│   └── config.example.yaml         # Template (committed to repo)
├── docker-compose.yml
├── Dockerfile
├── Makefile
├── go.mod
└── go.sum
```

### Package Responsibilities

| Package | Responsibility | Dependencies |
|---------|---------------|--------------|
| `cmd/bot` | Wire dependencies, start app, handle OS signals | `internal/app` |
| `internal/config` | Load YAML, expand env vars, validate | None |
| `internal/domain` | Pure data types, no business logic | None |
| `internal/exchange` | Binance API communication (WS + REST) | `domain` |
| `internal/market` | Cache and aggregate market data | `domain`, `exchange` |
| `internal/scanner` | Filter and rank funding candidates | `domain`, `market` |
| `internal/execution` | Place/manage orders (paper + live) | `domain`, `exchange`, `market` |
| `internal/scheduler` | Drive scan cycles on funding windows | `scanner`, `execution`, `storage` |
| `internal/storage` | PostgreSQL + Redis data access | `domain` |
| `internal/app` | Lifecycle: boot, wire, run, shutdown | All internal packages |

---

## 3. System Design

### 3.1 Core Interfaces

```go
// === Executor Interface (paper/live abstraction point) ===

type Executor interface {
    PlaceMarketOrder(ctx context.Context, req OrderRequest) (*OrderResult, error)
    PlaceLimitOrder(ctx context.Context, req OrderRequest) (*OrderResult, error)
    CancelOrder(ctx context.Context, symbol string, orderID string) error
    GetPosition(ctx context.Context, symbol string) (*Position, error)
    GetAccountBalance(ctx context.Context) (*Balance, error)
    SetLeverage(ctx context.Context, symbol string, leverage int) error
}

// === Market Data ===

type MarketDataProvider interface {
    GetFundingRate(symbol string) (float64, bool)
    GetPrice(symbol string) (float64, bool)
    GetCandles(symbol string, tf Timeframe, limit int) []Candle
    GetOpenInterest(symbol string) (float64, bool)
    GetAllFundingRates() map[string]float64
}

// === Scanner ===

type FundingScanner interface {
    Scan(ctx context.Context) ([]Candidate, error)
}

// === Trade Repository ===

type TradeRepository interface {
    Insert(ctx context.Context, trade *Trade) error
    GetByID(ctx context.Context, id uuid.UUID) (*Trade, error)
    GetRecent(ctx context.Context, limit int) ([]Trade, error)
    UpdateResult(ctx context.Context, id uuid.UUID, exit ExitInfo) error
    GetDailyLossCount(ctx context.Context, date time.Time) (int, error)
}

// === State Cache (Redis) ===

type StateCache interface {
    // Position tracking
    GetActivePositions(ctx context.Context) ([]Position, error)
    SetActivePosition(ctx context.Context, pos Position) error
    RemovePosition(ctx context.Context, symbol string) error

    // Cooldown
    IsOnCooldown(ctx context.Context) (bool, error)
    SetCooldown(ctx context.Context, duration time.Duration) error

    // Kill switch
    GetKillSwitch(ctx context.Context) (bool, error)
    SetKillSwitch(ctx context.Context, active bool) error

    // Scheduler locks
    AcquireSchedulerLock(ctx context.Context, window string, ttl time.Duration) (bool, error)

    // Market snapshots
    SetFundingSnapshot(ctx context.Context, rates map[string]float64) error
    GetFundingSnapshot(ctx context.Context) (map[string]float64, error)
}
```

### 3.2 Domain Types

```go
// === Core Trading Types ===

type Side string
const (
    SideBuy  Side = "BUY"
    SideSell Side = "SELL"
)

type OrderType string
const (
    OrderTypeMarket OrderType = "MARKET"
    OrderTypeLimit  OrderType = "LIMIT"
)

type Timeframe string
const (
    Timeframe5m  Timeframe = "5m"
    Timeframe15m Timeframe = "15m"
    Timeframe30m Timeframe = "30m"
    Timeframe1h  Timeframe = "1h"
)

type WindowType string
const (
    WindowFrontrun   WindowType = "FRONTRUN"    // T-30m to T-5m
    WindowLastMinute WindowType = "LAST_MINUTE" // T-5m to T+0
    WindowAfter      WindowType = "AFTER"       // T+0 to T+1m
)

type EntryMode string
const (
    EntryModeFrontrun   EntryMode = "FRONTRUN"
    EntryModeLastMinute EntryMode = "LAST_MINUTE"
    EntryModeAfter      EntryMode = "AFTER"
)

// === Data Structs ===

type Candle struct {
    Symbol    string
    Timeframe Timeframe
    OpenTime  time.Time
    Open      float64
    High      float64
    Low       float64
    Close     float64
    Volume    float64
    CloseTime time.Time
    IsClosed  bool
}

type FundingRate struct {
    Symbol      string
    Rate        float64
    NextFunding time.Time
    MarkPrice   float64
    UpdatedAt   time.Time
}

type Position struct {
    Symbol       string
    Side         Side
    EntryPrice   float64
    Quantity     float64
    Leverage     int
    EntryMode    EntryMode
    StopLoss     float64
    TakeProfit   float64
    OpenedAt     time.Time
    IsPaper      bool
}

type OrderRequest struct {
    Symbol   string
    Side     Side
    Type     OrderType
    Quantity float64
    Price    float64    // only for limit orders
    StopLoss float64   // SL price
    TakeProfit float64 // TP price
}

type OrderResult struct {
    OrderID   string
    Symbol    string
    Side      Side
    FillPrice float64
    Quantity  float64
    Status    string
    IsPaper   bool
    Timestamp time.Time
}

type Candidate struct {
    Symbol      string
    FundingRate float64
    MarkPrice   float64
    DailyROI    float64
    Score       float64  // simple score based on funding severity
}

type Trade struct {
    ID          uuid.UUID
    Symbol      string
    Side        Side
    EntryMode   EntryMode
    Leverage    int
    Confidence  int
    FundingRate float64
    DailyROI    float64
    EntryPrice  float64
    ExitPrice   float64
    PnL         float64
    Result      string   // "WIN", "LOSS", "BREAKEVEN"
    IsPaper     bool
    CreatedAt   time.Time
    ClosedAt    *time.Time
}
```

### 3.3 Concurrency Model

```
main goroutine
    │
    ├── app.Run(ctx) ──────────────────────────────────────────────
    │                                                              │
    ├── [goroutine] WS Connection Manager (markPrice stream)       │
    │       ├── readPump   → reads frames, sends to MarketEngine   │
    │       └── writePump  → sends pings, subscribe messages       │
    │                                                              │
    ├── [goroutine] WS Connection Manager (kline streams)          │
    │       ├── readPump   → reads frames, sends to MarketEngine   │
    │       └── writePump  → dynamic subscribe/unsubscribe         │
    │                                                              │
    ├── [goroutine] MarketEngine.Run()                             │
    │       └── select on channels, updates caches (mutex-guarded) │
    │                                                              │
    ├── [goroutine] OI Poller (REST, every 5 minutes)              │
    │       └── polls /fapi/v1/openInterest for candidate symbols  │
    │                                                              │
    ├── [goroutine] Scheduler.Run()                                │
    │       └── 1s ticker → evaluate window → trigger scan cycle   │
    │                                                              │
    ├── [goroutine] PositionManager.Run()                          │
    │       └── 1s ticker → check SL/TP for all open positions     │
    │                                                              │
    └── [goroutine] Shutdown handler (SIGINT/SIGTERM)              │
    │       └── cancels root context → all goroutines exit cleanly │
    │                                                              │
    └── [goroutine] Funding interval handler                       │
            └── polls /fapi/v1/fundingInfo for every symbols and   │
            store in in-memory cache                               │
    ────────────────────────────────────────────────────────────────
```

**Synchronization primitives:**

| Resource | Sync Mechanism | Reason |
|----------|---------------|--------|
| Funding rate map | `sync.RWMutex` | High read (scanner), low write (WS updates) |
| Ticker price map | `sync.RWMutex` | Same pattern |
| Candle store | `sync.RWMutex` per symbol | Reads from scanner, writes from WS |
| Active positions | Redis | Shared state, survives restart |
| Scheduler locks | Redis SETNX | Prevent duplicate scan cycles |

**Channel topology:**

```go
type Channels struct {
    MarkPriceUpdates chan []MarkPriceEvent  // WS → MarketEngine (buffered: 16)
    KlineUpdates     chan KlineEvent         // WS → MarketEngine (buffered: 256)
    ScanTrigger      chan WindowType         // Scheduler → Scanner (unbuffered)
}
```

### 3.4 Paper vs Live Mode Architecture

The mode is selected at startup via config and cannot change at runtime.

```
┌─────────────────────────────────────────────┐
│              Execution Engine                │
│                                             │
│  ┌─────────────────────────────────────┐    │
│  │         Executor Interface           │    │
│  └──────────┬──────────────┬───────────┘    │
│             │              │                │
│   ┌─────────▼────┐  ┌─────▼──────────┐     │
│   │ LiveExecutor  │  │ PaperExecutor  │     │
│   │ (Binance API) │  │ (In-memory)    │     │
│   └──────────────┘  └────────────────┘     │
│                                             │
│  ┌─────────────────────────────────────┐    │
│  │       Position Manager               │    │
│  │  (same logic for both modes)         │    │
│  └─────────────────────────────────────┘    │
└─────────────────────────────────────────────┘
```

**Key design rules:**
- Paper executor reads REAL market prices from ticker cache
- Paper executor simulates fills with configurable slippage (default: 1 basis point)
- Both modes write to the same `trades` table (distinguished by `is_paper` column)
- Position manager logic (SL/TP checks) is identical for both modes
- Paper executor tracks a virtual balance in memory (configurable starting balance)

---

## 4. Flow

### 4.1 Startup Flow

```
1. Load config.yaml + env overrides
2. Validate config (API keys present for live mode, DB connection params)
3. Connect PostgreSQL → run migrations
4. Connect Redis → ping health check
5. Fetch GET /fapi/v1/exchangeInfo → extract all USDT-M PERPETUAL symbols
6. Initialize MarketEngine (empty caches)
7. Connect WebSocket #1: !markPrice@arr@1s
8. Wait for first markPrice message (confirms connectivity)
9. Identify top-20 symbols by most negative funding
10. Connect WebSocket #2: kline streams for top-20 symbols × 4 timeframes
11. Start Scheduler goroutine
12. Start PositionManager goroutine
13. Start OI Poller goroutine
14. Log: "Bot started in {mode} mode, monitoring {N} symbols"
15. Block on context cancellation (SIGINT/SIGTERM)
```

### 4.2 Main Trading Cycle Flow

```
                    ┌──────────────┐
                    │  Scheduler   │
                    │  (1s tick)   │
                    └──────┬───────┘
                           │
                    Is it within a funding window?
                           │
                    No ◄───┴───► Yes
                    │              │
                 (idle)     Should scan now? (interval check via Redis lock)
                                   │
                            No ◄───┴───► Yes
                            │              │
                         (skip)     ┌──────▼───────┐
                                    │ Pre-checks   │
                                    │ - kill switch│
                                    │ - cooldown   │
                                    │ - pos count  │
                                    └──────┬───────┘
                                           │
                                    Any blocker?
                                           │
                                    Yes ◄──┴──► No
                                    │            │
                                 (skip)   ┌──────▼──────────┐
                                          │ FundingScanner   │
                                          │ .Scan()          │
                                          └──────┬──────────┘
                                                 │
                                          Candidates found?
                                                 │
                                          No ◄───┴───► Yes
                                          │              │
                                       (skip)     ┌──────▼──────────┐
                                                  │ ExecutionEngine  │
                                                  │ .Evaluate()      │
                                                  └──────┬──────────┘
                                                         │
                                                  ┌──────▼──────────┐
                                                  │ Executor         │
                                                  │ .PlaceOrder()    │
                                                  └──────┬──────────┘
                                                         │
                                                  ┌──────▼──────────┐
                                                  │ PositionManager  │
                                                  │ .Track()         │
                                                  └──────┬──────────┘
                                                         │
                                                  ┌──────▼──────────┐
                                                  │ TradeRepo        │
                                                  │ .Insert()        │
                                                  └─────────────────┘
```

### 4.3 WebSocket Data Flow

```
Binance WS: !markPrice@arr@1s
       │
       │  JSON array of all symbols' mark prices + funding rates
       │
       ▼
StreamRouter.handleMarkPrice([]byte)
       │
       ├── Parse JSON → []MarkPriceEvent
       │
       ├── For each event:
       │       ├── FundingCache.Update(symbol, rate, nextFundingTime)
       │       └── TickerCache.Update(symbol, markPrice)
       │
       └── Every 60s: check which symbols have most negative funding
               └── DynamicSubscriber.UpdateKlineSubscriptions(topSymbols)


Binance WS: <symbol>@kline_<tf>
       │
       │  Individual kline update per symbol per timeframe
       │
       ▼
StreamRouter.handleKline([]byte)
       │
       ├── Parse JSON → KlineEvent
       │
       └── CandleStore.Update(symbol, timeframe, candle)
               │
               └── If candle.IsClosed:
                       └── Append to ring buffer, evict oldest
```

### 4.4 Position Management Flow

```
User Data Stream (live mode) — ORDER_TRADE_UPDATE events
       │
       ▼
HandleUserDataEvent(event)
       │
       ├── event.Order.OrderStatus == "FILLED" && ReduceOnly?
       │       │
       │       No → ignore (entry fill or cancel ack)
       │       │
       │       Yes → find matching position in Redis by symbol
       │               │
       │               ├── exitPrice = event.Order.AvgPrice  (actual fill price from Binance)
       │               │
       │               ├── result = WIN or LOSS vs entryPrice
       │               │
       │               ├── cancel surviving leg (other SL or TP order)
       │               │
       │               ├── tradeRepo.UpdateResult(exitPrice, pnl, result)
       │               │
       │               ├── cache.RemovePosition(symbol)
       │               │
       │               └── if LOSS → set cooldown in Redis

PositionManager.Run() — 1s tick loop (breakeven only)
       │
       ▼
For each active position in Redis:
       │
       └── Check BREAKEVEN MOVE:
               │
               ├── unrealizedPnL > BreakevenActivationPct AND open >= 5 min?
               │       │
               │       Yes → cancel existing STOP_MARKET SL
               │               → place new STOP_MARKET SL at entryPrice
               │               → mark pos.BreakevenMoved = true
               │               → update Redis
               │
               └── pos.BreakevenMoved == true → skip (nothing to check)
```

### 4.5 Graceful Shutdown Flow

```
SIGINT / SIGTERM received
       │
       ▼
Cancel root context
       │
       ├── Scheduler stops (no new scans)
       │
       ├── PositionManager: final SL/TP check, then stops
       │       (does NOT close positions — they persist in Redis)
       │
       ├── WebSocket connections: send close frame, wait 5s
       │
       ├── OI Poller: stops
       │
       ├── PostgreSQL pool: drain + close
       │
       └── Redis: close
       
       Total shutdown timeout: 10 seconds
       After 10s: force exit
```

---

## 5. Detailed Logic

### 5.1 Scheduler Timing Logic

Binance funding settles at **00:00, 04:00, 08:00, 16:00, 20:00 UTC** (every 4 hours).

```go
// Calculate next funding time
func NextFundingTime(now time.Time) time.Time {
	utc := now.UTC()

	// Binance funding times (UTC)
	fundingHours := []int{0, 4, 8, 12, 16, 20}

	currentHour := utc.Hour()

	// Find next funding hour today
	for _, h := range fundingHours {
		if currentHour < h ||
			(currentHour == h &&
				(utc.Minute() > 0 || utc.Second() > 0 || utc.Nanosecond() > 0)) {

			return time.Date(
				utc.Year(),
				utc.Month(),
				utc.Day(),
				h,
				0,
				0,
				0,
				time.UTC,
			)
		}
	}

	// If already past last funding time today,
	// return tomorrow 00:00 UTC
	tomorrow := utc.AddDate(0, 0, 1)

	return time.Date(
		tomorrow.Year(),
		tomorrow.Month(),
		tomorrow.Day(),
		0,
		0,
		0,
		0,
		time.UTC,
	)
}
```

**Window definitions:**

| Window | Time Range | Scan Interval | Entry Mode |
|--------|-----------|---------------|------------|
| Frontrun | T-30m to T-5m | Every 5 minutes | `FRONTRUN` |
| Last Minute | T-5m to T+0 | Every 1 minute | `LAST_MINUTE` |
| After | T+0 to T+1m | Every 1 minute | `AFTER` |

**Deduplication:** Each scan uses a Redis SETNX lock with key `scan:{timestamp_truncated_to_interval}` and TTL equal to the interval. This prevents duplicate scans if the 1-second ticker fires multiple times within the same interval.

### 5.2 Funding Rate Scanner Logic

```
Input: All funding rates from FundingCache (updated every ~1s via markPrice stream)

Step 1: FILTER
    For each symbol where funding_rate exists:
        - REJECT if funding_rate > -0.002 (above -0.2% — not extreme enough)
        - REJECT if funding_rate < -0.02 (below -2% — too dangerous, squeeze risk)
        - PASS if -0.02 <= funding_rate <= -0.002

Step 2: SCORE (simple, no indicators in Phase 1)
    For each passing symbol:
        score = mapFundingToScore(funding_rate)
        
        Scoring table:
            funding >= -0.002  → 0 (already rejected)
            funding == -0.002  → 40
            -0.005 < funding < -0.002  → 60
            -0.01 < funding < -0.005  → 80
            -0.02 < funding < -0.01   → 90
            funding <= -0.02   → 0 (already rejected)
        
        Linear interpolation between breakpoints.

Step 3: RANK
    Sort candidates by score DESC
    Return top 10 candidates

Output: []Candidate{Symbol, FundingRate, MarkPrice, Score}
```

**Important Phase 1 limitation:** Without the full indicator engine (Phase 2), the scanner only uses funding rate severity for ranking. No RSI, OI, or candle pattern scoring in this phase.

### 5.3 Order Execution Logic

```
Input: Top candidate from scanner + window type

Step 1: VALIDATE
    - Check: account balance >= minimum (e.g., $50)
    - Check: symbol not already in active positions
    - Check: total active positions < max_positions (2)
    - Check: not on cooldown
    - Check: kill switch not active

Step 2: CALCULATE POSITION SIZE
    balance = fixed configurable amount or executor.GetAccountBalance()
    positionSizePct = config.Trading.PositionSizePct  (e.g., 3%)
    margin = balance * positionSizePct / 100
    quantity = margin * leverage / markPrice
    
    Round quantity to symbol's step size (from exchangeInfo)

Step 3: SET LEVERAGE
    executor.SetLeverage(symbol, config.Trading.Leverage)

Step 4: PLACE ORDER
    order = executor.PlaceMarketOrder({
        Symbol:   candidate.Symbol,
        Side:     SELL,              // always SHORT for funding strategy
        Type:     MARKET,
        Quantity: calculatedQty,
    })

Step 5: CALCULATE and Set SL/TP
    // entryPrice = avgPrice from Binance market order response (actual fill price)
    // Falls back to candidate.MarkPrice if avgPrice == 0
    entryPrice = order.FillPrice  // = resp.AvgPrice from Binance
    if entryPrice == 0 { entryPrice = candidate.MarkPrice }
    
    // Stop Loss: max % price move against position
    // For SHORT: SL is ABOVE entry
    // Order type: STOP_MARKET (guaranteed fill, no limit price slippage risk)
    slDistance = entryPrice * (config.Execution.SlPct / 100)
    stopLoss = entryPrice + slDistance
    
    // Take Profit: % price move down + funding fee for FRONTRUN and LAST_MINUTE mode
    // Order type: TAKE_PROFIT (stop-limit with 1% buffer below trigger)
    tpDistance = if frontrun/lastminutes -> entryPrice * (config.Execution.TpPct / 100) + (-funding_rate * entryPrice)
                else entryPrice * (config.Execution.TpPct / 100)
    takeProfit = entryPrice - tpDistance

Step 6: TRACK POSITION
    position = Position{
        Symbol:     candidate.Symbol,
        Side:       SELL,
        EntryPrice: order.FillPrice,
        Quantity:   order.Quantity,
        Leverage:   config.Trading.Leverage,
        EntryMode:  windowType.ToEntryMode(),
        StopLoss:   stopLoss,
        TakeProfit: takeProfit,
        IsPaper:    config.App.Mode == "paper",
    }
    
    stateCache.SetActivePosition(position)
    tradeRepo.Insert(trade)
```

### 5.4 Paper Executor Logic

```go
type PaperExecutor struct {
    balance     float64             // virtual balance (starts from config)
    positions   map[string]*PaperPosition
    tickerCache *market.TickerCache // reads REAL market prices
    slippage    float64             // simulated slippage (default: 0.0001 = 1bp)
    mu          sync.Mutex
}

// PlaceMarketOrder simulates a market fill at current price + slippage
func (p *PaperExecutor) PlaceMarketOrder(ctx context.Context, req OrderRequest) (*OrderResult, error) {
    currentPrice, ok := p.tickerCache.GetPrice(req.Symbol)
    if !ok {
        return nil, fmt.Errorf("no price available for %s", req.Symbol)
    }
    
    // Simulate slippage (worse fill for the trader)
    var fillPrice float64
    if req.Side == SideSell {
        fillPrice = currentPrice * (1 - p.slippage) // sell lower
    } else {
        fillPrice = currentPrice * (1 + p.slippage) // buy higher
    }
    
    // Deduct margin from virtual balance
    margin := req.Quantity * fillPrice / float64(leverage)
    p.balance -= margin
    
    return &OrderResult{
        OrderID:   uuid.New().String(),
        Symbol:    req.Symbol,
        Side:      req.Side,
        FillPrice: fillPrice,
        Quantity:  req.Quantity,
        Status:    "FILLED",
        IsPaper:   true,
        Timestamp: time.Now(),
    }, nil
}
```

### 5.5 Live Executor Logic

```go
type LiveExecutor struct {
    client *exchange.BinanceClient
}

// PlaceMarketOrder sends a real order to Binance Futures
func (l *LiveExecutor) PlaceMarketOrder(ctx context.Context, req OrderRequest) (*OrderResult, error) {
    resp, err := l.client.NewOrder(ctx, exchange.NewOrderRequest{
        Symbol:   req.Symbol,
        Side:     string(req.Side),
        Type:     "MARKET",
        Quantity: formatQuantity(req.Quantity, symbolInfo.StepSize),
    })
    if err != nil {
        return nil, fmt.Errorf("binance order: %w", err)
    }
    
    return &OrderResult{
        OrderID:   resp.OrderID,
        Symbol:    resp.Symbol,
        Side:      Side(resp.Side),
        FillPrice: resp.AvgPrice,
        Quantity:  resp.ExecutedQty,
        Status:    resp.Status,
        IsPaper:   false,
        Timestamp: time.UnixMilli(resp.UpdateTime),
    }, nil
}
```

### 5.6 WebSocket Connection Strategy

**Problem:** ~250 USDT-M perps × 4 timeframes = 1000 kline streams. Binance limit: 200 streams/connection, max 5 connections. Subscribing to all is wasteful.

**Solution: Three-tier subscription model**

```
Tier 1 — Always connected (1 connection):
    Stream: !markPrice@arr@1s
    Purpose: Universal funding rate + price monitoring for ALL symbols
    Data: funding rates, mark prices, next funding time

Tier 2 — Dynamic subscription (1-2 connections):
    Streams: <symbol>@kline_5m, <symbol>@kline_15m, <symbol>@kline_30m, <symbol>@kline_1h
    Purpose: Candlestick data for candidate symbols ONLY
    Symbols: Top 20 by most negative funding rate (re-evaluated every 60s)
    
    When candidate list changes:
        - Unsubscribe streams for removed symbols
        - Subscribe streams for new symbols
        - Use WS SUBSCRIBE/UNSUBSCRIBE method (no reconnection needed)

Tier 3 — User data stream (1 connection, live mode only):
    URL: /ws/<listenKey>   (listenKey from POST /fapi/v1/listenKey)
    Purpose: ORDER_TRADE_UPDATE events — accurate close detection + actual fill price
    Data: order fills, cancels, status changes for this account's orders
    Keepalive: PUT /fapi/v1/listenKey every 30 min (Binance expires key after 60 min)
    
    Events handled:
        - ORDER_TRADE_UPDATE with status=FILLED and reduceOnly=true
          → triggers position close with actual avgPrice from exchange
```

**Connection limits:**
- Top 20 symbols × 4 timeframes = 80 streams → fits in 1 connection
- Total connections: 3 (well within Binance's limits)

### 5.7 WebSocket Reconnection Strategy

```
                    ┌──────────┐
                    │ Connected│
                    └────┬─────┘
                         │
                    message received?
                         │
                  Yes ◄──┴──► No (30s timeout on markPrice stream)
                  │              │
            (continue)    Force reconnect
                              │
                         ┌────▼────┐
                         │Reconnect│
                         └────┬────┘
                              │
                         connect attempt
                              │
                      Success ◄─┴─► Failure
                      │               │
               resubscribe      wait (exponential backoff)
               reset backoff         │
                      │          ┌───▼───┐
                      │          │Backoff │
                      │          │1s→2s→4s│
                      │          │→8s→16s │
                      │          │→30s cap│
                      │          └───┬───┘
                      │              │
                      │         retry (max 5 consecutive failures)
                      │              │
                      │         5 failures?
                      │              │
                      │       No ◄───┴───► Yes
                      │       │              │
                      │    (retry)     ACTIVATE KILL SWITCH
                      │                      │
                      ▼                 STOP TRADING
                 ┌────────┐
                 │Connected│
                 └─────────┘
```

**Reconnection rules:**

| Scenario | Action |
|----------|--------|
| Clean server close | Reconnect after 1s |
| Network error | Exponential backoff: 1s, 2s, 4s, 8s, 16s, 30s (cap) |
| Rate limit (HTTP 429) | Wait 60s, then retry |
| Context cancelled | Exit cleanly, no retry |
| 5 consecutive failures | Set kill switch in Redis, stop all trading |
| Successful reconnect | Resubscribe all streams, reset failure counter |
| No message for 30s (markPrice) | Force reconnect (stream should update every 1s) |

**Health signal:** The `!markPrice@arr@1s` stream pushes data every second. If 30s passes with no message, the connection is considered dead even if TCP is still open.

### 5.8 Risk Controls (Phase 1 Scope)

```
┌─────────────────────────────────────────────────────┐
│                    Risk Checks                        │
│                (run before every trade)               │
│                                                      │
│  1. Kill Switch active?          → BLOCK ALL         │
│  2. On cooldown?                 → SKIP this cycle   │
│  3. Active positions >= 2?       → SKIP this cycle   │
│  4. Daily loss count >= 2?       → DISABLE for today │
│  5. WebSocket stale (>10s)?      → SKIP this cycle   │
│                                                      │
└─────────────────────────────────────────────────────┘
```

**Kill switch triggers (automatic):**
- 5 consecutive WebSocket reconnection failures
- Binance API returns HTTP 5xx three times in a row
- Order placement returns unexpected error (insufficient balance, invalid symbol)

**Cooldown:** After any position closes as LOSS, set a 15-minute cooldown in Redis (TTL-based, auto-expires).

**Daily loss limit:** After 2 losses in one calendar day (UTC), set `disabled=true` in `risk_state` table. Reset at 00:00 UTC.

### 5.9 Database Schema

```sql
-- migrations/001_create_trades.up.sql

CREATE TABLE IF NOT EXISTS trades (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    symbol       VARCHAR(32) NOT NULL,
    side         VARCHAR(8) NOT NULL,
    entry_mode   VARCHAR(32) NOT NULL,
    leverage     INT NOT NULL,
    confidence   INT DEFAULT 0,
    funding_rate NUMERIC(10,6),
    daily_roi    NUMERIC(10,4),
    entry_price  NUMERIC(20,8) NOT NULL,
    exit_price   NUMERIC(20,8),
    pnl          NUMERIC(20,8),
    result       VARCHAR(16),         -- WIN, LOSS, BREAKEVEN, NULL (open)
    is_paper     BOOLEAN NOT NULL DEFAULT true,
    created_at   TIMESTAMP NOT NULL DEFAULT NOW(),
    closed_at    TIMESTAMP
);

CREATE INDEX idx_trades_symbol ON trades(symbol);
CREATE INDEX idx_trades_created_at ON trades(created_at DESC);
CREATE INDEX idx_trades_result ON trades(result) WHERE result IS NOT NULL;
```

```sql
-- migrations/002_create_risk_state.up.sql

CREATE TABLE IF NOT EXISTS risk_state (
    trade_date       DATE PRIMARY KEY,
    daily_loss_count INT NOT NULL DEFAULT 0,
    disabled         BOOLEAN NOT NULL DEFAULT false,
    updated_at       TIMESTAMP NOT NULL DEFAULT NOW()
);
```

### 5.10 Redis Key Layout

```
positions:active          HASH    {symbol → JSON(Position)}
state:cooldown            STRING  "1" (with TTL = cooldown duration)
state:kill_switch         STRING  "1" or "0"
lock:scan:{timestamp}     STRING  "1" (with TTL = scan interval)
market:funding_snapshot   HASH    {symbol → rate} (updated every markPrice cycle)
meta:last_ws_message      STRING  Unix timestamp (health check)
```

### 5.11 Configuration Reference

```yaml
app:
  mode: "paper"                  # "paper" | "live"
  log_level: "info"              # "debug" | "info" | "warn" | "error"
  paper_balance: 1000.0          # Starting balance for paper mode (USDT)

binance:
  api_key: "${BINANCE_API_KEY}"
  api_secret: "${BINANCE_API_SECRET}"
  base_url: "https://fapi.binance.com"
  ws_url: "wss://fstream.binance.com"
  testnet: false                 # If true, use testnet endpoints

trading:
  max_positions: 2               # Max simultaneous open positions
  leverage: 20                   # Default leverage for all trades
  position_size_pct: 3.0         # % of balance per trade
  cooldown_minutes: 15           # Cooldown after a loss

funding:
  min_rate: -0.02                # Most negative allowed (-2%)
  max_rate: -0.002               # Least negative required (-0.2%)
  top_candidates: 10             # Max candidates returned by scanner

execution:
  sl_pct: 5.0                    # Stop loss: max account loss % per trade
  tp_pct: 2.0                    # Take profit: price move %
  trailing_enabled: false        # Phase 1: disabled (Phase 5 feature)
  trailing_activation_pct: 1.5   # Activate trailing after this % profit
  breakeven_activation_pct: 1.0  # Move SL to entry after this % profit
  slippage_bps: 1                # Paper mode slippage simulation (basis points)

scheduler:
  scan_interval_early: "5m"      # T-30m to T-5m scan frequency
  scan_interval_late: "1m"       # T-5m to T+1m scan frequency
  window_start_minutes: 30       # Begin scanning N minutes before funding

websocket:
  ping_interval: "2m"            # Send ping every N
  stale_timeout: "30s"           # Force reconnect after N without messages
  max_reconnect_failures: 5      # Kill switch after N consecutive failures
  reconnect_base_backoff: "1s"   # Initial backoff duration
  reconnect_max_backoff: "30s"   # Maximum backoff duration

database:
  postgres:
    host: "localhost"
    port: 5432
    user: "futures"
    password: "${POSTGRES_PASSWORD}"
    dbname: "futures_bot"
    sslmode: "disable"
    max_conns: 10
  redis:
    addr: "localhost:6379"
    password: ""
    db: 0
```

### 5.12 Docker Compose Setup

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: futures
      POSTGRES_PASSWORD: futures_dev
      POSTGRES_DB: futures_bot
    ports:
      - "5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U futures"]
      interval: 5s
      timeout: 5s
      retries: 5

  redis:
    image: redis:7-alpine
    ports:
      - "6379:6379"
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 5s
      retries: 5

  bot:
    build:
      context: .
      dockerfile: Dockerfile
    depends_on:
      postgres:
        condition: service_healthy
      redis:
        condition: service_healthy
    environment:
      - BINANCE_API_KEY=${BINANCE_API_KEY}
      - BINANCE_API_SECRET=${BINANCE_API_SECRET}
      - POSTGRES_PASSWORD=futures_dev
    volumes:
      - ./config:/app/config:ro
    restart: unless-stopped

volumes:
  pgdata:
```

### 5.13 Build Order (Implementation Sequence)

```
Week 1: Foundation
    [1] internal/config/         — YAML loader, env expansion, validation
    [2] internal/domain/         — All pure types (no logic)
    [3] internal/storage/        — PostgreSQL pool, migrations, Redis client
    [4] docker-compose.yml       — Postgres + Redis containers
    [5] Makefile                 — build, run, migrate, test targets

Week 2: Market Data
    [6] internal/exchange/types.go        — Binance response structs
    [7] internal/exchange/binance_ws.go   — WS connection + reconnect
    [8] internal/exchange/stream_router.go — Message demuxing
    [9] internal/market/                  — All caches (funding, ticker, candle, OI)

Week 3: Scanner + Scheduler
    [10] internal/scanner/       — Funding filter + ranking
    [11] internal/scheduler/     — Window calculation + scan loop

Week 4: Execution + Integration
    [12] internal/execution/     — Executor interface, paper, live, position mgr
    [13] internal/exchange/binance_client.go — REST client (orders, leverage)
    [14] internal/app/app.go     — Wire everything together
    [15] cmd/bot/main.go         — Entry point + signal handling
```

### 5.14 Makefile Targets

```makefile
.PHONY: build run test migrate-up migrate-down docker-up docker-down

build:
	go build -o bin/bot ./cmd/bot

run: build
	./bin/bot -config config/config.yaml

test:
	go test ./internal/... -v -race

migrate-up:
	migrate -path migrations -database "postgres://futures:futures_dev@localhost:5432/futures_bot?sslmode=disable" up

migrate-down:
	migrate -path migrations -database "postgres://futures:futures_dev@localhost:5432/futures_bot?sslmode=disable" down

docker-up:
	docker compose up -d postgres redis

docker-down:
	docker compose down

lint:
	golangci-lint run ./...
```

---

## 6. Binance API Reference (Phase 1 Endpoints)

### REST Endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/fapi/v1/exchangeInfo` | GET | Get all symbols, trading rules, step sizes |
| `/fapi/v1/openInterest` | GET | Open interest for a symbol |
| `/fapi/v1/order` | POST | Place new order |
| `/fapi/v1/order` | DELETE | Cancel order |
| `/fapi/v2/account` | GET | Account balance + positions |
| `/fapi/v1/leverage` | POST | Set leverage for a symbol |
| `/fapi/v1/fundingRate` | GET | Historical funding rates |

### WebSocket Streams

| Stream | Data | Update Frequency |
|--------|------|-----------------|
| `!markPrice@arr@1s` | All symbols: mark price, funding rate, next funding time | Every 1s |
| `<symbol>@kline_<interval>` | OHLCV candle for one symbol/timeframe | Real-time (on trade) |

### Authentication

- REST: HMAC-SHA256 signature on query string with `timestamp` and `signature` params
- WebSocket: No authentication needed for market data streams
- Order placement REST calls require signed requests

---

## 7. Error Handling Strategy

| Error Type | Response |
|-----------|----------|
| WebSocket disconnect | Exponential backoff reconnect (see 5.7) |
| Binance API 429 (rate limit) | Wait 60s, retry |
| Binance API 5xx | Retry 3x with 2s delay, then kill switch |
| Order rejected (insufficient margin) | Log, skip, do not retry |
| Order rejected (invalid quantity) | Log, adjust to valid step size, retry once |
| PostgreSQL connection lost | pgx pool auto-reconnects; log warning |
| Redis connection lost | Retry with 1s backoff; if 10s of failures, kill switch |
| Panic in goroutine | Recover, log, continue (do not crash entire bot) |

---

## 8. Logging Standards

Use `log/slog` with structured fields:

```go
slog.Info("scan cycle completed",
    "window", "FRONTRUN",
    "candidates", len(candidates),
    "top_symbol", candidates[0].Symbol,
    "top_funding", candidates[0].FundingRate,
)

slog.Warn("ws reconnecting",
    "attempt", attempt,
    "backoff", backoff.String(),
    "last_error", err,
)

slog.Error("order placement failed",
    "symbol", req.Symbol,
    "side", req.Side,
    "quantity", req.Quantity,
    "error", err,
)
```

**Log levels:**
- `DEBUG`: Raw WS messages, cache updates, every ticker tick
- `INFO`: Scan results, orders placed/closed, position updates, startup/shutdown
- `WARN`: Reconnections, stale data, approaching limits
- `ERROR`: Failed operations that affect trading capability
