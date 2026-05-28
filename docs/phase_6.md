# Phase 6: Daily Summary, Telegram Notifications & App Refactor

---

## 1. Overview

Phase 6 adds observability and code organization improvements:

1. **Daily Trade Record** — A scheduled job runs at 00:00 UTC to aggregate the previous day's trades into a summary row (trade count, wins, losses, win rate, total PnL). Always inserts a row, even on zero-trade days.

2. **Telegram Integration** — Real-time notifications for trade open, trade close, daily summary, and risk events (kill switch, daily loss limit, cooldown) via Telegram Bot API.

3. **App Refactor** — Split `app.go` (716 lines) into focused files within the same `app/` package for maintainability.

### What Changes

| Before (Phase 5)                          | After (Phase 6)                                              |
|-------------------------------------------|--------------------------------------------------------------|
| No daily aggregation                      | `daily_summaries` table, populated at 00:00 UTC              |
| No external notifications                 | Telegram alerts for trades, risk events, daily report         |
| Single 716-line `app.go`                  | Split into `app.go`, `scan.go`, `pollers.go`, `websocket.go` |

### What Stays the Same

- Full Phase 1-5 pipeline unchanged (scan → filter → score → memory → LLM → intent → execute → split TP → force-SL)
- All existing domain types, storage interfaces, and execution logic
- Risk engine, memory engine, position manager: unchanged
- WebSocket architecture and market engine: unchanged

---

## 2. Tech Stack Additions

| Component        | Choice                          | Reason                                         |
|------------------|---------------------------------|------------------------------------------------|
| Telegram Client  | `net/http` (direct Bot API)     | Simple REST calls, no heavy SDK needed         |
| Scheduler        | `time.AfterFunc` + daily ticker | Single goroutine, fires at 00:00 UTC           |
| Message Format   | Telegram MarkdownV2             | Rich formatting for trade details              |

### Cost Estimate

| Item                    | Cost        |
|-------------------------|-------------|
| Telegram Bot API        | Free        |
| Daily summary query     | Negligible  |
| Additional DB storage   | ~1 row/day  |

---

## 3. Project Structure (New/Modified Files)

```
internal/
├── app/
│   ├── app.go              # Slimmed: struct + Run() wiring + goroutine launch only
│   ├── scan.go             # NEW: scanFn trading cycle (extracted from Run closure)
│   ├── pollers.go          # NEW: oiPoller, klineSubscriber, fundingIntervalRefresher, keepAliveListenKey, startSkipValidator
│   ├── websocket.go        # NEW: updateKlineSubscriptions, WS setup helpers
│   └── helpers.go          # NEW: confidenceToSize, checkHistoricalPrice
├── notify/
│   ├── telegram.go         # NEW: Telegram Bot API client
│   ├── formatter.go        # NEW: message formatting (trade open/close/summary/risk)
│   └── notifier.go         # NEW: Notifier interface + event dispatch
├── summary/
│   ├── service.go          # NEW: daily summary aggregation logic
│   └── scheduler.go        # NEW: 00:00 UTC daily job scheduler
├── domain/
│   └── summary.go          # NEW: DailySummary type
├── storage/
│   └── summary_repo.go     # NEW: PGSummaryRepository
├── config/
│   └── config.go           # Modified: + telegram + summary config sections

migrations/
├── 007_create_daily_summaries.up.sql
└── 007_create_daily_summaries.down.sql
```

---

## 4. System Design: Daily Trade Record

### Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    DAILY SUMMARY SCHEDULER                        │
├─────────────────────────────────────────────────────────────────┤
│                                                                   │
│  Fires at 00:00 UTC daily:                                        │
│    1. Query trades WHERE created_at between yesterday 00:00-23:59 │
│    2. Aggregate: count, wins, losses, win_rate, total_pnl         │
│    3. INSERT into daily_summaries (always, even if 0 trades)      │
│    4. Send Telegram daily summary notification                    │
│                                                                   │
└─────────────────────────────────────────────────────────────────┘
```

### Aggregation Logic

Trades are grouped by **open time** (`created_at`), not close time. This means:
- A trade opened at 23:50 UTC and closed at 00:05 UTC the next day belongs to the **previous** day
- A trade opened at 00:01 UTC belongs to the **current** day regardless of when it closes
- Trades that are still open (no `closed_at`) are counted in the day they opened but excluded from PnL/result stats

### Edge Cases

| Scenario                          | Behavior                                                  |
|-----------------------------------|-----------------------------------------------------------|
| No trades in the day              | Insert row with all zeros, send "quiet day" notification  |
| Trade still open at 00:00 UTC     | Count in open day's `trade_count`, exclude from win/loss  |
| Bot restarts mid-day              | Summary job uses DB query, not in-memory state            |
| Bot starts after 00:00 UTC        | Detect missed summary on startup, backfill if needed      |
| Multiple summaries for same date  | UNIQUE constraint on `trade_date` prevents duplicates     |

### Backfill on Startup

On app startup, check if yesterday's summary exists. If not, generate and insert it. This handles:
- Bot was down at 00:00 UTC
- Bot crashed and restarted after midnight

```go
func (s *Service) BackfillIfNeeded(ctx context.Context) error {
    yesterday := time.Now().UTC().Add(-24 * time.Hour).Truncate(24 * time.Hour)
    exists, err := s.repo.Exists(ctx, yesterday)
    if err != nil {
        return err
    }
    if !exists {
        return s.GenerateAndStore(ctx, yesterday)
    }
    return nil
}
```

---

## 5. Domain Types

### DailySummary

```go
// internal/domain/summary.go

