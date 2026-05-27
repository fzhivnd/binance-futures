# Phase 4: Trade Memory Engine (pgvector)

---

## 1. Overview

Phase 4 adds a **Trade Memory Engine** that embeds completed trade setups (including skipped trades) into a vector space using pgvector. Before each LLM decision, the system retrieves the most similar historical setups and injects summarized lessons into the prompt — enabling the LLM to learn from past outcomes and calibrate confidence accordingly.

### Why This Matters

- The bot encounters recurring market patterns (specific funding/OI/BTC combinations)
- Without memory, the LLM makes the same mistake twice in identical conditions
- With memory, it can say: *"Last 3 times I saw this setup with BTC momentum > 75, I lost. Skip."*
- Skipped trades are equally important — they validate the decision to not trade

### What Changes

| Before (Phase 3)                                  | After (Phase 4)                                         |
|---------------------------------------------------|---------------------------------------------------------|
| LLM decides on current data only                  | LLM receives 3-5 similar past setups as context         |
| No learning from outcomes                         | Past wins/losses calibrate confidence                   |
| Skip decisions are not recorded                   | Skip decisions are stored + embedded for future recall   |
| `similar_past_trades` field is always empty       | Field populated from pgvector similarity search          |

### What Stays the Same

- Full Phase 1-3 pipeline unchanged (scan → filter → score → LLM → intent → execute)
- Embedding happens **after** trade closes (or after skip decision) — zero latency impact on trading
- Retrieval happens **before** LLM call — adds ~5-20ms (pgvector query)
- Risk engine, execution engine, position manager: unchanged

---

## 2. Tech Stack Additions

| Component            | Choice                                      | Reason                                                          |
|----------------------|---------------------------------------------|-----------------------------------------------------------------|
| Vector Extension     | pgvector (PostgreSQL extension)             | Already in stack, no new infra; cosine similarity built-in      |
| Embedding Model      | OpenAI `text-embedding-3-small`             | 1536 dims, $0.02/1M tokens, fast (~100ms), great quality       |
| Embedding Dimension  | 1536 (full)                                 | Maximum accuracy; storage cost negligible at our scale          |
| Vector Index         | IVFFlat (initially) → HNSW (at scale)       | IVFFlat: simple, fast build; HNSW: better recall at >10k rows  |
| Summarizer           | Same LLM (gpt-4.1-mini)                    | Generate lesson summaries from trade outcome + conditions       |

### Embedding Dimension: 1536 (Full)

Using full 1536 dimensions for maximum accuracy:
- Better at distinguishing subtle differences between similar setups (RSI 71 + OI +18% vs RSI 68 + OI +12%)
- Storage is negligible (~6KB/row × thousands of rows = a few MB total)
- pgvector handles 1536-dim vectors efficiently with HNSW/IVFFlat indexes
- No `dimensions` parameter needed — use the model's native output directly

### Cost Estimate

| Operation              | Frequency            | Tokens/call | Cost/call    | Monthly          |
|------------------------|----------------------|-------------|--------------|------------------|
| Embed trade on close   | ~6-18 trades/day     | ~200        | $0.000004    | ~$0.002          |
| Embed skip decision    | ~30-90 skips/day     | ~200        | $0.000004    | ~$0.01           |
| Retrieve (no API cost) | pgvector query only  | 0           | $0           | $0               |
| Summarize lesson       | same as embed count  | ~500 in+out | $0.001       | ~$1-3            |
| **Total Phase 4 cost** |                      |             |              | **~$1-4/month**  |

---

## 3. Project Structure (New/Modified Files)

```
internal/
├── memory/
│   ├── engine.go              # Main memory engine: orchestrates embed + store + retrieve
│   ├── embedder.go            # Calls OpenAI embedding API, converts features → text → vector
│   ├── retriever.go           # Queries pgvector for similar trades, returns ranked results
│   ├── summarizer.go          # Generates lesson summaries from trade outcomes
│   ├── feature_builder.go     # Builds embedding text from trade/indicator/BTC data
│   ├── types.go               # TradeMemory, MemoryEntry, SimilarTrade types
│   └── engine_test.go
├── llm/
│   ├── schema.go              # + LLMSimilarTrade (activate placeholder)
│   ├── prompt.go              # + "SIMILAR PAST TRADES" section in user message
│   ├── mapper.go              # + map similar trades into LLM request
│   └── decision_engine.go     # + accept similarTrades parameter
├── domain/
│   └── memory.go              # TradeMemory domain type
├── storage/
│   └── memory_repo.go         # pgvector CRUD: insert embedding, similarity search
├── config/
│   └── config.go              # + MemoryConfig struct
├── app/
│   └── app.go                 # Modified: wire memory engine, call on trade close + skip

config/
└── config.yaml                # + memory section

migrations/
├── 005_create_trade_memories.up.sql
└── 005_create_trade_memories.down.sql
```

---

## 4. System Design

### Architecture Diagram

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                          TRADE MEMORY ENGINE                                  │
├──────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  ┌─────────────────────────────────────────────────────┐                    │
│  │             WRITE PATH (async, post-trade)           │                    │
│  │                                                     │                    │
│  │  Trade Closed / Skip Decision                       │                    │
│  │         │                                           │                    │
│  │         ▼                                           │                    │
│  │  ┌─────────────────┐                               │                    │
│  │  │ Feature Builder  │ Build structured text from:   │                    │
│  │  │                  │ - indicators at entry time    │                    │
│  │  │                  │ - BTC context                 │                    │
│  │  │                  │ - trade outcome               │                    │
│  │  └────────┬─────────┘                               │                    │
│  │           │                                         │                    │
│  │           ▼                                         │                    │
│  │  ┌─────────────────┐                               │                    │
│  │  │    Embedder      │ OpenAI text-embedding-3-small │                    │
│  │  │  (API call ~100ms)│ → 512-dim vector            │                    │
│  │  └────────┬─────────┘                               │                    │
│  │           │                                         │                    │
│  │           ▼                                         │                    │
│  │  ┌─────────────────┐                               │                    │
│  │  │  Summarizer      │ gpt-4.1-mini generates       │                    │
│  │  │  (API call ~1s)  │ 1-2 sentence lesson          │                    │
│  │  └────────┬─────────┘                               │                    │
│  │           │                                         │                    │
│  │           ▼                                         │                    │
│  │  ┌─────────────────┐                               │                    │
│  │  │  PostgreSQL +    │ Store vector + metadata       │                    │
│  │  │  pgvector        │                               │                    │
│  │  └─────────────────┘                               │                    │
│  └─────────────────────────────────────────────────────┘                    │
│                                                                              │
│  ┌─────────────────────────────────────────────────────┐                    │
│  │             READ PATH (sync, pre-LLM)               │                    │
│  │                                                     │                    │
│  │  Before LLM Call (during scan cycle)                │                    │
│  │         │                                           │                    │
│  │         ▼                                           │                    │
│  │  ┌─────────────────┐                               │                    │
│  │  │ Feature Builder  │ Build text from current       │                    │
│  │  │                  │ candidate setup               │                    │
│  │  └────────┬─────────┘                               │                    │
│  │           │                                         │                    │
│  │           ▼                                         │                    │
│  │  ┌─────────────────┐                               │                    │
│  │  │    Embedder      │ Embed current setup → vector  │                    │
│  │  └────────┬─────────┘                               │                    │
│  │           │                                         │                    │
│  │           ▼                                         │                    │
│  │  ┌─────────────────┐                               │                    │
│  │  │   Retriever      │ pgvector cosine similarity   │                    │
│  │  │  (query ~5-20ms) │ → top 5 similar memories     │                    │
│  │  └────────┬─────────┘                               │                    │
│  │           │                                         │                    │
│  │           ▼                                         │                    │
│  │  ┌─────────────────┐                               │                    │
│  │  │  LLM Request     │ Inject similar_past_trades   │                    │
│  │  │  (prompt.go)     │ section into user message     │                    │
│  │  └─────────────────┘                               │                    │
│  └─────────────────────────────────────────────────────┘                    │
│                                                                              │
└──────────────────────────────────────────────────────────────────────────────┘
```

### Integration with Existing Pipeline

```
SCAN → SCORE → [RETRIEVE SIMILAR TRADES] → LLM DECISION → INTENT QUEUE → EXECUTE
                      ↑ Phase 4                     │
                      │                             │
                      └────── TRADE CLOSED ─────────┘
                              OR SKIP DECISION
                                    │
                                    ▼
                           [EMBED + SUMMARIZE + STORE]
                                 ↑ Phase 4 (async)
