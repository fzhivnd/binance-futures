# Futures Bot

A funding-rate shorting bot for Binance Futures. Automatically identifies overextended long positions near funding settlement, shorts them, and manages exits with split TP + trailing stop.

---

## How it works

The bot watches every 4h funding settlement window (`00:00 04:00 08:00 12:00 16:00 20:00 UTC`). Starting 30 minutes before settlement, it scans for symbols with deeply negative funding rates, scores them against technical indicators, optionally asks an LLM to pick the best candidate, and places a market short entry. Positions are managed automatically — SL, TP1 at 50%, trailing stop on the remainder, breakeven move, and LLM-assisted force-close when losing.

Full system design: [`docs/summary.md`](docs/summary.md)

---

## Requirements

- Go 1.25+
- Docker + Docker Compose
- Binance Futures account with API key (Futures trading enabled)
- OpenAI API key (optional — LLM can be disabled)
- Telegram bot token (optional — notifications can be disabled)

---

## Quick start

**1. Clone and copy config**

```sh
cp config/config.example.yaml config/config.yaml
cp .env.example .env
```

**2. Fill in `.env`**

```env
BINANCE_API_KEY=your_key
BINANCE_API_SECRET=your_secret
OPENAI_API_KEY=sk-...
POSTGRES_USER=futures
POSTGRES_PASSWORD=yourpassword
POSTGRES_DB=futures_bot
REDIS_PASSWORD=yourredispassword
TELEGRAM_BOT_TOKEN=...
TELEGRAM_CHAT_ID=...
```

**3. Start infrastructure**

```sh
make docker-up        # starts postgres + redis only
```

**4. Run migrations**

```sh
make migrate-up
```

**5. Start the bot**

```sh
make run              # builds and runs locally
# or
docker compose up -d  # runs bot + dashboard in Docker
```

**6. Open dashboard**

```
http://localhost:8080
```

---

## Modes

Set `app.mode` in `config/config.yaml`:

| Mode | Behaviour |
|---|---|
| `paper` | Simulates fills and P&L using mark price. No real orders placed. |
| `live` | Places real orders on Binance Futures. |

Start with `paper` mode to verify the bot is scanning and scoring correctly before going live.

---

## Configuration

All settings live in `config/config.yaml`. Secrets are injected via environment variables using `${VAR}` syntax.

Key settings:

| Section | Key setting | Default |
|---|---|---|
| `trading` | `leverage` | 20x |
| `trading` | `position_size_pct` | 5% of balance per trade |
| `trading` | `max_positions` | 2 |
| `funding` | `min_rate` / `max_rate` | −0.2% to −2% |
| `execution` | `sl_pct` | 5% |
| `execution` | `tp_pct` | 2% |
| `execution` | `tp1_size_pct` | 50% (split TP) |
| `execution` | `trailing_callback_rate` | 0.5% |
| `risk` | `max_daily_losses` | 2 |
| `llm` | `enabled` | true |
| `llm` | `model` | gpt-4.1-mini |

---

## Make targets

```sh
make build            # build bot binary
make run              # build and run bot locally
make build-dashboard  # build dashboard binary
make run-dashboard    # build and run dashboard locally
make test             # run all tests
make migrate-up       # apply all DB migrations
make migrate-down     # roll back all migrations
make docker-up        # start postgres + redis
make docker-up-all    # start postgres + redis + bot + dashboard
make docker-down      # stop all containers
make lint             # run golangci-lint
```

---

## Project structure

```
cmd/
  bot/          — main entry point for the trading bot
  dashboard/    — main entry point for the web dashboard
    web/        — embedded static HTML dashboard
internal/
  app/          — top-level wiring, scan loop, kline subscriptions
  exchange/     — Binance REST client and WebSocket connections
  market/       — in-memory caches (funding, ticker, candles, OI)
  scanner/      — funding rate scan + ROI/volume filters
  indicator/    — RSI, ATR, candle patterns, OI delta, BTC context
  scoring/      — composite scoring (0–100)
  llm/          — OpenAI decision engine, force-SL engine
  execution/    — order placement, position manager, SL/TP logic
  risk/         — pre-trade guards (kill switch, drawdown, daily loss)
  memory/       — pgvector trade embedding and similarity retrieval
  scheduler/    — funding window timing
  notify/       — Telegram notifications
  storage/      — PostgreSQL and Redis repositories
  summary/      — daily summary aggregation
  dashboard/    — dashboard HTTP handlers and DB queries
config/         — config.yaml (gitignored secrets)
migrations/     — SQL migration files
docs/           — design documents
```

---

## Dashboard

A read-only web UI for reviewing trade history and daily summaries.

- **Trade history** — symbol, side, entry/exit, PnL, result, close reason, LLM confidence
- **Daily summary** — trade count, win rate, total PnL per day
- **Stats bar** — all-time win rate, total PnL, best day
- **Paper / Live toggle** — view either mode independently

Runs as a separate binary (`cmd/dashboard`) connecting only to PostgreSQL.

---

## Risk controls

| Control | Default |
|---|---|
| Max concurrent positions | 2 |
| Daily loss limit | 2 losses → trading paused until 00:00 UTC |
| Post-loss cooldown | 15 min |
| Kill switch | Redis flag; blocks all new entries; activate via Redis or Telegram |
| Max drawdown | 10% of account |
| BTC bullish breakout | Skips all alt shorts |
| ATR ratio guard | Rejects symbols with ATR > 6% of price |
| 1h funding interval | Always skipped (too frequent, no edge) |
| 8h funding alignment | Skipped when not settling in current 4h window |

---

## Docs

| File | Content |
|---|---|
| [`docs/summary.md`](docs/summary.md) | Full system design and code behaviour |
| [`docs/dashboard_design.md`](docs/dashboard_design.md) | Dashboard architecture |
| [`docs/roi_backfill.md`](docs/roi_backfill.md) | Candle backfill design and per-indicator requirements |