type DailySummary struct {
    ID         int       `json:"id"`
    TradeDate  time.Time `json:"trade_date"`  // UTC date (truncated to day)
    TradeCount int       `json:"trade_count"` // total trades opened this day
    WinCount   int       `json:"win_count"`   // WIN + PARTIAL_WIN
    LossCount  int       `json:"loss_count"`  // LOSS + FORCE_SL
    WinRate    float64   `json:"win_rate"`    // win_count / (win_count + loss_count), 0 if no closed trades
    TotalPnL   float64   `json:"total_pnl"`   // sum of pnl for closed trades opened this day
    IsPaper    bool      `json:"is_paper"`    // paper or live mode
    CreatedAt  time.Time `json:"created_at"`
}
```

### Result Classification for Summary

| Trade Result   | Counted As |
|----------------|------------|
| `WIN`          | Win        |
| `PARTIAL_WIN`  | Win        |
| `LOSS`         | Loss       |
| `FORCE_SL`     | Loss       |
| `BREAKEVEN`    | Neither    |
| `MANUAL`       | Neither    |

Trades still open (`closed_at IS NULL`) are counted in `trade_count` but excluded from win/loss/pnl.

---

## 6. Database Migration

### `007_create_daily_summaries.up.sql`

```sql
CREATE TABLE IF NOT EXISTS daily_summaries (
    id          SERIAL PRIMARY KEY,
    trade_date  DATE NOT NULL,
    trade_count INTEGER NOT NULL DEFAULT 0,
    win_count   INTEGER NOT NULL DEFAULT 0,
    loss_count  INTEGER NOT NULL DEFAULT 0,
    win_rate    NUMERIC(5,2) NOT NULL DEFAULT 0,
    total_pnl   NUMERIC(20,8) NOT NULL DEFAULT 0,
    is_paper    BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_daily_summaries_date UNIQUE (trade_date, is_paper)
);

CREATE INDEX idx_daily_summaries_date ON daily_summaries (trade_date DESC);
```

### `007_create_daily_summaries.down.sql`

```sql
DROP TABLE IF EXISTS daily_summaries;
```

---

## 7. Daily Summary Service

### `internal/summary/service.go`

```go
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

    summary := s.aggregate(trades, dayStart)

    if err := s.summaryRepo.Upsert(ctx, summary); err != nil {
        return nil, err
    }

    slog.Info("daily_summary_generated",
        "date", dayStart.Format("2006-01-02"),
        "trades", summary.TradeCount,
        "wins", summary.WinCount,
        "losses", summary.LossCount,
        "win_rate", summary.WinRate,
        "pnl", summary.TotalPnL,
    )

    return summary, nil
}

func (s *Service) aggregate(trades []domain.Trade, date time.Time) *domain.DailySummary {
    summary := &domain.DailySummary{
        TradeDate:  date,
        TradeCount: len(trades),
        IsPaper:    s.isPaper,
    }

    for _, t := range trades {
        if t.ClosedAt == nil {
            continue // still open, skip from win/loss/pnl stats
        }

        switch t.Result {
        case "WIN", "PARTIAL_WIN":
            summary.WinCount++
        case "LOSS", "FORCE_SL":
            summary.LossCount++
        }

        if t.PnL != nil {
            summary.TotalPnL += *t.PnL
        }
    }

    decided := summary.WinCount + summary.LossCount
    if decided > 0 {
        summary.WinRate = float64(summary.WinCount) / float64(decided) * 100
    }

    return summary
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
```

### `internal/summary/scheduler.go`

```go
package summary

import (
    "context"
    "log/slog"
    "time"
)

type DailyScheduler struct {
    service    *Service
    onComplete func(context.Context, *domain.DailySummary) // callback for Telegram notification
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
        // Next midnight UTC
        nextMidnight := now.Truncate(24 * time.Hour).Add(24 * time.Hour)
        delay := nextMidnight.Sub(now)

        select {
        case <-ctx.Done():
            return
        case <-time.After(delay):
        }

        // Generate summary for the day that just ended
        yesterday := time.Now().UTC().Add(-1 * time.Second).Truncate(24 * time.Hour)
        summary, err := d.service.GenerateAndStore(ctx, yesterday)
        if err != nil {
            slog.Error("daily summary generation failed", "date", yesterday, "error", err)
            continue
        }

        if d.onComplete != nil {
            d.onComplete(ctx, summary)
        }
    }
}
```

---

## 8. Summary Repository

### `internal/storage/summary_repo.go`

```go
package storage