```

---

## 5. Embedding Strategy (Critical Design Decision)

### 5.1 What to Embed

The embedding must capture the **market conditions at decision time** — not just the trade parameters. This lets us find historically similar *setups*, regardless of which coin it was.

#### Feature Vector (Text Representation for Embedding)

```
funding_rate: -0.85% | daily_roi: 34.5% | oi_delta_1h: +18.3% | oi_delta_15m: +6.2%
rsi_14_15m: 71.2 | rsi_7_5m: 74.8 | atr_ratio: 2.8
vol_change_5m: +3.1% | volume_spike: true | momentum_loss: true
candle_patterns: 1h:SHOOTING_STAR(STRONG) 30m:BEARISH_ENGULFING(STRONG) 15m:DOJI_AFTER_PUMP(MEDIUM)
btc_trend: neutral | btc_momentum: 55 | btc_rsi: 52.3 | btc_breakout: false
composite_score: 78 | entry_mode: LAST_MINUTE | minutes_to_settlement: 4
```

#### Why Text Embedding Over Raw Vector

| Approach               | Pros                                    | Cons                                     |
|------------------------|-----------------------------------------|------------------------------------------|
| Raw numerical vector   | No API cost, fast, deterministic        | Poor at capturing pattern relationships  |
| Text embedding (chosen)| Captures semantic relationships between features | API cost (~$0.01/mo), 100ms latency |

Text embedding is superior because:
1. **Semantic understanding**: "rsi_14_15m: 72 + momentum_loss: true" is semantically close to "rsi_14_15m: 69 + momentum_loss: true" — the model understands RSI + momentum exhaustion as a combined pattern
2. **Candle patterns**: "SHOOTING_STAR + BEARISH_ENGULFING" is semantically similar to "EVENING_STAR + UPPER_WICK_REJECTION" — both signal reversal
3. **BTC context**: "btc_trend: bullish + btc_breakout: true" is semantically far from "btc_trend: neutral" — the model understands danger level
4. **Flexible**: Adding new features later doesn't require re-indexing old vectors

### 5.2 What NOT to Embed

| Excluded              | Reason                                                    |
|-----------------------|-----------------------------------------------------------|
| Symbol name           | We want cross-symbol pattern matching                     |
| Exact timestamp       | We want timeless pattern matching                         |
| Entry/exit prices     | Price levels are meaningless across coins                 |
| PnL amount            | Stored in metadata, not in embedding                      |
| Account balance       | Irrelevant to setup quality                               |

### 5.3 Embedding for Skip Decisions

Skip decisions are embedded with the same features but with `action: SKIP` and `skip_reason` appended. This is critical because:
- A skip in conditions that later would have been profitable → lesson: "don't skip this pattern"
- A skip in conditions that would have lost → validation: "good skip"

For skip validation, we retroactively check what would have happened (using the price movement after the skip) and store the hypothetical outcome.

### 5.4 Hybrid Approach: Structured Metadata + Vector Search

The search uses pgvector for approximate pattern matching, then **metadata filters** narrow results:

```sql
-- Find similar setups, but only from the same general market regime
SELECT * FROM trade_memories
WHERE btc_regime = $1              -- same BTC regime (bullish/neutral/bearish)
  AND ABS(funding_bucket - $2) <= 1  -- similar funding severity
ORDER BY embedding <=> $3          -- cosine distance to current setup
LIMIT 5;
```

This prevents comparing a "BTC neutral + funding -0.5%" setup with a "BTC breakout + funding -1.5%" setup — even if other indicators look similar.

---

## 6. Data Flow & Contracts

### 6.1 Domain Types (`internal/domain/memory.go`)

```go
package domain

import (
    "time"

    "github.com/google/uuid"
)

type TradeAction string

const (
    ActionTrade TradeAction = "TRADE"
    ActionSkip  TradeAction = "SKIP"
)

type TradeMemory struct {
    ID        uuid.UUID
    TradeID   *uuid.UUID // nil for skip decisions
    Symbol    string
    Action    TradeAction
    CreatedAt time.Time

    // Setup conditions at decision time
    FundingRate     float64
    DailyROI        float64
    OIDelta1h       float64
    OIDelta15m      float64
    RSI14_15m       float64
    RSI7_5m         float64
    ATRRatio        float64
    VolChange5m     float64
    VolumeSpike     bool
    MomentumLoss    bool
    CandlePatterns  string // serialized: "1h:SHOOTING_STAR(STRONG) 30m:..."
    CompositeScore  float64
    EntryMode       string
    MinutesToSettle int

    // BTC context
    BTCTrend      string
    BTCMomentum   int
    BTCBreakout   bool
    BTCRSI        float64
    BTCChange1h   float64

    // Outcome (filled after trade closes, or retroactively for skips)
    Outcome     string  // "WIN" | "PARTIAL_WIN" | "LOSS" | "FORCE_SL" | "BREAKEVEN" | "SKIP_VALIDATED" | "SKIP_MISSED"
    ProfitPct   float64 // actual or hypothetical for skips
    HoldMinutes int     // duration from entry to exit

    // LLM-generated lesson
    Lesson string // "Extreme funding + OI exhaustion preceded dump; BTC neutral allowed reversal"

    // Metadata for filtered search
    BTCRegime     string // "bullish" | "neutral" | "bearish"
    FundingBucket int    // 1: -0.2~-0.5, 2: -0.5~-1.0, 3: -1.0~-2.0
}

// SimilarTrade is the compact form injected into LLM prompts
type SimilarTrade struct {
    Outcome    string
    ProfitPct  float64
    Similarity float64
    Lesson     string
    DaysAgo    int
}
```

### 6.2 LLM Schema Extension (`internal/llm/schema.go`)

```go
// Activate the Phase 4 placeholder — add to LLMRequest
type LLMRequest struct {
    Timestamp          int64             `json:"timestamp"`
    MinutesToSettlement int              `json:"minutes_to_settlement"`
    BTCContext         LLMBTCContext     `json:"btc_context"`
    Candidates         []LLMCandidate    `json:"candidates"`
    SimilarTrades      []LLMSimilarTrade `json:"similar_past_trades,omitempty"` // Phase 4
}

type LLMSimilarTrade struct {
    Outcome    string  `json:"outcome"`      // "WIN" | "PARTIAL_WIN" | "LOSS" | "FORCE_SL" | "BREAKEVEN" | "SKIP_VALIDATED" | "SKIP_MISSED"
    ProfitPct  float64 `json:"profit_pct"`
    Similarity float64 `json:"similarity"`   // 0-1 cosine similarity
    Lesson     string  `json:"lesson"`       // 1-2 sentence summary
    DaysAgo    int     `json:"days_ago"`
}
```

### 6.3 Memory Repository Interface (`internal/storage/memory_repo.go`)

```go
package storage

import (
    "context"

    "github.com/google/uuid"
    pgvector "github.com/pgvector/pgvector-go"

    "futures/internal/domain"
)

