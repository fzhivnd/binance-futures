# Dashboard Design

## Approach

Lightweight HTTP server serving a single HTML page.

A new `cmd/dashboard/main.go` starts an HTTP server that reads directly from the same PostgreSQL DB the bot uses (read-only queries). No new dependencies — just Go's `net/http` + a single self-contained HTML file with inline JS.

---

## Pages / Views

### 1. Trade History (`/`)

| Column | Source |
|---|---|
| Time | `created_at` |
| Symbol | `symbol` |
| Side | `side` |
| Mode | `entry_mode` |
| Leverage | `leverage` |
| Entry / Exit | `entry_price` / `avg_close_price` |
| PnL | `pnl` |
| Result | `result` (WIN/LOSS/etc, color-coded) |
| Close Reason | `close_reason` (TP_TRAIL/FORCE_SL/HARD_SL/MANUAL) |
| Paper? | `is_paper` badge |
| LLM Conf | `llm_confidence` |

Filters: date range, paper/live toggle, result filter.

### 2. Daily Summary (`/summary`)

| Column | Source |
|---|---|
| Date | `trade_date` |
| Trades | `trade_count` |
| Wins / Losses | `win_count` / `loss_count` |
| Win Rate | `win_rate` % |
| Total PnL | `total_pnl` |
| Paper/Live | `is_paper` |

---

## API Endpoints (JSON)

```
GET /api/trades?limit=100&paper=true&from=2026-05-01&to=2026-05-28
GET /api/summary?paper=true
GET /api/stats          ← totals: all-time win rate, total PnL, best day
```

---

## Structure

```
cmd/dashboard/
  main.go              ← HTTP server, DB connect, route wiring
internal/dashboard/
  handler.go           ← HTTP handlers
  queries.go           ← read-only DB queries (separate from storage/)
web/
  index.html           ← single file, vanilla JS + fetch, no build step
```

---

## Key Design Decisions

| Choice | Reason |
|---|---|
| Separate `cmd/dashboard` binary | Bot stays isolated; dashboard is read-only and can run independently |
| No new Go dependencies | Uses `net/http` + existing pgx pool patterns |
| Single HTML file | No npm, no build pipeline — just open browser or `go:embed` it |
| Read-only queries | Never touches bot's write path; safe to run while bot is live |
| Paper/Live toggle | DB separates them with `is_paper` flag |

---

## Deployment

### Dockerfile

The existing `Dockerfile` builds only the `bot` binary. It needs to be extended to also build `dashboard` as a second binary in the same image.

```dockerfile
# In builder stage, add:
RUN go build -o bin/bot ./cmd/bot
RUN go build -o bin/dashboard ./cmd/dashboard

# In runtime stage, add:
COPY --from=builder /app/bin/dashboard .
COPY web/ web/
```

### docker-compose.yml

Add a `dashboard` service alongside `bot`. It shares the same `network_mode: host` so it can reach `localhost:5432` (postgres) directly, consistent with how the bot connects.

```yaml
  dashboard:
    build:
      context: .
      dockerfile: Dockerfile
    network_mode: host
    command: ./dashboard
    environment:
      - POSTGRES_HOST=localhost
      - POSTGRES_USER=${POSTGRES_USER}
      - POSTGRES_PASSWORD=${POSTGRES_PASSWORD}
      - POSTGRES_DB=${POSTGRES_DB}
      - DASHBOARD_PORT=8080        # optional override, default 8080
    depends_on:
      postgres:
        condition: service_healthy
    restart: unless-stopped
```

Access the dashboard at `http://localhost:8080` while the stack is running.

### Makefile additions

```makefile
build-dashboard:
    go build -o bin/dashboard ./cmd/dashboard

run-dashboard: build-dashboard
    ./bin/dashboard

docker-up-all:
    docker compose up -d
```

### Environment variables (dashboard only)

| Variable | Default | Purpose |
|---|---|---|
| `POSTGRES_HOST` | `localhost` | DB host |
| `POSTGRES_USER` | — | DB user (same as bot) |
| `POSTGRES_PASSWORD` | — | DB password (same as bot) |
| `POSTGRES_DB` | — | DB name (same as bot) |
| `DASHBOARD_PORT` | `8080` | HTTP listen port |

No Redis, no Binance API key, no Telegram token needed — dashboard is read-only PostgreSQL only.

---

## Out of Scope (v1)

- No WebSocket live updates
- No auth (local-only deployment)
- No charting library