import (
    "context"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"

    "futures/internal/domain"
)

type PGSummaryRepository struct {
    pool *pgxpool.Pool
}

func NewPGSummaryRepository(pool *pgxpool.Pool) *PGSummaryRepository {
    return &PGSummaryRepository{pool: pool}
}

func (r *PGSummaryRepository) Upsert(ctx context.Context, s *domain.DailySummary) error {
    _, err := r.pool.Exec(ctx, `
        INSERT INTO daily_summaries (trade_date, trade_count, win_count, loss_count, win_rate, total_pnl, is_paper)
        VALUES ($1, $2, $3, $4, $5, $6, $7)
        ON CONFLICT (trade_date, is_paper) DO UPDATE SET
            trade_count = EXCLUDED.trade_count,
            win_count   = EXCLUDED.win_count,
            loss_count  = EXCLUDED.loss_count,
            win_rate    = EXCLUDED.win_rate,
            total_pnl   = EXCLUDED.total_pnl
    `, s.TradeDate, s.TradeCount, s.WinCount, s.LossCount, s.WinRate, s.TotalPnL, s.IsPaper)
    return err
}

func (r *PGSummaryRepository) Exists(ctx context.Context, date time.Time, isPaper bool) (bool, error) {
    var exists bool
    err := r.pool.QueryRow(ctx, `
        SELECT EXISTS(SELECT 1 FROM daily_summaries WHERE trade_date=$1 AND is_paper=$2)
    `, date, isPaper).Scan(&exists)
    return exists, err
}

func (r *PGSummaryRepository) GetByDate(ctx context.Context, date time.Time, isPaper bool) (*domain.DailySummary, error) {
    s := &domain.DailySummary{}
    err := r.pool.QueryRow(ctx, `
        SELECT id, trade_date, trade_count, win_count, loss_count, win_rate, total_pnl, is_paper, created_at
        FROM daily_summaries WHERE trade_date=$1 AND is_paper=$2
    `, date, isPaper).Scan(&s.ID, &s.TradeDate, &s.TradeCount, &s.WinCount, &s.LossCount, &s.WinRate, &s.TotalPnL, &s.IsPaper, &s.CreatedAt)
    if err != nil {
        return nil, err
    }
    return s, nil
}
```

### Trade Repository Addition

```go
// Added to TradeRepository interface
type TradeRepository interface {
    // ... existing methods ...
    FindByOpenTimeRange(ctx context.Context, from, to time.Time, isPaper bool) ([]Trade, error)
}

// Implementation
func (r *PGTradeRepository) FindByOpenTimeRange(ctx context.Context, from, to time.Time, isPaper bool) ([]domain.Trade, error) {
    rows, err := r.pool.Query(ctx, `
        SELECT id, symbol, side, entry_price, avg_close_price, pnl, result, close_reason, is_paper, created_at, closed_at
        FROM trades
        WHERE created_at >= $1 AND created_at < $2 AND is_paper = $3
        ORDER BY created_at ASC
    `, from, to, isPaper)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var trades []domain.Trade
    for rows.Next() {
        var t domain.Trade
        if err := rows.Scan(&t.ID, &t.Symbol, &t.Side, &t.EntryPrice, &t.AvgClosePrice, &t.PnL, &t.Result, &t.CloseReason, &t.IsPaper, &t.CreatedAt, &t.ClosedAt); err != nil {
            return nil, err
        }
        trades = append(trades, t)
    }
    return trades, nil
}
```

---

## 9. System Design: Telegram Integration

### Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                         NOTIFIER                                  │
├─────────────────────────────────────────────────────────────────┤
│                                                                   │
│  Event Sources:                                                   │
│    ├── ExecutionEngine.Execute()     → TradeOpened event          │
│    ├── PositionManager.recordClose() → TradeClosed event         │
│    ├── DailyScheduler.onComplete()   → DailySummary event        │
│    ├── RiskEngine (kill switch)      → KillSwitchActivated event │
│    ├── RiskEngine (daily loss)       → DailyLossLimitHit event   │
│    └── RiskEngine (cooldown)         → CooldownStarted event     │
│                                                                   │
│  Dispatch:                                                        │
│    Event → Formatter → Telegram Bot API                           │
│                                                                   │
│  Delivery:                                                        │
│    - Fire-and-forget (async goroutine per message)                │
│    - Log on failure, don't retry (Telegram is best-effort)        │
│    - Rate limit: max 30 messages/second (Telegram Bot limit)      │
│                                                                   │
└─────────────────────────────────────────────────────────────────┘
```

### Design Principles

1. **Non-blocking** — Notification failures never affect trading logic
2. **Best-effort** — Log errors but don't retry; Telegram is informational
3. **Async dispatch** — Each notification fires in a goroutine to avoid blocking the hot path
4. **Configurable** — Can be disabled entirely via config (`telegram.enabled: false`)
5. **Structured events** — Notifier receives typed events, not raw strings