type MemoryRepository interface {
    Insert(ctx context.Context, memory *domain.TradeMemory, embedding pgvector.Vector) error
    FindSimilar(ctx context.Context, embedding pgvector.Vector, btcRegime string, fundingBucket int, limit int) ([]MemorySearchResult, error)
    UpdateOutcome(ctx context.Context, id uuid.UUID, outcome string, profitPct float64, holdMinutes int) error
    GetByTradeID(ctx context.Context, tradeID uuid.UUID) (*domain.TradeMemory, error)
    GetPendingSkipValidations(ctx context.Context, olderThan time.Duration) ([]domain.TradeMemory, error)
}

type MemorySearchResult struct {
    Memory     domain.TradeMemory
    Similarity float64 // 1 - cosine_distance (1.0 = identical)
}
```

---

## 7. Database Schema

### Migration: `005_create_trade_memories.up.sql`

```sql
-- Enable pgvector extension (idempotent)
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS trade_memories (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    trade_id            UUID REFERENCES trades(id) ON DELETE SET NULL,
    symbol              VARCHAR(32) NOT NULL,
    action              VARCHAR(8) NOT NULL,  -- 'TRADE' | 'SKIP'
    created_at          TIMESTAMP NOT NULL DEFAULT NOW(),

    -- Setup conditions (embedded into vector)
    funding_rate        NUMERIC(10,6) NOT NULL,
    daily_roi           NUMERIC(10,4),
    oi_delta_1h         NUMERIC(8,4),
    oi_delta_15m        NUMERIC(8,4),
    rsi_14_15m          NUMERIC(6,2),
    rsi_7_5m            NUMERIC(6,2),
    atr_ratio           NUMERIC(6,4),
    vol_change_5m       NUMERIC(8,4),
    volume_spike        BOOLEAN NOT NULL DEFAULT false,
    momentum_loss       BOOLEAN NOT NULL DEFAULT false,
    candle_patterns     TEXT,
    composite_score     NUMERIC(6,2),
    entry_mode          VARCHAR(16),
    minutes_to_settle   INT,

    -- BTC context
    btc_trend           VARCHAR(16),
    btc_momentum        INT,
    btc_breakout        BOOLEAN NOT NULL DEFAULT false,
    btc_rsi             NUMERIC(6,2),
    btc_change_1h       NUMERIC(8,4),

    -- Outcome (updated after trade closes / skip validation)
    outcome             VARCHAR(16),  -- 'WIN' | 'PARTIAL_WIN' | 'LOSS' | 'FORCE_SL' | 'BREAKEVEN' | 'SKIP_VALIDATED' | 'SKIP_MISSED'
    profit_pct          NUMERIC(8,4),
    hold_minutes        INT,

    -- LLM-generated lesson summary
    lesson              TEXT,

    -- Metadata for filtered vector search
    btc_regime          VARCHAR(16) NOT NULL,  -- 'bullish' | 'neutral' | 'bearish'
    funding_bucket      INT NOT NULL,          -- 1: mild, 2: moderate, 3: extreme

    -- Vector embedding (1536 dimensions — full text-embedding-3-small output)
    embedding           vector(1536) NOT NULL
);

-- Indexes for filtered similarity search
CREATE INDEX idx_trade_memories_embedding
    ON trade_memories USING ivfflat (embedding vector_cosine_ops)
    WITH (lists = 50);

CREATE INDEX idx_trade_memories_regime_bucket
    ON trade_memories (btc_regime, funding_bucket);

CREATE INDEX idx_trade_memories_trade_id
    ON trade_memories (trade_id)
    WHERE trade_id IS NOT NULL;

CREATE INDEX idx_trade_memories_outcome_null
    ON trade_memories (created_at)
    WHERE outcome IS NULL;

CREATE INDEX idx_trade_memories_created_at
    ON trade_memories (created_at DESC);
```

### Migration: `005_create_trade_memories.down.sql`

```sql
DROP TABLE IF EXISTS trade_memories;
```

### IVFFlat → HNSW Migration (when >5000 rows)

```sql
-- Run manually when trade_memories exceeds ~5000 rows for better recall
DROP INDEX idx_trade_memories_embedding;
CREATE INDEX idx_trade_memories_embedding
    ON trade_memories USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);
```

---

## 8. Configuration

### Config Struct Addition

```go
// internal/config/config.go

type MemoryConfig struct {
    Enabled              bool   `yaml:"enabled"`
    EmbeddingModel       string `yaml:"embedding_model"`
    EmbeddingDimensions  int    `yaml:"embedding_dimensions"`  // 1536 for text-embedding-3-small
    TopSimilar           int    `yaml:"top_similar"`           // how many similar trades to retrieve
    MinSimilarity        float64 `yaml:"min_similarity"`       // minimum cosine similarity (0-1)
    EmbedSkips           bool   `yaml:"embed_skips"`           // also store skip decisions
    SkipValidationDelay  string `yaml:"skip_validation_delay"` // how long to wait before validating skips
    MaxMemoryAge         string `yaml:"max_memory_age"`        // ignore memories older than this
    SummarizerModel      string `yaml:"summarizer_model"`      // model for lesson generation
}
```

### config.yaml Addition

```yaml
memory:
  enabled: true
  embedding_model: "text-embedding-3-small"
  embedding_dimensions: 1536
  top_similar: 5
  min_similarity: 0.75
  embed_skips: true
  skip_validation_delay: "2h"    # wait 2h to check what happened after a skip
  max_memory_age: "90d"          # ignore memories older than 90 days
  summarizer_model: "gpt-4.1-mini"
```

### Defaults

```go
func setDefaults(cfg *Config) {
    // ... existing defaults ...

    // Phase 4: Memory defaults
    if cfg.Memory.EmbeddingModel == "" {
        cfg.Memory.EmbeddingModel = "text-embedding-3-small"
    }
    if cfg.Memory.EmbeddingDimensions == 0 {
        cfg.Memory.EmbeddingDimensions = 1536
    }
    if cfg.Memory.TopSimilar == 0 {
        cfg.Memory.TopSimilar = 5
    }
    if cfg.Memory.MinSimilarity == 0 {
        cfg.Memory.MinSimilarity = 0.75
    }
    if cfg.Memory.SkipValidationDelay == "" {
        cfg.Memory.SkipValidationDelay = "2h"
    }
    if cfg.Memory.MaxMemoryAge == "" {
        cfg.Memory.MaxMemoryAge = "90d"
    }
    if cfg.Memory.SummarizerModel == "" {
        cfg.Memory.SummarizerModel = "gpt-4.1-mini"
    }
}
```

---

## 9. Detailed Implementation Logic

### 9.1 Feature Builder (`internal/memory/feature_builder.go`)

Converts trade setup conditions into a structured text string suitable for embedding.

```go
package memory

import (
    "fmt"
    "strings"

    "futures/internal/domain"
)

// BuildFeatureText creates a normalized text representation of a trade setup
// for embedding. The text is structured so semantically similar setups
// produce similar embeddings.
func BuildFeatureText(
    snap *domain.IndicatorSnapshot,
    btc *domain.BTCContext,
    candidate *domain.Candidate,
    score float64,
    entryMode string,
    minutesToSettle int,
) string {
    var sb strings.Builder

    // Core trade parameters
    sb.WriteString(fmt.Sprintf("funding_rate: %.2f%% | daily_roi: %.1f%% | composite_score: %.0f\n",
        candidate.FundingRate*100, candidate.DailyROI, score))

    // Momentum indicators
    sb.WriteString(fmt.Sprintf("rsi_14_15m: %.1f | rsi_7_5m: %.1f | momentum_loss: %v\n",
        snap.RSI14_15m, snap.RSI7_5m, snap.MomentumLoss))

    // Open Interest
    sb.WriteString(fmt.Sprintf("oi_delta_1h: %+.1f%% | oi_delta_15m: %+.1f%%\n",
        snap.OIDelta1h, snap.OIDelta15m))

    // Volatility
    sb.WriteString(fmt.Sprintf("atr_ratio: %.1f | vol_change_5m: %+.1f%% | volume_spike: %v\n",
        snap.ATRRatio, snap.VolChange5m, snap.VolumeSpike))

    // Candle patterns
    if len(snap.Patterns) > 0 {
        sb.WriteString("candle_patterns: ")
        for _, p := range snap.Patterns {
            sb.WriteString(fmt.Sprintf("%s:%s(%s) ", p.Timeframe, p.Pattern, p.Strength))
        }
        sb.WriteString("\n")
    }

    // BTC context
    if btc != nil {
        sb.WriteString(fmt.Sprintf("btc_trend: %s | btc_momentum: %d | btc_rsi: %.1f | btc_breakout: %v | btc_change_1h: %+.2f%%\n",
            btc.Trend, btc.MomentumScore, btc.RSI14_1h, btc.IsBreakout, btc.PriceChange1h))
    }

    // Entry context
    sb.WriteString(fmt.Sprintf("entry_mode: %s | minutes_to_settlement: %d\n",
        entryMode, minutesToSettle))

    return sb.String()
}