---

## 10. Notifier Interface

### `internal/notify/notifier.go`

```go
package notify

import (
    "context"
    "log/slog"

    "futures/internal/domain"
)

// Event types sent to the notifier
type TradeOpenedEvent struct {
    Symbol     string
    Side       string
    EntryPrice float64
    Quantity   float64
    Leverage   int
    StopLoss   float64
    TakeProfit float64
    EntryMode  string
    Confidence int
    Score      float64
    IsPaper    bool
}

type TradeClosedEvent struct {
    Symbol        string
    Side          string
    EntryPrice    float64
    ExitPrice     float64
    PnL           float64
    PnLPct        float64  // leveraged PnL %
    Result        string   // WIN, LOSS, PARTIAL_WIN, FORCE_SL, BREAKEVEN
    CloseReason   string   // TP_TRAIL, HARD_SL, FORCE_SL, MANUAL
    HoldDuration  string   // human-readable (e.g. "12m 34s")
    IsPaper       bool
}

type RiskEvent struct {
    Type    string // "kill_switch" | "daily_loss_limit" | "cooldown"
    Message string
}

// Notifier dispatches trade/risk notifications.
type Notifier struct {
    telegram *TelegramClient
    enabled  bool
}

func NewNotifier(cfg TelegramConfig) *Notifier {
    if !cfg.Enabled || cfg.BotToken == "" || cfg.ChatID == "" {
        return &Notifier{enabled: false}
    }
    return &Notifier{
        telegram: NewTelegramClient(cfg.BotToken, cfg.ChatID),
        enabled:  true,
    }
}

func (n *Notifier) NotifyTradeOpened(ctx context.Context, event TradeOpenedEvent) {
    if !n.enabled {
        return
    }
    go func() {
        msg := FormatTradeOpened(event)
        if err := n.telegram.SendMessage(ctx, msg); err != nil {
            slog.Warn("telegram: trade opened notification failed", "symbol", event.Symbol, "error", err)
        }
    }()
}

func (n *Notifier) NotifyTradeClosed(ctx context.Context, event TradeClosedEvent) {
    if !n.enabled {
        return
    }
    go func() {
        msg := FormatTradeClosed(event)
        if err := n.telegram.SendMessage(ctx, msg); err != nil {
            slog.Warn("telegram: trade closed notification failed", "symbol", event.Symbol, "error", err)
        }
    }()
}

func (n *Notifier) NotifyDailySummary(ctx context.Context, summary *domain.DailySummary) {
    if !n.enabled {
        return
    }
    go func() {
        msg := FormatDailySummary(summary)
        if err := n.telegram.SendMessage(ctx, msg); err != nil {
            slog.Warn("telegram: daily summary notification failed", "error", err)
        }
    }()
}

func (n *Notifier) NotifyRiskEvent(ctx context.Context, event RiskEvent) {
    if !n.enabled {
        return
    }
    go func() {
        msg := FormatRiskEvent(event)
        if err := n.telegram.SendMessage(ctx, msg); err != nil {
            slog.Warn("telegram: risk event notification failed", "type", event.Type, "error", err)
        }
    }()
}
```

---

## 11. Telegram Client

### `internal/notify/telegram.go`

```go
package notify

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "time"
)

type TelegramConfig struct {
    Enabled  bool   `yaml:"enabled"`
    BotToken string `yaml:"bot_token"`
    ChatID   string `yaml:"chat_id"`
    Timeout  int    `yaml:"timeout_secs"` // default 10
}

type TelegramClient struct {
    botToken string
    chatID   string
    client   *http.Client
}

func NewTelegramClient(botToken, chatID string) *TelegramClient {
    return &TelegramClient{
        botToken: botToken,
        chatID:   chatID,
        client:   &http.Client{Timeout: 10 * time.Second},
    }
}

func (t *TelegramClient) SendMessage(ctx context.Context, text string) error {
    url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.botToken)

    payload := map[string]interface{}{
        "chat_id":    t.chatID,
        "text":       text,
        "parse_mode": "MarkdownV2",
    }
    body, _ := json.Marshal(payload)

    req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
    if err != nil {
        return err
    }
    req.Header.Set("Content-Type", "application/json")

    resp, err := t.client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("telegram API returned %d", resp.StatusCode)
    }
    return nil
}
```

---

## 12. Message Formatting

### `internal/notify/formatter.go`

```go
package notify

import (
    "fmt"
    "strings"

    "futures/internal/domain"
)

// escapeMarkdownV2 escapes special characters for Telegram MarkdownV2.
func escapeMarkdownV2(s string) string {
    replacer := strings.NewReplacer(
        "_", "\\_", "*", "\\*", "[", "\\[", "]", "\\]",
        "(", "\\(", ")", "\\)", "~", "\\~", "`", "\\`",
        ">", "\\>", "#", "\\#", "+", "\\+", "-", "\\-",
        "=", "\\=", "|", "\\|", "{", "\\{", "}", "\\}",
        ".", "\\.", "!", "\\!",
    )
    return replacer.Replace(s)
}

func FormatTradeOpened(e TradeOpenedEvent) string {
    mode := "LIVE"
    if e.IsPaper {
        mode = "PAPER"
    }

    return fmt.Sprintf(
        "*SHORT OPENED* \\[%s\\]\n\n"+
            "Symbol: `%s`\n"+
            "Entry: `$%s`\n"+
            "Leverage: `%dx`\n"+
            "SL: `$%s` \\| TP: `$%s`\n"+
            "Mode: `%s` \\| Confidence: `%d`\n"+
            "Score: `%.1f` \\| Entry Mode: `%s`",
        escapeMarkdownV2(e.Symbol),
        escapeMarkdownV2(e.Symbol),
        escapeMarkdownV2(fmt.Sprintf("%.8g", e.EntryPrice)),
        e.Leverage,
        escapeMarkdownV2(fmt.Sprintf("%.8g", e.StopLoss)),
        escapeMarkdownV2(fmt.Sprintf("%.8g", e.TakeProfit)),
        mode,
        e.Confidence,
        e.Score,
        escapeMarkdownV2(e.EntryMode),
    )
}

func FormatTradeClosed(e TradeClosedEvent) string {
    mode := "LIVE"
    if e.IsPaper {
        mode = "PAPER"
    }

    emoji := "🔴"
    switch e.Result {
    case "WIN", "PARTIAL_WIN":
        emoji = "🟢"
    case "BREAKEVEN":
        emoji = "⚪"
    case "FORCE_SL":
        emoji = "🟡"
    }

    pnlSign := "+"
    if e.PnL < 0 {
        pnlSign = ""
    }

    return fmt.Sprintf(
        "%s *TRADE CLOSED* \\[%s\\]\n\n"+
            "Symbol: `%s`\n"+
            "Result: `%s` \\(%s\\)\n"+
            "Entry: `$%s` → Exit: `$%s`\n"+
            "PnL: `%s%.4f USDT` \\(`%s%.1f%%`\\)\n"+
            "Hold: `%s`\n"+
            "Mode: `%s`",
        emoji,
        escapeMarkdownV2(e.Symbol),
        escapeMarkdownV2(e.Symbol),
        escapeMarkdownV2(e.Result),
        escapeMarkdownV2(e.CloseReason),
        escapeMarkdownV2(fmt.Sprintf("%.8g", e.EntryPrice)),
        escapeMarkdownV2(fmt.Sprintf("%.8g", e.ExitPrice)),
        pnlSign, e.PnL,
        pnlSign, e.PnLPct,
        escapeMarkdownV2(e.HoldDuration),
        mode,
    )
}

func FormatDailySummary(s *domain.DailySummary) string {
    mode := "LIVE"
    if s.IsPaper {
        mode = "PAPER"
    }

    pnlSign := "+"
    if s.TotalPnL < 0 {
        pnlSign = ""
    }

    status := "📊"
    if s.TradeCount == 0 {
        status = "😴"
    } else if s.TotalPnL > 0 {
        status = "📈"
    } else if s.TotalPnL < 0 {
        status = "📉"
    }

    return fmt.Sprintf(
        "%s *DAILY SUMMARY* \\[%s\\]\n"+
            "Date: `%s`\n\n"+
            "Trades: `%d`\n"+
            "Wins: `%d` \\| Losses: `%d`\n"+
            "Win Rate: `%.1f%%`\n"+
            "Total PnL: `%s%.4f USDT`\n"+
            "Mode: `%s`",
        status,
        escapeMarkdownV2(s.TradeDate.Format("2006-01-02")),
        escapeMarkdownV2(s.TradeDate.Format("2006-01-02")),
        s.TradeCount,
        s.WinCount, s.LossCount,
        s.WinRate,
        pnlSign, s.TotalPnL,
        mode,
    )
}

func FormatRiskEvent(e RiskEvent) string {
    emoji := "⚠️"
    switch e.Type {
    case "kill_switch":
        emoji = "🚨"
    case "daily_loss_limit":
        emoji = "🛑"
    case "cooldown":
        emoji = "⏸️"
    }

    return fmt.Sprintf(
        "%s *RISK ALERT*\n\n"+
            "Type: `%s`\n"+
            "Detail: %s",
        emoji,
        escapeMarkdownV2(e.Type),
        escapeMarkdownV2(e.Message),
    )
}
```

---

## 13. Integration Points

### 13.1 Trade Opened Notification

Triggered in `ExecutionEngine` after successful order placement:

```go
// internal/execution/engine.go (after position stored)

if a.notifier != nil {
    a.notifier.NotifyTradeOpened(ctx, notify.TradeOpenedEvent{
        Symbol:     candidate.Symbol,
        Side:       "SHORT",
        EntryPrice: entryPrice,
        Quantity:   order.Quantity,
        Leverage:   a.cfg.Trading.Leverage,
        StopLoss:   stopLoss,
        TakeProfit: takeProfit,
        EntryMode:  string(window),
        Confidence: confidence,
        Score:      sc.CompositeScore,
        IsPaper:    a.cfg.App.Mode == "paper",
    })
}
```