// FundingBucket categorizes funding rate severity for metadata filtering
func FundingBucket(fundingRatePct float64) int {
    absFunding := -fundingRatePct // funding is negative for shorts
    switch {
    case absFunding >= 1.0:
        return 3 // extreme
    case absFunding >= 0.5:
        return 2 // moderate
    default:
        return 1 // mild
    }
}

// BTCRegime maps BTC trend to a regime category
func BTCRegime(btcTrend string) string {
    switch btcTrend {
    case "bullish":
        return "bullish"
    case "bearish":
        return "bearish"
    default:
        return "neutral"
    }
}
```

### 9.2 Embedder (`internal/memory/embedder.go`)

```go
package memory

import (
    "context"
    "fmt"
    "time"

    openai "github.com/openai/openai-go"
    "github.com/openai/openai-go/option"
    pgvector "github.com/pgvector/pgvector-go"
)

type EmbedderConfig struct {
    APIKey     string
    Model      string // "text-embedding-3-small"
    Dimensions int    // 1536
    Timeout    time.Duration
}

type Embedder struct {
    client *openai.Client
    cfg    EmbedderConfig
}

func NewEmbedder(cfg EmbedderConfig) *Embedder {
    client := openai.NewClient(
        option.WithAPIKey(cfg.APIKey),
        option.WithRequestTimeout(cfg.Timeout),
    )
    return &Embedder{client: client, cfg: cfg}
}

// Embed converts feature text into a vector using OpenAI embeddings API.
func (e *Embedder) Embed(ctx context.Context, text string) (pgvector.Vector, error) {
    resp, err := e.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
        Model:      openai.EmbeddingModel(e.cfg.Model),
        Input:      openai.EmbeddingNewParamsInputUnion{OfString: text},
        // No Dimensions param needed — 1536 is the native output of text-embedding-3-small
    })
    if err != nil {
        return pgvector.Vector{}, fmt.Errorf("embedding API call failed: %w", err)
    }

    if len(resp.Data) == 0 {
        return pgvector.Vector{}, fmt.Errorf("empty embedding response")
    }

    vec := make([]float32, len(resp.Data[0].Embedding))
    for i, v := range resp.Data[0].Embedding {
        vec[i] = float32(v)
    }

    return pgvector.NewVector(vec), nil
}
```

### 9.3 Summarizer (`internal/memory/summarizer.go`)

```go
package memory

import (
    "context"
    "fmt"
    "log/slog"

    "futures/internal/domain"
    "futures/internal/llm"
)

type Summarizer struct {
    client *llm.Client
}

func NewSummarizer(client *llm.Client) *Summarizer {
    return &Summarizer{client: client}
}

const summarizeSystemPrompt = `You generate concise 1-2 sentence lessons from completed trades. 
Focus on: what market condition was key, why the trade won/lost/was correctly skipped.
Be specific about the indicators that mattered most.
Do NOT use generic advice. Reference the actual values.`

// Summarize generates a lesson from a completed trade memory.
func (s *Summarizer) Summarize(ctx context.Context, memory *domain.TradeMemory) (string, error) {
    userMsg := fmt.Sprintf(`Trade setup:
- Symbol: %s | Action: %s | Entry mode: %s
- Funding: %.2f%% | OI delta 1h: +%.1f%% | RSI(14,15m): %.1f
- BTC: %s (momentum %d, breakout=%v)
- Candles: %s
- Composite score: %.0f

Outcome: %s | PnL: %.2f%% | Hold time: %d minutes

Generate a concise lesson (1-2 sentences) about what drove this outcome.`,
        memory.Symbol, memory.Action, memory.EntryMode,
        memory.FundingRate*100, memory.OIDelta1h, memory.RSI14_15m,
        memory.BTCTrend, memory.BTCMomentum, memory.BTCBreakout,
        memory.CandlePatterns, memory.CompositeScore,
        memory.Outcome, memory.ProfitPct, memory.HoldMinutes,
    )

    lesson, err := s.client.Call(ctx, summarizeSystemPrompt, userMsg)
    if err != nil {
        slog.Warn("summarizer failed, using fallback", "error", err)
        return fmt.Sprintf("%s: %.1f%% PnL in %s regime with funding %.2f%%",
            memory.Outcome, memory.ProfitPct, memory.BTCRegime, memory.FundingRate*100), nil
    }

    return lesson, nil
}
```

### 9.4 Retriever (`internal/memory/retriever.go`)

```go
package memory

import (
    "context"
    "time"

    pgvector "github.com/pgvector/pgvector-go"

    "futures/internal/domain"
    "futures/internal/storage"
)

type RetrieverConfig struct {
    TopSimilar    int
    MinSimilarity float64
    MaxAge        time.Duration
}

type Retriever struct {
    repo storage.MemoryRepository
    cfg  RetrieverConfig
}

func NewRetriever(repo storage.MemoryRepository, cfg RetrieverConfig) *Retriever {
    return &Retriever{repo: repo, cfg: cfg}
}

// FindSimilar retrieves the most similar historical trade setups.
// Uses btcRegime and fundingBucket as pre-filters before vector search.
func (r *Retriever) FindSimilar(
    ctx context.Context,
    embedding pgvector.Vector,
    btcRegime string,
    fundingBucket int,
) ([]domain.SimilarTrade, error) {
    results, err := r.repo.FindSimilar(ctx, embedding, btcRegime, fundingBucket, r.cfg.TopSimilar)
    if err != nil {
        return nil, err
    }

    var similar []domain.SimilarTrade
    now := time.Now()

    for _, res := range results {
        if res.Similarity < r.cfg.MinSimilarity {
            continue
        }
        if res.Memory.Outcome == "" {
            continue // outcome not yet determined
        }

        daysAgo := int(now.Sub(res.Memory.CreatedAt).Hours() / 24)

        similar = append(similar, domain.SimilarTrade{
            Outcome:    res.Memory.Outcome,
            ProfitPct:  res.Memory.ProfitPct,
            Similarity: res.Similarity,
            Lesson:     res.Memory.Lesson,
            DaysAgo:    daysAgo,
        })
    }

    return similar, nil
}
```

### 9.5 Memory Engine (`internal/memory/engine.go`)

```go
package memory

import (
    "context"
    "log/slog"
    "math"
    "time"

    "github.com/google/uuid"

    "futures/internal/domain"
    "futures/internal/llm"
    "futures/internal/scheduler"
    "futures/internal/storage"
)

type Engine struct {
    embedder   *Embedder
    retriever  *Retriever
    summarizer *Summarizer
    repo       storage.MemoryRepository
    enabled    bool
}

type EngineConfig struct {
    Enabled             bool
    EmbedderCfg         EmbedderConfig
    RetrieverCfg        RetrieverConfig
    LLMClient           *llm.Client // shared client for summarization
    Repo                storage.MemoryRepository
}