### 13.2 Trade Closed Notification

Triggered in `PositionManager.recordClose()` after updating the trade record:

```go
// internal/execution/position_manager.go (after UpdateResult)

if m.notifier != nil {
    holdDuration := time.Since(pos.OpenedAt).Round(time.Second).String()
    leveragedPnlPct := pnl / (pos.EntryPrice * pos.OriginalQty) * float64(pos.Leverage) * 100

    m.notifier.NotifyTradeClosed(ctx, notify.TradeClosedEvent{
        Symbol:       pos.Symbol,
        Side:         "SHORT",
        EntryPrice:   pos.EntryPrice,
        ExitPrice:    avgClose,
        PnL:          pnl,
        PnLPct:       leveragedPnlPct,
        Result:       result,
        CloseReason:  closeReason,
        HoldDuration: holdDuration,
        IsPaper:      pos.IsPaper,
    })
}
```

### 13.3 Risk Event Notifications

Triggered in `RiskEngine` when protective actions fire:

```go
// internal/risk/engine.go

// Kill switch activation
func (e *Engine) ActivateKillSwitch(ctx context.Context, reason string) {
    _ = e.cache.SetKillSwitch(ctx, true)
    if e.notifier != nil {
        e.notifier.NotifyRiskEvent(ctx, notify.RiskEvent{
            Type:    "kill_switch",
            Message: "Kill switch activated: " + reason,
        })
    }
}

// Daily loss limit hit
func (e *Engine) onDailyLossLimitHit(ctx context.Context) {
    if e.notifier != nil {
        e.notifier.NotifyRiskEvent(ctx, notify.RiskEvent{
            Type:    "daily_loss_limit",
            Message: fmt.Sprintf("Daily loss limit reached (%d losses). Trading disabled until 00:00 UTC.", e.cfg.MaxDailyLosses),
        })
    }
}

// Cooldown started
func (e *Engine) onCooldownStarted(ctx context.Context, duration time.Duration) {
    if e.notifier != nil {
        e.notifier.NotifyRiskEvent(ctx, notify.RiskEvent{
            Type:    "cooldown",
            Message: fmt.Sprintf("Cooldown started for %s after loss.", duration.Round(time.Minute)),
        })
    }
}
```

### 13.4 Daily Summary Notification

Triggered by the daily scheduler callback:

```go
// Wired in app.go Run()
onDailySummary := func(ctx context.Context, summary *domain.DailySummary) {
    notifier.NotifyDailySummary(ctx, summary)
}
dailySched := summary.NewDailyScheduler(summaryService, onDailySummary)
go dailySched.Run(ctx)
```

---

## 14. Configuration

### Config Struct Additions

```go
// internal/config/config.go

type Config struct {
    // ... existing ...
    Telegram TelegramConfig `yaml:"telegram"`
    Summary  SummaryConfig  `yaml:"summary"`
}

type TelegramConfig struct {
    Enabled  bool   `yaml:"enabled"`
    BotToken string `yaml:"bot_token"`
    ChatID   string `yaml:"chat_id"`
    Timeout  int    `yaml:"timeout_secs"`
}

type SummaryConfig struct {
    Enabled bool `yaml:"enabled"`
}
```

### config.yaml Addition

```yaml
telegram:
  enabled: true
  bot_token: ""           # from @BotFather
  chat_id: ""             # your Telegram user/group chat ID
  timeout_secs: 10

summary:
  enabled: true
```

### Defaults

```go
if cfg.Telegram.Timeout == 0 {
    cfg.Telegram.Timeout = 10
}
```

---

## 15. App Refactor: File Split

### Current `app.go` (716 lines) → Split into 5 files

#### `app.go` — Struct definition + `New()` + `Run()` (wiring + goroutine launch)

```go
package app

// App struct definition with all fields
// New() constructor
// Run() — only service initialization, dependency wiring, and goroutine spawning
//   - Initialize market engine, binance client, exchange info
//   - Initialize executor, scanner, indicators, scorer, risk engine
//   - Initialize execution engine, position manager
//   - Initialize LLM, memory, intent queue
//   - Initialize notifier, daily summary service
//   - Set up WebSocket connections
//   - Start all goroutines
//   - Wait for shutdown

// Target: ~250-300 lines (initialization + wiring only)
```

#### `scan.go` — Core trading cycle

```go
package app

// scanFn(ctx, window) — the full scan cycle:
//   - Risk pre-check
//   - Funding scanner
//   - ROI/Volume filters
//   - BTC context
//   - Indicator compute + scoring
//   - LLM decision path (cooldown check, memory retrieval, evaluate, intent queue)
//   - Phase 2 fallback path

// Target: ~220 lines
```

The current `scanFn` is a closure inside `Run()`. Refactoring it to a method on `*App`:

```go
func (a *App) scanFn(ctx context.Context, window scheduler.WindowType) error {
    // All dependencies accessed via a.* fields (already stored on the struct)
    // No closure captures needed
}
```

#### `pollers.go` — Background polling goroutines

```go
package app

// oiPoller(ctx, client) — 5min OI polling
// klineSubscriber(ctx) — 60s kline subscription rotation
// fundingIntervalRefresher(ctx, client) — post-settlement funding interval refresh
// keepAliveListenKey(ctx, client, listenKey) — 30min listen key ping
// startSkipValidator(ctx) — memory skip validation ticker

// Target: ~120 lines
```

#### `websocket.go` — WebSocket subscription management

```go
package app

// updateKlineSubscriptions(ctx, old, new) — diff-based subscribe/unsubscribe
// setupMarkPriceWS(cfg, router, killFn) — mark price WS setup
// setupKlineWS(cfg, router, killFn) — kline WS setup
// setupUserDataWS(cfg, router, killFn, listenKey) — user data WS setup (live only)

// Target: ~80 lines
```

#### `helpers.go` — Utility functions

```go
package app

// confidenceToSize(confidence) — maps LLM confidence to position size %
// checkHistoricalPrice(symbol, entryTime) — price change lookup for skip validator
// llmCallState struct + dedup logic

// Target: ~50 lines
```

### Migration Strategy

1. Extract methods one file at a time (pollers first — most isolated)
2. No interface changes, no import changes, no behavioral changes
3. The `scanFn` closure converts to `a.scanFn` method — the scheduler receives `a.scanFn` directly
4. All tests continue passing after each extraction (no logic changes)

---

## 16. Dependency Injection Updates

The `Notifier` needs to be wired into `ExecutionEngine`, `PositionManager`, and `RiskEngine`:

```go
// In app.go Run():

// Create notifier
notifier := notify.NewNotifier(notify.TelegramConfig{
    Enabled:  a.cfg.Telegram.Enabled,
    BotToken: a.cfg.Telegram.BotToken,
    ChatID:   a.cfg.Telegram.ChatID,
    Timeout:  a.cfg.Telegram.Timeout,
})

// Wire into components
a.execEng = execution.NewExecutionEngine(
    a.executor, a.engine, cache, tradeRepo, riskRepo, a.cfg, notifier,
)
a.posMgr = execution.NewPositionManager(
    a.executor, a.engine, cache, tradeRepo, riskRepo, a.cfg, notifier,
)
a.riskEngine = risk.NewEngine(cache, tradeRepo, a.drawdown, riskCfg, notifier)
```

Each component stores the notifier as an optional field. If nil, notifications are silently skipped. No behavior changes when Telegram is disabled.

---

## 17. Goroutine Summary (Updated)

After Phase 6, the full goroutine roster:

| #  | Goroutine                     | Source File        | Interval          |
|----|-------------------------------|--------------------|-------------------|
| 1  | WS Mark Price                 | websocket.go       | Continuous        |
| 2  | WS Klines                    | websocket.go       | Continuous        |
| 3  | WS User Data (live only)     | websocket.go       | Continuous        |
| 4  | MarketEngine.Run()           | (market package)   | Channel-driven    |
| 5  | OI Poller                    | pollers.go         | Every 5min        |
| 6  | Kline Subscriber             | pollers.go         | Every 60s         |
| 7  | Funding Interval Refresher   | pollers.go         | Post-settlement   |
| 8  | Scheduler.Run()              | (scheduler package)| Every 1s          |
| 9  | PositionManager.Run()        | (execution package)| Every 1s          |
| 10 | Skip Validator               | pollers.go         | Configurable      |
| 11 | ListenKey Keepalive (live)   | pollers.go         | Every 30min       |
| 12 | **Daily Summary Scheduler**  | (summary package)  | **00:00 UTC**     |

---

## 18. Error Handling

| Scenario                              | Behavior                                                    |
|---------------------------------------|-------------------------------------------------------------|
| Telegram API unreachable              | Log warning, continue trading. Never retry.                 |
| Telegram rate limited (429)           | Log warning, drop message. Bot limit is 30 msg/s — unlikely.|
| Invalid bot token / chat ID           | Notifier created but all sends fail with 401/400. Logged.   |
| DB query fails for daily summary      | Log error, retry next day (or on next restart via backfill).|
| Summary already exists for date       | UPSERT overwrites — idempotent.                             |
| Bot starts after 00:00, missed summary| Backfill on startup detects and generates.                  |
| Trade closes while Telegram is down   | PnL still recorded in DB. Notification lost (acceptable).   |

---

## 19. Testing Strategy

### Unit Tests