func NewEngine(cfg EngineConfig) *Engine {
    if !cfg.Enabled {
        return &Engine{enabled: false}
    }

    return &Engine{
        embedder:   NewEmbedder(cfg.EmbedderCfg),
        retriever:  NewRetriever(cfg.Repo, cfg.RetrieverCfg),
        summarizer: NewSummarizer(cfg.LLMClient),
        repo:       cfg.Repo,
        enabled:    true,
    }
}

// RecordTrade embeds and stores a completed trade. Called asynchronously after trade closes.
func (e *Engine) RecordTrade(
    ctx context.Context,
    trade *domain.Trade,
    snap *domain.IndicatorSnapshot,
    btc *domain.BTCContext,
    sc *domain.ScoredCandidate,
    decision *domain.LLMDecision,
) error {
    if !e.enabled {
        return nil
    }

    entryMode := ""
    if decision != nil {
        entryMode = string(decision.EntryMode)
    }
    minutesToSettle := 0
    next := scheduler.NextFundingTime(trade.CreatedAt)
    if !next.IsZero() {
        minutesToSettle = int(math.Round(next.Sub(trade.CreatedAt).Minutes()))
    }

    featureText := BuildFeatureText(snap, btc, &sc.Candidate, sc.CompositeScore, entryMode, minutesToSettle)

    embedding, err := e.embedder.Embed(ctx, featureText)
    if err != nil {
        slog.Error("failed to embed trade", "trade_id", trade.ID, "error", err)
        return err
    }

    memory := &domain.TradeMemory{
        ID:              uuid.New(),
        TradeID:         &trade.ID,
        Symbol:          trade.Symbol,
        Action:          domain.ActionTrade,
        CreatedAt:       trade.CreatedAt,
        FundingRate:     trade.FundingRate,
        DailyROI:        trade.DailyROI,
        OIDelta1h:       snap.OIDelta1h,
        OIDelta15m:      snap.OIDelta15m,
        RSI14_15m:       snap.RSI14_15m,
        RSI7_5m:         snap.RSI7_5m,
        ATRRatio:        snap.ATRRatio,
        VolChange5m:     snap.VolChange5m,
        VolumeSpike:     snap.VolumeSpike,
        MomentumLoss:    snap.MomentumLoss,
        CandlePatterns:  formatPatterns(snap.Patterns),
        CompositeScore:  sc.CompositeScore,
        EntryMode:       entryMode,
        MinutesToSettle: minutesToSettle,
        BTCTrend:        btc.Trend,
        BTCMomentum:     btc.MomentumScore,
        BTCBreakout:     btc.IsBreakout,
        BTCRSI:          btc.RSI14_1h,
        BTCChange1h:     btc.PriceChange1h,
        Outcome:         trade.Result,
        ProfitPct:       trade.PnL,
        HoldMinutes:     holdMinutes(trade),
        BTCRegime:       BTCRegime(btc.Trend),
        FundingBucket:   FundingBucket(trade.FundingRate * 100),
    }

    // Generate lesson summary
    lesson, err := e.summarizer.Summarize(ctx, memory)
    if err != nil {
        slog.Warn("lesson summarization failed", "error", err)
        lesson = ""
    }
    memory.Lesson = lesson

    return e.repo.Insert(ctx, memory, embedding)
}

// RecordSkip embeds and stores a skip decision. Called when LLM decides to skip.
func (e *Engine) RecordSkip(
    ctx context.Context,
    candidates []*domain.ScoredCandidate,
    btc *domain.BTCContext,
    skipReason string,
) error {
    if !e.enabled || len(candidates) == 0 {
        return nil
    }

    // Embed the top candidate that was skipped
    top := candidates[0]
    snap := top.Indicators
    minutesToSettle := 0
    next := scheduler.NextFundingTime(time.Now().UTC())
    if !next.IsZero() {
        minutesToSettle = int(math.Round(time.Until(next).Minutes()))
    }

    featureText := BuildFeatureText(snap, btc, &top.Candidate, top.CompositeScore, "SKIP", minutesToSettle)

    embedding, err := e.embedder.Embed(ctx, featureText)
    if err != nil {
        slog.Error("failed to embed skip decision", "error", err)
        return err
    }

    memory := &domain.TradeMemory{
        ID:              uuid.New(),
        Symbol:          top.Candidate.Symbol,
        Action:          domain.ActionSkip,
        CreatedAt:       time.Now(),
        FundingRate:     top.Candidate.FundingRate,
        DailyROI:        top.Candidate.DailyROI,
        OIDelta1h:       snap.OIDelta1h,
        OIDelta15m:      snap.OIDelta15m,
        RSI14_15m:       snap.RSI14_15m,
        RSI7_5m:         snap.RSI7_5m,
        ATRRatio:        snap.ATRRatio,
        VolChange5m:     snap.VolChange5m,
        VolumeSpike:     snap.VolumeSpike,
        MomentumLoss:    snap.MomentumLoss,
        CandlePatterns:  formatPatterns(snap.Patterns),
        CompositeScore:  top.CompositeScore,
        EntryMode:       "SKIP",
        MinutesToSettle: minutesToSettle,
        BTCTrend:        btc.Trend,
        BTCMomentum:     btc.MomentumScore,
        BTCBreakout:     btc.IsBreakout,
        BTCRSI:          btc.RSI14_1h,
        BTCChange1h:     btc.PriceChange1h,
        BTCRegime:       BTCRegime(btc.Trend),
        FundingBucket:   FundingBucket(top.Candidate.FundingRate * 100),
        // Outcome will be filled by skip validation job
    }

    return e.repo.Insert(ctx, memory, embedding)
}

// RetrieveSimilar finds similar historical setups for the current candidate.
// Called synchronously before LLM decision (adds ~5-20ms).
func (e *Engine) RetrieveSimilar(
    ctx context.Context,
    snap *domain.IndicatorSnapshot,
    btc *domain.BTCContext,
    candidate *domain.Candidate,
    score float64,
    entryMode string,
    minutesToSettle int,
) ([]domain.SimilarTrade, error) {
    if !e.enabled {
        return nil, nil
    }

    featureText := BuildFeatureText(snap, btc, candidate, score, entryMode, minutesToSettle)

    embedding, err := e.embedder.Embed(ctx, featureText)
    if err != nil {
        slog.Warn("failed to embed for retrieval, skipping memory", "error", err)
        return nil, nil // non-fatal: proceed without memory
    }

    btcRegime := BTCRegime(btc.Trend)
    fundingBucket := FundingBucket(candidate.FundingRate * 100)

    return e.retriever.FindSimilar(ctx, embedding, btcRegime, fundingBucket)
}

// ValidateSkips checks skipped trades to determine if the skip was correct.
// Runs as a background goroutine on a timer (every skip_validation_delay).
func (e *Engine) ValidateSkips(ctx context.Context, checkPrice func(symbol string, entryTime time.Time) (float64, error)) error {
    if !e.enabled {
        return nil
    }

    pending, err := e.repo.GetPendingSkipValidations(ctx, 2*time.Hour)
    if err != nil {
        return err
    }

    for _, mem := range pending {
        priceChange, err := checkPrice(mem.Symbol, mem.CreatedAt)
        if err != nil {
            slog.Warn("skip validation price check failed", "symbol", mem.Symbol, "error", err)
            continue
        }

        var outcome string
        var profitPct float64

        // For shorts: if price went down, skip was a miss; if price went up, skip was validated
        if priceChange < -1.0 { // would have profited >1%
            outcome = "SKIP_MISSED"
            profitPct = -priceChange // what we would have made
        } else {
            outcome = "SKIP_VALIDATED"
            profitPct = priceChange // positive means we avoided a loss
        }

        if err := e.repo.UpdateOutcome(ctx, mem.ID, outcome, profitPct, 0); err != nil {
            slog.Error("failed to update skip outcome", "id", mem.ID, "error", err)
        }

        // Generate lesson for validated skips
        mem.Outcome = outcome
        mem.ProfitPct = profitPct
        lesson, _ := e.summarizer.Summarize(ctx, &mem)
        if lesson != "" {
            // Update lesson in DB (optional, best-effort)
            _ = e.repo.UpdateLesson(ctx, mem.ID, lesson)
        }
    }

    return nil
}