| Test                                           | What it validates                                   |
|------------------------------------------------|-----------------------------------------------------|
| `TestAggregate_ZeroTrades`                     | Empty day produces row with all zeros               |
| `TestAggregate_MixedResults`                   | Correct win/loss/pnl counting                       |
| `TestAggregate_OpenTradesExcludedFromPnL`      | Open trades count in total but not in win/loss/pnl  |
| `TestAggregate_WinRateCalculation`             | Win rate = wins / (wins + losses) * 100             |
| `TestFormatTradeOpened_MarkdownV2`             | Output is valid MarkdownV2 (no unescaped chars)     |
| `TestFormatTradeClosed_AllResults`             | Each result type renders correctly                  |
| `TestFormatDailySummary_ZeroTrades`            | "Quiet day" formatting                              |
| `TestFormatRiskEvent_AllTypes`                 | Kill switch, loss limit, cooldown messages          |
| `TestTelegramClient_SendMessage`              | HTTP request constructed correctly                  |
| `TestNotifier_DisabledNoOp`                    | Disabled notifier doesn't panic or send             |
| `TestBackfillIfNeeded_MissedDay`              | Generates summary when yesterday is missing         |
| `TestBackfillIfNeeded_AlreadyExists`          | No duplicate when summary exists                    |

### Integration Tests

| Test                                           | What it validates                                   |
|------------------------------------------------|-----------------------------------------------------|
| `TestDailySummary_FullFlow`                    | Trades inserted → summary generated → correct stats |
| `TestSummaryRepo_Upsert_Idempotent`           | Multiple upserts produce single row                 |
| `TestTradeRepo_FindByOpenTimeRange`            | Correct date boundary filtering                     |

### Manual Validation

- Set up test Telegram bot, verify all 4 message types render correctly
- Manually trigger daily summary, verify Telegram delivery
- Simulate kill switch, verify risk alert notification
- Run paper trading, verify trade open/close notifications arrive
- Test with `telegram.enabled: false`, verify no HTTP calls made

---

## 20. Implementation Checklist

| #  | Task                                                          | Dependencies |
|----|---------------------------------------------------------------|--------------|
|    | **App Refactor**                                              |              |
| 1  | Extract `pollers.go` (oiPoller, klineSubscriber, etc.)        | None         |
| 2  | Extract `websocket.go` (updateKlineSubscriptions, WS setup)   | None         |
| 3  | Extract `helpers.go` (confidenceToSize, checkHistoricalPrice) | None         |
| 4  | Convert scanFn closure → `scan.go` as method on `*App`       | None         |
| 5  | Slim `app.go` to struct + Run() wiring only                   | #1-4         |
|    | **Daily Trade Record**                                        |              |
| 6  | Create `domain/summary.go` (DailySummary type)                | None         |
| 7  | Create migration `007_create_daily_summaries`                 | None         |
| 8  | Create `storage/summary_repo.go` (Upsert, Exists, GetByDate) | #7           |
| 9  | Add `FindByOpenTimeRange` to trade repository                 | None         |
| 10 | Create `summary/service.go` (aggregation logic)               | #6,#8,#9     |
| 11 | Create `summary/scheduler.go` (00:00 UTC daily job)           | #10          |
| 12 | Add startup backfill logic                                    | #10          |
|    | **Telegram Integration**                                      |              |
| 13 | Create `notify/telegram.go` (Bot API client)                  | None         |
| 14 | Create `notify/formatter.go` (MarkdownV2 message builders)    | None         |
| 15 | Create `notify/notifier.go` (event dispatch)                  | #13,#14      |
| 16 | Add Telegram + Summary config fields                          | None         |
| 17 | Wire notifier into ExecutionEngine (trade opened)             | #15          |
| 18 | Wire notifier into PositionManager (trade closed)             | #15          |
| 19 | Wire notifier into RiskEngine (risk events)                   | #15          |
| 20 | Wire daily summary → notifier callback                        | #11,#15      |
|    | **Testing & Validation**                                      |              |
| 21 | Write unit tests (daily summary aggregation)                  | #10          |
| 22 | Write unit tests (Telegram formatter)                         | #14          |
| 23 | Write unit tests (notifier disabled mode)                     | #15          |
| 24 | Write integration tests (summary repo + trade query)          | #8,#9        |
| 25 | End-to-end: paper trading with Telegram enabled               | All          |

---

## 21. Implementation Order (Recommended)

1. **App refactor first** (tasks 1-5) — reduces merge conflicts for subsequent work
2. **Daily summary** (tasks 6-12) — independent of Telegram; can validate via logs/DB
3. **Telegram** (tasks 13-20) — builds on summary; enables notification for all events
4. **Testing** (tasks 21-25) — validates everything together

---

## 22. Summary

Phase 6 adds three capabilities:

| Feature              | Impact                                                              |
|----------------------|---------------------------------------------------------------------|
| **Daily Record**     | Track performance trends over time; enables weekly/monthly analysis  |
| **Telegram**         | Real-time awareness of bot activity without watching terminal logs   |
| **App Refactor**     | `app.go` drops from 716 → ~250 lines; each concern in its own file |

These are operational improvements — they don't change trading logic, risk management, or execution strategy. The bot trades exactly the same; you just get better visibility into what it's doing.