func formatPatterns(patterns []domain.CandleSignal) string {
    if len(patterns) == 0 {
        return ""
    }
    parts := make([]string, len(patterns))
    for i, p := range patterns {
        parts[i] = fmt.Sprintf("%s:%s(%s)", p.Timeframe, p.Pattern, p.Strength)
    }
    return strings.Join(parts, " ")
}

func holdMinutes(trade *domain.Trade) int {
    if trade.ClosedAt == nil {
        return 0
    }
    return int(trade.ClosedAt.Sub(trade.CreatedAt).Minutes())
}
```

### 9.6 Memory Repository (`internal/storage/memory_repo.go`)

```go
package storage

import (
    "context"
    "time"

    "github.com/google/uuid"
    "github.com/jackc/pgx/v5/pgxpool"
    pgvector "github.com/pgvector/pgvector-go"

    "futures/internal/domain"
)

type PGMemoryRepository struct {
    pool   *pgxpool.Pool
    maxAge time.Duration
}

func NewPGMemoryRepository(pool *pgxpool.Pool, maxAge time.Duration) *PGMemoryRepository {
    return &PGMemoryRepository{pool: pool, maxAge: maxAge}
}

func (r *PGMemoryRepository) Insert(ctx context.Context, memory *domain.TradeMemory, embedding pgvector.Vector) error {
    _, err := r.pool.Exec(ctx, `
        INSERT INTO trade_memories (
            id, trade_id, symbol, action, created_at,
            funding_rate, daily_roi, oi_delta_1h, oi_delta_15m,
            rsi_14_15m, rsi_7_5m, atr_ratio, vol_change_5m,
            volume_spike, momentum_loss, candle_patterns,
            composite_score, entry_mode, minutes_to_settle,
            btc_trend, btc_momentum, btc_breakout, btc_rsi, btc_change_1h,
            outcome, profit_pct, hold_minutes, lesson,
            btc_regime, funding_bucket, embedding
        ) VALUES (
            $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,
            $20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31
        )`,
        memory.ID, memory.TradeID, memory.Symbol, memory.Action, memory.CreatedAt,
        memory.FundingRate, memory.DailyROI, memory.OIDelta1h, memory.OIDelta15m,
        memory.RSI14_15m, memory.RSI7_5m, memory.ATRRatio, memory.VolChange5m,
        memory.VolumeSpike, memory.MomentumLoss, memory.CandlePatterns,
        memory.CompositeScore, memory.EntryMode, memory.MinutesToSettle,
        memory.BTCTrend, memory.BTCMomentum, memory.BTCBreakout, memory.BTCRSI, memory.BTCChange1h,
        memory.Outcome, memory.ProfitPct, memory.HoldMinutes, memory.Lesson,
        memory.BTCRegime, memory.FundingBucket, embedding,
    )
    return err
}

func (r *PGMemoryRepository) FindSimilar(
    ctx context.Context,
    embedding pgvector.Vector,
    btcRegime string,
    fundingBucket int,
    limit int,
) ([]MemorySearchResult, error) {
    cutoff := time.Now().Add(-r.maxAge)

    rows, err := r.pool.Query(ctx, `
        SELECT
            id, trade_id, symbol, action, created_at,
            funding_rate, daily_roi, oi_delta_1h, oi_delta_15m,
            rsi_14_15m, rsi_7_5m, atr_ratio, vol_change_5m,
            volume_spike, momentum_loss, candle_patterns,
            composite_score, entry_mode, minutes_to_settle,
            btc_trend, btc_momentum, btc_breakout, btc_rsi, btc_change_1h,
            outcome, profit_pct, hold_minutes, lesson,
            btc_regime, funding_bucket,
            1 - (embedding <=> $1) AS similarity
        FROM trade_memories
        WHERE outcome IS NOT NULL
          AND created_at > $2
          AND btc_regime = $3
          AND ABS(funding_bucket - $4) <= 1
        ORDER BY embedding <=> $1
        LIMIT $5
    `, embedding, cutoff, btcRegime, fundingBucket, limit)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var results []MemorySearchResult
    for rows.Next() {
        var m domain.TradeMemory
        var similarity float64
        err := rows.Scan(
            &m.ID, &m.TradeID, &m.Symbol, &m.Action, &m.CreatedAt,
            &m.FundingRate, &m.DailyROI, &m.OIDelta1h, &m.OIDelta15m,
            &m.RSI14_15m, &m.RSI7_5m, &m.ATRRatio, &m.VolChange5m,
            &m.VolumeSpike, &m.MomentumLoss, &m.CandlePatterns,
            &m.CompositeScore, &m.EntryMode, &m.MinutesToSettle,
            &m.BTCTrend, &m.BTCMomentum, &m.BTCBreakout, &m.BTCRSI, &m.BTCChange1h,
            &m.Outcome, &m.ProfitPct, &m.HoldMinutes, &m.Lesson,
            &m.BTCRegime, &m.FundingBucket,
            &similarity,
        )
        if err != nil {
            return nil, err
        }
        results = append(results, MemorySearchResult{Memory: m, Similarity: similarity})
    }

    return results, nil
}

func (r *PGMemoryRepository) UpdateOutcome(ctx context.Context, id uuid.UUID, outcome string, profitPct float64, holdMinutes int) error {
    _, err := r.pool.Exec(ctx, `
        UPDATE trade_memories SET outcome=$1, profit_pct=$2, hold_minutes=$3
        WHERE id=$4
    `, outcome, profitPct, holdMinutes, id)
    return err
}

func (r *PGMemoryRepository) UpdateLesson(ctx context.Context, id uuid.UUID, lesson string) error {
    _, err := r.pool.Exec(ctx, `
        UPDATE trade_memories SET lesson=$1 WHERE id=$2
    `, lesson, id)
    return err
}

func (r *PGMemoryRepository) GetByTradeID(ctx context.Context, tradeID uuid.UUID) (*domain.TradeMemory, error) {
    // ... standard query by trade_id ...
    return nil, nil
}

func (r *PGMemoryRepository) GetPendingSkipValidations(ctx context.Context, olderThan time.Duration) ([]domain.TradeMemory, error) {
    cutoff := time.Now().Add(-olderThan)
    rows, err := r.pool.Query(ctx, `
        SELECT id, symbol, action, created_at, funding_rate, daily_roi,
            btc_trend, btc_momentum, btc_breakout, btc_rsi, btc_change_1h,
            oi_delta_1h, rsi_14_15m, candle_patterns, composite_score,
            btc_regime, funding_bucket
        FROM trade_memories
        WHERE action = 'SKIP' AND outcome IS NULL AND created_at < $1
        ORDER BY created_at ASC
        LIMIT 50
    `, cutoff)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var memories []domain.TradeMemory
    for rows.Next() {
        var m domain.TradeMemory
        if err := rows.Scan(
            &m.ID, &m.Symbol, &m.Action, &m.CreatedAt, &m.FundingRate, &m.DailyROI,
            &m.BTCTrend, &m.BTCMomentum, &m.BTCBreakout, &m.BTCRSI, &m.BTCChange1h,
            &m.OIDelta1h, &m.RSI14_15m, &m.CandlePatterns, &m.CompositeScore,
            &m.BTCRegime, &m.FundingBucket,
        ); err != nil {
            return nil, err
        }
        memories = append(memories, m)
    }
    return memories, nil
}
```

---

## 10. LLM Prompt Integration

### 10.1 Updated System Prompt (Addition to Phase 3 prompt)

Append this section to the existing system prompt:

```
MEMORY-ENHANCED DECISION MAKING:
You will sometimes receive "SIMILAR PAST TRADES" — historical setups with conditions close to the current candidates. Use them as follows:

Outcome definitions:
- WIN: Setup fully validated — hit TP1 and trailing captured extended move
- PARTIAL_WIN: TP1 hit (direction correct) but price reversed before trailing captured further gains
- LOSS: Hit hard stop-loss (5%) — setup completely failed
- FORCE_SL: Position was force-closed early by risk check — thesis invalidated after entry
- BREAKEVEN: Position closed flat — inconclusive
- SKIP_VALIDATED: We skipped and price went against short (good skip)
- SKIP_MISSED: We skipped but price dropped (missed opportunity)

How to use outcomes:
- If similar setups show LOSS → strong signal to SKIP or reduce confidence by 15-20 points
- If similar setups show FORCE_SL → setup tends to invalidate quickly — reduce confidence by 10-15 or SKIP
- If similar setups show PARTIAL_WIN → setup works directionally but lacks follow-through — moderate confidence (don't over-size)
- If similar setups show WIN → high confidence, thesis has strong follow-through — boost by 5-10 points
- If similar setups show SKIP_MISSED → consider trading if current confluence is strong
- If similar setups show SKIP_VALIDATED → lean toward SKIP unless current setup is clearly better
- Weight recent memories (< 7 days) more than older ones
- Lessons tell you WHAT specifically went right/wrong — use them for specific guidance, not just outcome statistics
- If no similar trades are provided, decide purely on current data (this is normal for new/rare setups)
```

### 10.2 Updated User Message Template (Addition)

When similar trades are available, append this section after candidates:

```
=== SIMILAR PAST TRADES ===
{{#if similar_trades}}
Historical setups with similar conditions:

{{#each similar_trades}}
[{{index}}] {{similarity}}% similar | {{outcome}} {{profit_pct}}% | {{days_ago}}d ago
    Lesson: "{{lesson}}"
{{/each}}

Win rate of similar setups: {{win_rate}}% ({{win_count}}/{{total_count}})
{{else}}
No similar past trades found (new setup pattern).
{{/if}}
```

### 10.3 Rendered Example

```
=== SIMILAR PAST TRADES ===
Historical setups with similar conditions:

[1] 92% similar | WIN +3.2% | 3d ago
    Lesson: "Extreme funding -0.9% with OI exhaustion (+15%) preceded sharp dump after settlement; BTC neutral allowed reversal to play out"

[2] 88% similar | LOSS -2.1% | 5d ago
    Lesson: "Similar funding/OI setup but BTC pumped 2.5% during hold period, dragging alt up; BTC momentum was 72 vs current 55"

[3] 85% similar | WIN +1.8% | 8d ago
    Lesson: "Waited for AFTER entry instead of FRONTRUN; settlement reaction confirmed direction before entry"

[4] 81% similar | SKIP_VALIDATED +0.3% | 2d ago
    Lesson: "Skipped because ATR ratio 3.5 was too high; price whipsawed 4% before continuing down — would have hit SL"

[5] 78% similar | SKIP_MISSED -2.8% | 6d ago
    Lesson: "Skipped due to BTC momentum concern at 68; BTC actually cooled off and alt dumped 2.8% — should have traded"

Win rate of similar setups: 40% (2/5)
```

---

## 11. Modified Decision Engine Flow

### Phase 3 → Phase 4 Changes in `decision_engine.go`

```go
// Evaluate now accepts similar trades for memory-enhanced decisions
func (e *DecisionEngine) Evaluate(
    ctx context.Context,
    candidates []*domain.ScoredCandidate,
    btc *domain.BTCContext,
    tpPct float64,
    similarTrades []domain.SimilarTrade, // Phase 4: injected from memory engine
) (*domain.LLMDecision, error) {
    req := MapToLLMRequest(candidates, btc, tpPct)

    // Phase 4: attach similar trades
    if len(similarTrades) > 0 {
        req.SimilarTrades = mapSimilarTrades(similarTrades)
    }

    systemPrompt := e.prompt.SystemPrompt()  // now includes memory section
    userMessage := e.prompt.UserMessage(req)  // now includes similar trades section

    // ... rest unchanged ...
}
```

### Modified `app.go` Scan Flow

```go
// Before LLM call — retrieve similar trades for top candidate
var similarTrades []domain.SimilarTrade
if a.memoryEngine != nil && len(top) > 0 {
    topSnap := top[0].Indicators
    similar, err := a.memoryEngine.RetrieveSimilar(ctx,
        topSnap, btc, &top[0].Candidate,
        top[0].CompositeScore, "", minutesToSettle,
    )
    if err != nil {
        slog.Warn("memory retrieval failed, proceeding without", "error", err)
    } else {
        similarTrades = similar
    }
}

// Call LLM with memory context
decision, err := a.llmEngine.Evaluate(ctx, top, btc, a.cfg.Execution.TpPct, similarTrades)

// After trade closes — record in memory (async)
go func() {
    ctx := context.Background()
    if err := a.memoryEngine.RecordTrade(ctx, trade, snap, btc, sc, decision); err != nil {
        slog.Error("failed to record trade memory", "error", err)
    }
}()

// On LLM skip — record skip decision (async)
if decision.Action == "SKIP" && a.cfg.Memory.EmbedSkips {
    go func() {
        ctx := context.Background()
        if err := a.memoryEngine.RecordSkip(ctx, top, btc, decision.SkipReason); err != nil {
            slog.Error("failed to record skip memory", "error", err)
        }
    }()
}
```

---

## 12. Skip Validation Background Job

A background goroutine validates skip decisions by checking what happened to the price after the skip:

```go
// Started in app.go alongside other background goroutines
func (a *App) startSkipValidator(ctx context.Context) {
    delay, _ := time.ParseDuration(a.cfg.Memory.SkipValidationDelay)
    ticker := time.NewTicker(delay)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            if err := a.memoryEngine.ValidateSkips(ctx, a.checkHistoricalPrice); err != nil {
                slog.Error("skip validation failed", "error", err)
            }
        }
    }
}

// checkHistoricalPrice returns the % price change from entry time to +2h later
// Uses Binance kline API to get the 2h price movement after the skip
func (a *App) checkHistoricalPrice(symbol string, entryTime time.Time) (float64, error) {
    // 1. Get mark price at entryTime (from 1m kline close)
    // 2. Get mark price at entryTime + 2h
    // 3. Return percentage change (negative = price went down = would have profited on short)
    return a.exchange.GetPriceChange(symbol, entryTime, entryTime.Add(2*time.Hour))
}
```

---

## 13. Dependency Additions

```bash
go get github.com/pgvector/pgvector-go
```

The `github.com/openai/openai-go` package (already in go.mod) supports embeddings.

---

## 14. Error Handling & Edge Cases

| Scenario                               | Behavior                                                    |
|----------------------------------------|-------------------------------------------------------------|
| Embedding API timeout                  | Skip memory for this trade, log warning                     |
| Embedding API rate limit               | Retry once after 1s, then skip                              |
| pgvector query returns 0 results       | Proceed without memory ("No similar trades found")          |
| All similar trades have no outcome yet | Filter them out, proceed with empty set                     |
| Summarizer fails                       | Use fallback: "{OUTCOME}: {pnl}% in {regime}"              |
| Skip validation price API fails        | Skip this entry, retry next cycle                           |
| Memory table empty (cold start)        | System works identically to Phase 3                         |
| Similar trades all from same symbol    | Still valid — cross-symbol matching is a bonus, not required|
| Very old memories (>90d)               | Excluded by max_memory_age filter                           |
| Memory disabled (`enabled: false`)     | Falls back to Phase 3 behavior (no memory)                  |
| pgvector extension not installed       | Startup check → disable memory with warning                 |

---

## 15. Performance Considerations

| Operation                  | Latency    | When                    | Impact                          |
|----------------------------|------------|-------------------------|---------------------------------|
| Embed current setup        | ~100ms     | Before LLM call (sync)  | Adds to scan cycle latency      |
| pgvector similarity search | ~5-20ms    | Before LLM call (sync)  | Negligible                      |
| Embed completed trade      | ~100ms     | After trade close (async)| Zero impact on trading          |
| Summarize lesson           | ~1-2s      | After trade close (async)| Zero impact on trading          |
| Skip validation            | ~500ms     | Background job           | Zero impact on trading          |

**Total sync overhead per scan cycle: ~120ms** (embed + query). This is acceptable given the 1-minute scan interval.

### Optimization: Embedding Cache

For the read path, cache the current setup's embedding in Redis for the duration of the funding window (30min TTL). If the top candidate hasn't changed, reuse the cached embedding instead of re-calling the API.

```go
// Redis key: "memory:embed:{symbol}:{window_id}"
// Value: serialized []float32
// TTL: 30 minutes
```

---

## 16. Testing Strategy

### Unit Tests

| Test                                         | What it validates                                        |
|----------------------------------------------|----------------------------------------------------------|
| `TestBuildFeatureText`                       | Correct text formatting from domain types                |
| `TestFundingBucket`                          | Bucket boundaries (mild/moderate/extreme)                |
| `TestBTCRegime`                              | Regime mapping from trend string                         |
| `TestEmbedder_Success`                       | Correct API call + vector parsing                        |
| `TestEmbedder_Timeout`                       | Returns error on timeout                                 |
| `TestRetriever_FiltersSimilarity`            | Results below min_similarity are excluded                |
| `TestRetriever_FiltersNullOutcome`           | Memories without outcomes are excluded                   |
| `TestSummarizer_Success`                     | Generates non-empty lesson                               |
| `TestSummarizer_Fallback`                    | Returns fallback on LLM failure                          |
| `TestEngine_RecordTrade`                     | Full write path: embed → summarize → store               |
| `TestEngine_RecordSkip`                      | Skip path stores without outcome                         |
| `TestEngine_RetrieveSimilar`                 | Read path: embed → query → filter → return               |
| `TestEngine_Disabled`                        | All methods are no-op when disabled                      |
| `TestValidateSkips_PriceDown`               | Skip marked as SKIP_MISSED when price dropped            |
| `TestValidateSkips_PriceUp`                 | Skip marked as SKIP_VALIDATED when price rose            |

### Integration Tests

| Test                                         | What it validates                                        |
|----------------------------------------------|----------------------------------------------------------|
| `TestMemoryRepo_InsertAndFind`               | pgvector insert + cosine similarity search               |
| `TestMemoryRepo_FilterByRegime`              | BTC regime filter narrows results correctly              |
| `TestMemoryRepo_FilterByBucket`              | Funding bucket filter works with ±1 tolerance            |
| `TestMemoryRepo_MaxAge`                      | Old memories excluded from search                        |
| `TestEngine_EndToEnd`                        | Record trade → retrieve similar → verify match           |
| `TestLLMPrompt_WithSimilarTrades`            | Prompt renders similar trades section correctly          |
| `TestDecisionEngine_WithMemory`              | LLM receives and uses similar trades in decision         |

### Manual Validation

- Run in paper mode with `memory.enabled: true`
- After 10+ trades: verify similar trades appear in LLM prompts
- Compare LLM decisions with/without memory on identical setups
- Check skip validation: verify SKIP_MISSED/SKIP_VALIDATED accuracy
- Monitor embedding API latency (should stay <200ms)

---

## 17. Cold Start Strategy

The memory engine has no value until enough trades are recorded. Strategy:

1. **Phase 4a (first 2 weeks)**: Memory engine runs in **record-only mode** — embeds all trades and skips but does NOT inject into LLM prompts. This builds the initial vector database.

2. **Phase 4b (after ~50 recorded setups)**: Enable retrieval. The system now injects similar trades into prompts.

3. **Optional**: Backfill from existing `indicator_snapshots` table. Since we already store full indicator data per trade, we can retroactively embed historical trades:

```go
// One-time backfill script
func BackfillMemories(ctx context.Context, pool *pgxpool.Pool, embedder *Embedder) error {
    // Query indicator_snapshots JOIN trades WHERE trade has result
    // For each: build feature text → embed → insert into trade_memories
    // Rate limit: 500 req/min for embeddings API
}
```

---

## 18. Implementation Checklist

| #  | Task                                                          | Dependencies |
|----|---------------------------------------------------------------|--------------|
| 1  | Add `pgvector-go` to go.mod                                  | None         |
| 2  | Add `MemoryConfig` to config struct + defaults                | None         |
| 3  | Create migration `005_create_trade_memories`                  | None         |
| 4  | Create `internal/domain/memory.go`                            | None         |
| 5  | Create `internal/memory/feature_builder.go`                   | #4           |
| 6  | Create `internal/memory/embedder.go`                          | #1           |
| 7  | Create `internal/memory/summarizer.go`                        | None         |
| 8  | Create `internal/memory/retriever.go`                         | #4           |
| 9  | Create `internal/memory/engine.go`                            | #5,#6,#7,#8  |
| 10 | Create `internal/storage/memory_repo.go`                      | #1,#3,#4     |
| 11 | Update `internal/llm/schema.go` (activate SimilarTrades)      | #4           |
| 12 | Update `internal/llm/prompt.go` (add memory section)          | #11          |
| 13 | Update `internal/llm/mapper.go` (map similar trades)          | #11          |
| 14 | Update `internal/llm/decision_engine.go` (accept similar)     | #12,#13      |
| 15 | Add `GetPriceChange` to exchange client                       | None         |
| 16 | Modify `app.go`: wire memory engine + hook into trade lifecycle | #9,#10,#14  |
| 17 | Add skip validation background job                            | #9,#15       |
| 18 | Add embedding cache in Redis (optional optimization)          | #6           |
| 19 | Write unit tests                                              | #9,#10       |
| 20 | Write integration tests (requires pgvector-enabled Postgres)  | #10          |
| 21 | Create backfill script for existing trades                    | #9,#10       |
| 22 | End-to-end paper trading with memory enabled                  | All          |

---

## 19. Monitoring & Observability

```go
// Log on every memory write
slog.Info("memory_recorded",
    "action", memory.Action,
    "symbol", memory.Symbol,
    "outcome", memory.Outcome,
    "lesson_length", len(memory.Lesson),
    "embed_latency_ms", embedLatency.Milliseconds(),
)

// Log on every memory retrieval
slog.Info("memory_retrieved",
    "candidate", topCandidate.Symbol,
    "similar_count", len(similarTrades),
    "top_similarity", topSimilarity,
    "win_rate", winRate,
    "query_latency_ms", queryLatency.Milliseconds(),
)

// Log on skip validation
slog.Info("skip_validated",
    "id", memory.ID,
    "symbol", memory.Symbol,
    "outcome", outcome,
    "hypothetical_pnl", profitPct,
)
```

---

## 20. Summary

Phase 4 adds a learning layer that improves decision quality over time:

- **Write Path** (async): Trade closes → build feature text → embed → summarize lesson → store in pgvector
- **Read Path** (sync, +120ms): Before LLM call → embed current setup → cosine similarity search → inject top 5 similar trades into prompt
- **Skip Tracking**: LLM skips are embedded and retroactively validated against actual price movement
- **Metadata Filtering**: BTC regime + funding bucket pre-filter ensures semantically meaningful comparisons
- **Text Embedding**: Structured feature text captures semantic relationships between indicators (better than raw numerical vectors)
- **Cold Start**: Record-only mode for first 2 weeks, then enable retrieval after ~50 trades
- **Cost**: ~$1-4/month additional (embeddings + summarization)
- **Backward Compatible**: `memory.enabled: false` reverts to Phase 3 behavior
- **Self-Improving**: Each trade (or skip) adds to the knowledge base, making future decisions more informed
