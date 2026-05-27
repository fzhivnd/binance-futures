# Phase 3: LLM Decision Engine Integration

---

## 1. Overview

Phase 3 replaces the deterministic score-threshold gate (`score >= 60`) with an **LLM-powered decision engine** that evaluates pre-scored candidates and makes intelligent trade decisions. The LLM acts as the **final decision layer** — it receives compact, pre-processed candidate data and returns a structured trade decision.

### What Changes

| Before (Phase 2)                          | After (Phase 3)                                    |
|-------------------------------------------|----------------------------------------------------|
| Score >= 60 → auto-execute best candidate | Score → top 3-5 candidates → LLM picks 1 or skips |
| Deterministic confidence from score       | LLM provides independent confidence (0-100)        |
| Window determines entry mode              | LLM recommends entry mode (frontrun/last/after)    |
| Fixed SL/TP from config                   | LLM suggests TP strategy (base/aggressive/trail)   |
| No reasoning                              | LLM provides entry reasons + warnings              |

### What Stays the Same

- All pre-LLM pipeline: funding scan → ROI/volume filter → indicator compute → scoring
- Risk engine still does pre-checks (kill switch, cooldown, daily loss, drawdown)
- Execution engine still places orders (paper/live)
- Position manager: breakeven SL logic unchanged; close detection via user data stream (pre-Phase 3 improvement)
- WebSocket market data infrastructure unchanged (mark price + klines + user data stream)

---

## 2. Tech Stack Additions

| Component        | Choice                       | Reason                                                    |
|------------------|------------------------------|-----------------------------------------------------------|
| LLM Model        | GPT-4.1-mini                 | Fast (~1-2s), cheap ($0.40/1M in), great structured JSON  |
| LLM SDK          | `github.com/openai/openai-go` | Official Go SDK, streaming support, typed responses    |
| Response Format  | Structured Outputs (JSON)    | Deterministic parsing, no regex/manual extraction needed   |
| Fallback         | Skip trade                   | If LLM unavailable/timeout → no trade                     |
| Timeout          | 15 seconds hard limit        | Trading is time-sensitive; abandon if too slow             |
| Retry            | 1 retry with 5s timeout      | Quick retry on transient failure, then skip                |
| Rate Limiting    | Token bucket (10 req/min)    | Prevent runaway API calls during rapid scan cycles         |

### Cost Estimate

Model pricing (gpt-4.1-mini, 2025-05): **$0.40 / 1M input tokens**, **$1.60 / 1M output tokens**.

Typical call: ~2,000 input tokens (system prompt + 5 candidates) + ~300 output tokens = **~$0.00088 / call**.

With `call_cooldown_secs: 300` (5-minute cooldown), LLM calls are suppressed when inputs are stable:

| Funding interval | Windows/Day | Actual calls/window | Calls/Day | Cost/Day  | Monthly   |
|------------------|-------------|---------------------|-----------|-----------|-----------|
| 8h (3/day)       | 3 × 3 types | ~6 stable / 30 max  | ~54 (max) | ~$0.047   | ~$1.42    |
| 8h with cooldown | 3 × 3 types | ~6 per window       | ~18       | ~$0.016   | ~$0.48    |
| 4h (6/day)       | 6 × 3 types | ~6 per window       | ~36       | ~$0.032   | ~$0.96    |
| 1h (24/day)      | 24 × 3 types| ~6 per window       | ~144      | ~$0.127   | ~$3.81    |

The rate limiter (`max_rpm: 30`) acts as a hard ceiling regardless of scan frequency.

---

## 3. Project Structure (New/Modified Files)

```
internal/
├── llm/
│   ├── client.go              # OpenAI API client wrapper (timeout, retry, rate limit)
│   ├── decision_engine.go     # Main decision engine: builds prompt, calls LLM, parses response
│   ├── prompt.go              # Prompt template builder (system + user message construction)
│   ├── schema.go              # Request/response JSON schemas (LLMRequest, LLMResponse)
│   ├── mapper.go              # Maps domain types → LLM request format
│   └── decision_engine_test.go
├── intent/
│   ├── queue.go               # TradeIntent queue: stores pending intent, fires on window match
│   ├── intent.go              # TradeIntent domain type + lifecycle
│   └── queue_test.go
├── config/
│   └── config.go              # + LLMConfig struct
├── domain/
│   └── llm_decision.go        # LLMDecision domain type
├── app/
│   └── app.go                 # Modified: inject LLM engine + intent queue, update scanFn
├── scheduler/
│   └── scheduler.go           # Modified: uniform 1m scan interval

config/
└── config.yaml                # + llm section

migrations/
└── 004_add_llm_fields_to_trades.up.sql
```

---

## 4. System Design

### Architecture Diagram

```
┌──────────────────────────────────────────────────────────────────────────┐
│                     SCAN CYCLE (every 1m, T-30m → T+1m)                  │
├──────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  ┌──────────────┐    ┌──────────────┐    ┌────────────────────┐         │
│  │ Funding Scan │───▶│ ROI + Volume │───▶│ Indicator Compute  │         │
│  │  (all coins) │    │   Filter     │    │  (per candidate)   │         │
│  └──────────────┘    └──────────────┘    └────────┬───────────┘         │
│                                                    │                     │
│                                                    ▼                     │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │                    SCORER (Phase 2)                                │  │
│  │  Composite score 0-100, rank by score, take top 3-5               │  │
│  └──────────────────────────────┬────────────────────────────────────┘  │
│                                 │ top 3-5 ScoredCandidates              │
│                                 ▼                                        │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │              LLM DECISION ENGINE (Phase 3)                        │  │
│  │                                                                   │  │
│  │  1. Map candidates → compact JSON                                 │  │
│  │  2. Build prompt (system + user message)                          │  │
│  │  3. Call GPT-4.1-mini (15s timeout)                               │  │
│  │  4. Parse structured JSON response                                │  │
│  │  5. Return: TradeIntent (symbol, confidence, entry_mode, etc.)    │  │
│  │                                                                   │  │
│  │  On failure → return SKIP (fallback)                              │  │
│  └──────────────────────────────┬────────────────────────────────────┘  │
│                                 │ LLMDecision                           │
│                                 ▼                                        │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │              INTENT QUEUE (Phase 3 - new)                         │  │
│  │                                                                   │  │
│  │  • Ranked by confidence; dedup per (symbol, entry_mode)           │  │
│  │  • FRONTRUN: fires best eligible every frontrun_exec_interval     │  │
│  │  • LAST_MINUTE / AFTER: fires best eligible once on window entry  │  │
│  │  • Each window type fires independently (max 1 trade each)        │  │
│  │  • Auto-clears after funding settlement                           │  │
│  └──────────────────────────────┬────────────────────────────────────┘  │
│                                 │ when window matches                    │
│                                 ▼                                        │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │              RISK ENGINE (Phase 2)                                 │  │
│  │  Post-LLM validation: BTC breakout, ATR, drawdown                 │  │
│  └──────────────────────────────┬────────────────────────────────────┘  │
│  │                                                                   │  │
│                                 ▼                                        │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │              EXECUTION ENGINE                                      │  │
│  │  Position size from LLM confidence, entry mode from intent        │  │
│  └───────────────────────────────────────────────────────────────────┘  │
│                                                                          │
└──────────────────────────────────────────────────────────────────────────┘
```

### Flow Detail

```
1. Scheduler runs scan every 1m throughout the funding window (T-30m → T+1m)
2. Each scan: funding filter → ROI/volume filter → indicator compute → scoring
3. Take top N candidates (configurable, default 5)
4. LLM call cooldown check: skip if same symbol/score/BTC/window within call_cooldown_secs
5. Call LLM with candidates + BTC context + minutes to settlement + projected TP1
6. LLM returns: OPEN_SHORT (symbol, confidence, entry_mode) or SKIP
7. If OPEN_SHORT → add/replace intent in ranked queue (always replaces by symbol)
   - A SKIP does NOT cancel existing queued intents
8. Intent Queue ticks every 1s:
   - FRONTRUN window: fire highest-confidence eligible intent every frontrun_exec_interval_secs
   - LAST_MINUTE / AFTER window: fire highest-confidence eligible intent once on entry
   - Each window type fires independently (FRONTRUN fire ≠ blocks LAST_MINUTE)
9. At fire time: re-validate risk (BTC, drawdown) before executing
10. Queue auto-clears after funding settlement (T+1m)
```

### Intent Queue State Machine (per intent)

```
                    ┌─────────────────────────┐
                    │       PENDING           │
                    │  waiting for its        │◄──── always replaced when new
                    │  target window          │      intent arrives for same symbol
                    └────────────┬────────────┘
                                 │ windowReady(current >= target)
                                 │ AND firedInWindow[window] == false
                                 ▼
                    ┌─────────────────────────┐
                    │       FIRING            │
                    │  risk check → execute   │
                    └────────────┬────────────┘
                                 │
                    ┌────────────┼────────────┐
                    ▼            ▼            ▼
               ┌────────┐  ┌────────┐  ┌────────┐
               │ FIRED  │  │REJECTED│  │EXPIRED │
               │(trade) │  │(risk)  │  │(T+1m)  │
               └────────┘  └────────┘  └────────┘
```

### Window Matching Logic

| LLM says entry_mode | Current Window = FRONTRUN | Current Window = LAST_MINUTE | Current Window = AFTER |
|---------------------|---------------------------|------------------------------|------------------------|
| FRONTRUN            | Fire immediately          | Fire immediately (late OK)   | Fire immediately       |
| LAST_MINUTE         | Hold → wait for T-5m (scan stops at T-3m) | Fire immediately | Fire immediately  |
| AFTER               | Hold → wait for T+0       | Hold → wait for T+0          | Fire immediately       |

Rule: intent fires when current window >= target window (later is always OK, we don't go backward).

### LLM Call Cooldown

Scans run every 1 minute, but market conditions change slowly. To avoid calling the LLM 30 times per window with nearly identical data, a `lastLLMCall` state is tracked in `App`:

```
Skip LLM call if ALL of:
  - Same top candidate symbol as last call
  - Composite score changed < 5 points since last call
  - BTC trend unchanged since last call
  - Same window type (FRONTRUN / LAST_MINUTE / AFTER) as last call
  - Time since last call < call_cooldown_secs (default 5m)
```

A window transition always triggers a fresh LLM call regardless of other conditions.

**Effect:** With a 5-minute cooldown and 30-minute FRONTRUN window, calls drop from ~30 to ~6 per window under stable conditions, while remaining fully responsive to meaningful changes.

---

## 5. Data Flow & Contracts

### 5.1 LLM Request Schema

```go
// internal/llm/schema.go

type LLMRequest struct {
    Timestamp           int64          `json:"timestamp"`
    MinutesToSettlement int            `json:"minutes_to_settlement"` // remaining minutes; LLM picks entry_mode
    BTCContext          LLMBTCContext  `json:"btc_context"`
    Candidates          []LLMCandidate `json:"candidates"`
    // Phase 4: SimilarTrades []LLMSimilarTrade `json:"similar_past_trades,omitempty"`
}

type LLMBTCContext struct {
    Trend         string  `json:"trend"`          // "bullish" | "bearish" | "neutral"
    MomentumScore int     `json:"momentum_score"` // 0-100
    Volatility    string  `json:"volatility"`     // "low" | "medium" | "high"
    IsBreakout    bool    `json:"is_breakout"`
    RSI           float64 `json:"rsi"`
    PriceChange1h float64 `json:"price_change_1h_pct"`
}

type LLMCandidate struct {
    Symbol          string          `json:"symbol"`
    FundingRate     float64         `json:"funding_rate_pct"`    // e.g. -0.85 means -0.85%
    DailyROI        float64         `json:"daily_roi_pct"`       // e.g. 34.5 means 34.5%
    ProjectedTP1Pct float64         `json:"projected_tp1_pct"`   // base TP + funding fee (FRONTRUN/LAST_MINUTE only)
    CompositeScore  float64         `json:"composite_score"`     // 0-100
    ScoreBreakdown  LLMBreakdown    `json:"score_breakdown"`
    RSI14_15m       float64         `json:"rsi_14_15m"`          // setup context (~3.5h lookback)
    RSI7_5m         float64         `json:"rsi_7_5m"`            // entry timing (~35 min lookback)
    OIDelta1h       float64         `json:"oi_delta_1h_pct"`     // setup confirmation
    OIDelta15m      float64         `json:"oi_delta_15m_pct"`    // recent leverage buildup
    ATRRatio        float64         `json:"atr_ratio"`           // ATR/price * 100
    VolChange5m     float64         `json:"vol_change_5m_pct"`   // current 5m vol vs 2h avg
    VolumeSpikeFlag bool            `json:"volume_spike"`
    MomentumLoss    bool            `json:"momentum_loss"`
    CandlePatterns  []LLMCandleInfo `json:"candle_patterns"`
}

type LLMBreakdown struct {
    Funding    float64 `json:"funding"`
    OI         float64 `json:"oi"`
    BTC        float64 `json:"btc"`
    Candle     float64 `json:"candle"`
    Volume     float64 `json:"volume"`
    ROI        float64 `json:"roi"`
    Volatility float64 `json:"volatility"`
}

type LLMCandleInfo struct {
    Timeframe string `json:"timeframe"` // "1h" | "30m" | "15m" | "5m"
    Pattern   string `json:"pattern"`   // "SHOOTING_STAR", "BEARISH_ENGULFING", etc.
    Strength  string `json:"strength"`  // "STRONG" | "MEDIUM" | "WEAK"
}

// Phase 4 - placeholder for similar past trades
type LLMSimilarTrade struct {
    Outcome     string  `json:"outcome"`      // "WIN" | "LOSS"
    ProfitPct   float64 `json:"profit_pct"`
    Similarity  float64 `json:"similarity"`   // 0-1 cosine similarity
    KeyFactors  string  `json:"key_factors"`  // summarized lesson
}
```

### 5.2 LLM Response Schema

```go
// internal/llm/schema.go

type LLMResponse struct {
    Action       string   `json:"action"`        // "OPEN_SHORT" | "SKIP"
    Symbol       string   `json:"symbol"`        // selected candidate symbol (if OPEN_SHORT)
    Confidence   int      `json:"confidence"`    // 0-100
    EntryMode    string   `json:"entry_mode"`    // "FRONTRUN" | "LAST_MINUTE" | "AFTER" — LLM decides
    EntryReasons []string `json:"entry_reasons"` // 2-4 concise reasons
    Warnings     []string `json:"warnings"`      // 0-3 risk warnings
    SkipReason   string   `json:"skip_reason"`   // only if action=SKIP
}
```

### 5.3 Domain Type

```go
// internal/domain/llm_decision.go

type LLMDecision struct {
    Action       string
    Symbol       string
    Confidence   int
    EntryMode    EntryMode
    EntryReasons []string
    Warnings     []string
    SkipReason   string
}
```

---

## 6. LLM Prompt Design

### 6.1 System Prompt

```
You are a professional Binance Futures funding-rate reversal trader. Your role is to evaluate pre-filtered short candidates and make a final trade decision.

STRATEGY CONTEXT:
- We short coins with extremely negative funding rates (-0.2% to -2%) during overextension setups
- Goal: capture funding fee yield + price reversal on overleveraged longs
- Risk: squeeze events where price pumps further despite negative funding
- Leverage: 20x
- Hard stop-loss: ~5% price move against us
- This means high-ATR coins with ATR ratio > 3 can easily hit our SL on normal volatility — factor this into confidence

DECISION FRAMEWORK:
1. Evaluate each candidate's setup quality holistically
2. Consider BTC market context (bullish BTC = dangerous for alt shorts)
3. Look for confluence: strong funding + rising OI + bearish candle patterns + momentum exhaustion
4. Avoid: low confluence setups, squeeze risk (extreme OI + no reversal signal), BTC breakout environment

ENTRY MODE LOGIC:
- FRONTRUN: Enter 15-30m before funding settlement. Best when: high confidence, clear reversal forming. Captures full funding fee (included in Projected TP1). Risk: price can still pump before settlement.
- LAST_MINUTE: Enter 1-5m before settlement. Best when: moderate confidence, want direction confirmation. Captures funding fee (included in Projected TP1). Safer than FRONTRUN.
- AFTER: Enter 0-5m after settlement. Best when: want to see actual settlement reaction. Does NOT capture funding fee — effective TP1 is lower than shown. Lowest squeeze risk.

Note: "Projected TP1" shown per candidate includes the funding fee and applies to FRONTRUN/LAST_MINUTE entries only. For AFTER entries, subtract the funding rate from Projected TP1 to get the effective target.

RISK PARAMETERS:
- Hard SL: 5% adverse price move triggers stop
- Base TP: ~2% price move = 40% ROI at 20x
- Breakeven trigger: move SL to entry after 1% profit
- Consider: if a coin's ATR ratio is high (>3), normal price swings may trigger our tight SL before the thesis plays out. Reduce confidence for high-volatility setups unless reversal signal is very strong.

CONFIDENCE SCORING (0-100):
- 90-100: Exceptional setup. Multiple strong confluence signals. Very high probability reversal.
- 80-89: Strong setup. Good confluence. Clear entry signal.
- 70-79: Decent setup. Moderate confluence. Some uncertainty.
- 60-69: Marginal setup. Weak confluence. Only trade if nothing better.
- <60: Do NOT return this. Return SKIP instead.

RULES:
- You MUST select exactly ONE candidate or SKIP all
- If no candidate has clear edge, SKIP. No trade is better than a bad trade.
- If BTC is in strong bullish breakout, heavily penalize all candidates (shorts are dangerous)
- Provide 2-4 concise entry reasons explaining your decision
- Flag any warnings about the setup (risks, concerns)
- Be probabilistic in reasoning, not certain
```

### 6.2 User Message Template

```
Current time: {{timestamp_utc}}
Next funding settlement in: {{minutes_to_settlement}}m

=== BTC MARKET CONTEXT ===
Trend: {{btc_trend}} | Momentum: {{btc_momentum}}/100 | Volatility: {{btc_volatility}}
Breakout: {{btc_breakout}} | RSI(14): {{btc_rsi}} | 1h change: {{btc_1h_pct}}%

=== CANDIDATES (ranked by composite score) ===

{{#each candidates}}
[{{index}}] {{symbol}}
  Funding: {{funding_rate_pct}}% | Daily ROI: {{daily_roi_pct}}% | Projected TP1: {{projected_tp1_pct}}% | Score: {{composite_score}}/100
  Score breakdown: funding={{breakdown.funding}} oi={{breakdown.oi}} btc={{breakdown.btc}} candle={{breakdown.candle}} vol={{breakdown.volume}} roi={{breakdown.roi}} volatility={{breakdown.volatility}}
  RSI(14,15m): {{rsi_14_15m}} | RSI(7,5m): {{rsi_7_5m}} | OI delta 1h: +{{oi_delta_1h_pct}}% | OI delta 15m: +{{oi_delta_15m_pct}}%
  ATR ratio: {{atr_ratio}} | Vol change 5m: {{vol_change_5m_pct}}% | Volume spike: {{volume_spike}} | Momentum loss: {{momentum_loss}}
  Candle patterns: {{#each candle_patterns}}[{{timeframe}}:{{pattern}}({{strength}})] {{/each}}
{{/each}}

Evaluate these candidates and provide your trade decision.
```

### 6.3 Example Prompt (Rendered)

```
Current time: 2024-03-15T15:30:00Z
Next funding settlement in: 4m

=== BTC MARKET CONTEXT ===
Trend: neutral | Momentum: 55/100 | Volatility: medium
Breakout: false | RSI(14): 52.3 | 1h change: -0.3%

=== CANDIDATES (ranked by composite score) ===

[1] 1000PEPEUSDT
  Funding: -0.85% | Daily ROI: 34.5% | Projected TP1: 2.95% | Score: 78/100
  Score breakdown: funding=21.3 oi=15.0 btc=7.0 candle=18.5 vol=10.0 roi=15.0 volatility=5.0
  RSI(14,15m): 71.2 | RSI(7,5m): 74.8 | OI delta 1h: +18.3% | OI delta 15m: +6.2%
  ATR ratio: 2.8 | Vol change 5m: 3.1% | Volume spike: true | Momentum loss: true
  Candle patterns: [1h:SHOOTING_STAR(STRONG)] [30m:BEARISH_ENGULFING(STRONG)] [15m:DOJI_AFTER_PUMP(MEDIUM)]

[2] WIFUSDT
  Funding: -0.62% | Daily ROI: 28.1% | Projected TP1: 2.72% | Score: 71/100
  Score breakdown: funding=18.5 oi=11.2 btc=7.0 candle=14.2 vol=5.0 roi=15.0 volatility=3.0
  RSI(14,15m): 65.8 | RSI(7,5m): 68.1 | OI delta 1h: +12.1% | OI delta 15m: +3.4%
  ATR ratio: 3.1 | Vol change 5m: 1.2% | Volume spike: false | Momentum loss: false
  Candle patterns: [1h:UPPER_WICK_REJECTION(MEDIUM)] [5m:FAILED_BREAKOUT(WEAK)]

[3] DOGEUSDT
  Funding: -0.41% | Daily ROI: 22.3% | Projected TP1: 2.51% | Score: 64/100
  Score breakdown: funding=15.2 oi=7.5 btc=7.0 candle=12.0 vol=5.0 roi=15.0 volatility=3.0
  RSI(14,15m): 58.2 | RSI(7,5m): 55.9 | OI delta 1h: +5.2% | OI delta 15m: +1.1%
  ATR ratio: 1.9 | Vol change 5m: 0.4% | Volume spike: false | Momentum loss: false
  Candle patterns: [30m:DOJI_AFTER_PUMP(WEAK)]

Evaluate these candidates and provide your trade decision.
```

### 6.4 Expected Response

```json
{
  "action": "OPEN_SHORT",
  "symbol": "1000PEPEUSDT",
  "confidence": 82,
  "entry_mode": "LAST_MINUTE",
  "entry_reasons": [
    "Extreme funding -0.85% with OI still rising +18% indicates overleveraged longs about to face settlement pressure",
    "Strong bearish candle confluence across 1h/30m/15m with shooting star + engulfing pattern",
    "RSI(14,15m) 71 + RSI(7,5m) 74 with momentum loss signals exhaustion at resistance",
    "Volume spike on 5m (+3.1% vs avg) confirms active distribution"
  ],
  "warnings": [
    "OI delta 15m +6.2% still accelerating — could indicate incoming squeeze if price breaks above recent high",
    "BTC momentum neutral but watch for sudden pump that could drag alts up"
  ],
  "skip_reason": ""
}
```

---

## 7. Detailed Implementation Logic

### 7.1 Client (`internal/llm/client.go`)

```go
package llm

import (
    "context"
    "time"

    openai "github.com/openai/openai-go"
    "github.com/openai/openai-go/option"
)

type ClientConfig struct {
    APIKey     string
    Model      string        // "gpt-4.1-mini"
    Timeout    time.Duration // 5s
    RetryCount int           // 1
    RetryDelay time.Duration // 500ms
    MaxRPM     int           // 10 requests per minute
}

type Client struct {
    client  *openai.Client
    cfg     ClientConfig
    limiter *rateLimiter // token bucket
}

func NewClient(cfg ClientConfig) *Client {
    client := openai.NewClient(
        option.WithAPIKey(cfg.APIKey),
        option.WithRequestTimeout(cfg.Timeout),
    )
    return &Client{
        client:  client,
        cfg:     cfg,
        limiter: newRateLimiter(cfg.MaxRPM),
    }
}

// Call sends a structured chat completion request and returns the raw response.
// Handles timeout, retry (1x), and rate limiting.
// Returns error on: timeout, rate limit exceeded, API error, invalid response.
func (c *Client) Call(ctx context.Context, systemPrompt, userMessage string) (string, error) {
    // 1. Check rate limit
    // 2. Build request with structured output (response_format: json_schema)
    // 3. Call API
    // 4. On transient error: retry once with shorter timeout
    // 5. Return raw JSON string from response
}
```

### 7.2 Decision Engine (`internal/llm/decision_engine.go`)

```go
package llm

import (
    "context"
    "encoding/json"
    "fmt"
    "log/slog"

    "futures/internal/domain"
    "futures/internal/scheduler"
)

type DecisionEngine struct {
    client *Client
    prompt *PromptBuilder
}

func NewDecisionEngine(client *Client) *DecisionEngine {
    return &DecisionEngine{
        client: client,
        prompt: NewPromptBuilder(),
    }
}

// Evaluate takes top scored candidates and returns a trade decision.
// On any LLM failure → returns SKIP decision (safe fallback).
func (e *DecisionEngine) Evaluate(
    ctx context.Context,
    candidates []*domain.ScoredCandidate,
    btc *domain.BTCContext,
    window scheduler.WindowType,
    // Phase 4: similarTrades []domain.SimilarTrade,
) (*domain.LLMDecision, error) {
    // 1. Map domain candidates → LLMRequest
    req := MapToLLMRequest(candidates, btc, window)

    // 2. Build prompt messages
    systemPrompt := e.prompt.SystemPrompt()
    userMessage := e.prompt.UserMessage(req)

    // 3. Call LLM
    raw, err := e.client.Call(ctx, systemPrompt, userMessage)
    if err != nil {
        slog.Warn("LLM call failed, falling back to SKIP", "error", err)
        return &domain.LLMDecision{Action: "SKIP", SkipReason: "LLM unavailable"}, nil
    }

    // 4. Parse response
    var resp LLMResponse
    if err := json.Unmarshal([]byte(raw), &resp); err != nil {
        slog.Warn("LLM response parse failed, falling back to SKIP", "error", err)
        return &domain.LLMDecision{Action: "SKIP", SkipReason: "LLM response invalid"}, nil
    }

    // 5. Validate response
    decision := mapResponseToDecision(resp, candidates)
    if decision.Action == "OPEN_SHORT" && decision.Confidence < 60 {
        decision.Action = "SKIP"
        decision.SkipReason = "LLM confidence below threshold"
    }

    slog.Info("LLM decision",
        "action", decision.Action,
        "symbol", decision.Symbol,
        "confidence", decision.Confidence,
        "entry_mode", decision.EntryMode,
        "reasons", decision.EntryReasons,
    )

    return decision, nil
}
```

### 7.3 Prompt Builder (`internal/llm/prompt.go`)

```go
package llm

import (
    "fmt"
    "strings"
    "time"
)

type PromptBuilder struct{}

func NewPromptBuilder() *PromptBuilder {
    return &PromptBuilder{}
}

func (p *PromptBuilder) SystemPrompt() string {
    return systemPromptText // the constant defined in section 6.1
}

func (p *PromptBuilder) UserMessage(req *LLMRequest) string {
    var sb strings.Builder

    // Header
    t := time.Unix(req.Timestamp, 0).UTC()
    sb.WriteString(fmt.Sprintf("Current time: %s\n", t.Format(time.RFC3339)))
    sb.WriteString(fmt.Sprintf("Next funding settlement in: %dm\n\n", req.MinutesToSettlement))

    // BTC context
    sb.WriteString("=== BTC MARKET CONTEXT ===\n")
    sb.WriteString(fmt.Sprintf("Trend: %s | Momentum: %d/100 | Volatility: %s\n",
        req.BTCContext.Trend, req.BTCContext.MomentumScore, req.BTCContext.Volatility))
    sb.WriteString(fmt.Sprintf("Breakout: %v | RSI(14): %.1f | 1h change: %.2f%%\n\n",
        req.BTCContext.IsBreakout, req.BTCContext.RSI, req.BTCContext.PriceChange1h))

    // Candidates
    sb.WriteString("=== CANDIDATES (ranked by composite score) ===\n\n")
    for i, c := range req.Candidates {
        sb.WriteString(fmt.Sprintf("[%d] %s\n", i+1, c.Symbol))
        sb.WriteString(fmt.Sprintf("  Funding: %.2f%% | Daily ROI: %.1f%% | Projected TP1: %.2f%% | Score: %.0f/100\n",
            c.FundingRate, c.DailyROI, c.ProjectedTP1Pct, c.CompositeScore))
        sb.WriteString(fmt.Sprintf("  Score breakdown: funding=%.1f oi=%.1f btc=%.1f candle=%.1f vol=%.1f roi=%.1f volatility=%.1f\n",
            c.ScoreBreakdown.Funding, c.ScoreBreakdown.OI, c.ScoreBreakdown.BTC,
            c.ScoreBreakdown.Candle, c.ScoreBreakdown.Volume, c.ScoreBreakdown.ROI, c.ScoreBreakdown.Volatility))
        sb.WriteString(fmt.Sprintf("  RSI(14,15m): %.1f | RSI(7,5m): %.1f | OI delta 1h: +%.1f%% | OI delta 15m: +%.1f%%\n",
            c.RSI14_15m, c.RSI7_5m, c.OIDelta1h, c.OIDelta15m))
        sb.WriteString(fmt.Sprintf("  ATR ratio: %.1f | Vol change 5m: %.1f%% | Volume spike: %v | Momentum loss: %v\n",
            c.ATRRatio, c.VolChange5m, c.VolumeSpikeFlag, c.MomentumLoss))
        if len(c.CandlePatterns) > 0 {
            sb.WriteString("  Candle patterns: ")
            for _, cp := range c.CandlePatterns {
                sb.WriteString(fmt.Sprintf("[%s:%s(%s)] ", cp.Timeframe, cp.Pattern, cp.Strength))
            }
            sb.WriteString("\n")
        }
        sb.WriteString("\n")
    }

    sb.WriteString("Evaluate these candidates and provide your trade decision.")
    return sb.String()
}
```

### 7.4 Mapper (`internal/llm/mapper.go`)

```go
package llm

import (
    "math"
    "time"

    "futures/internal/domain"
    "futures/internal/scheduler"
)

func MapToLLMRequest(
    candidates []*domain.ScoredCandidate,
    btc *domain.BTCContext,
    tpPct float64,
) *LLMRequest {
    next := scheduler.NextFundingTime(time.Now().UTC())
    minutesTo := int(math.Round(time.Until(next).Minutes()))
    if minutesTo < 0 {
        minutesTo = 0
    }
    req := &LLMRequest{
        Timestamp:           time.Now().UTC().Unix(),
        MinutesToSettlement: minutesTo,
        Candidates:          make([]LLMCandidate, 0, len(candidates)),
    }

    // Map BTC context
    if btc != nil {
        req.BTCContext = LLMBTCContext{
            Trend:         btc.Trend,
            MomentumScore: btc.MomentumScore,
            Volatility:    btc.Volatility,
            IsBreakout:    btc.IsBreakout,
            RSI:           btc.RSI14_1h,
            PriceChange1h: btc.PriceChange1h,
        }
    }

    // Map candidates
    for _, sc := range candidates {
        fundingRatePct := sc.Candidate.FundingRate * 100
        projectedTP1 := math.Round((tpPct+math.Abs(fundingRatePct))*100) / 100

        c := LLMCandidate{
            Symbol:          sc.Candidate.Symbol,
            FundingRate:     fundingRatePct,
            DailyROI:        sc.Candidate.DailyROI,
            ProjectedTP1Pct: projectedTP1,
            CompositeScore:  sc.CompositeScore,
            ScoreBreakdown: LLMBreakdown{
                Funding:    sc.Breakdown.FundingScore,
                OI:         sc.Breakdown.OIScore,
                BTC:        sc.Breakdown.BTCScore,
                Candle:     sc.Breakdown.CandleScore,
                Volume:     sc.Breakdown.VolumeScore,
                ROI:        sc.Breakdown.ROIScore,
                Volatility: sc.Breakdown.VolatilityScore,
            },
            RSI14_15m:       sc.Indicators.RSI14_15m,
            RSI7_5m:         sc.Indicators.RSI7_5m,
            OIDelta1h:       sc.Indicators.OIDelta1h,
            OIDelta15m:      sc.Indicators.OIDelta15m,
            ATRRatio:        sc.Indicators.ATRRatio,
            VolChange5m:     sc.Indicators.VolChange5m,
            VolumeSpikeFlag: sc.Indicators.VolumeSpike,
            MomentumLoss:    sc.Indicators.MomentumLoss,
        }

        // Map candle patterns
        for _, sig := range sc.Indicators.Patterns {
            c.CandlePatterns = append(c.CandlePatterns, LLMCandleInfo{
                Timeframe: string(sig.Timeframe),
                Pattern:   string(sig.Pattern),
                Strength:  string(sig.Strength),
            })
        }

        req.Candidates = append(req.Candidates, c)
    }

    return req
}

func mapResponseToDecision(resp LLMResponse, candidates []*domain.ScoredCandidate) *domain.LLMDecision {
    d := &domain.LLMDecision{
        Action:       resp.Action,
        Symbol:       resp.Symbol,
        Confidence:   resp.Confidence,
        EntryReasons: resp.EntryReasons,
        Warnings:     resp.Warnings,
        SkipReason:   resp.SkipReason,
    }

    // Map entry mode
    switch resp.EntryMode {
    case "FRONTRUN":
        d.EntryMode = domain.EntryModeFrontrun
    case "LAST_MINUTE":
        d.EntryMode = domain.EntryModeLastMinute
    case "AFTER":
        d.EntryMode = domain.EntryModeAfter
    default:
        d.EntryMode = domain.EntryModeLastMinute
    }

    // Validate symbol exists in candidates
    if d.Action == "OPEN_SHORT" {
        found := false
        for _, sc := range candidates {
            if sc.Candidate.Symbol == d.Symbol {
                found = true
                break
            }
        }
        if !found {
            d.Action = "SKIP"
            d.SkipReason = "LLM selected unknown symbol"
        }
    }

    return d
}
```

---

## 8. Configuration

### Config Struct Addition

```go
// internal/config/config.go

type LLMConfig struct {
    Enabled                  bool   `yaml:"enabled"`
    APIKey                   string `yaml:"api_key"`
    Model                    string `yaml:"model"`
    TimeoutSecs              int    `yaml:"timeout_secs"`
    MaxRetries               int    `yaml:"max_retries"`
    MaxRPM                   int    `yaml:"max_rpm"`
    TopCandidates            int    `yaml:"top_candidates"`          // how many candidates to send to LLM
    MinConfidence            int    `yaml:"min_confidence"`          // below this → override to SKIP
    FrontrunExecIntervalSecs int    `yaml:"frontrun_exec_interval_secs"` // how often to fire best FRONTRUN intent (default 300 = 5m)
    CallCooldownSecs         int    `yaml:"call_cooldown_secs"`      // min gap between LLM calls with similar inputs (default 300 = 5m)
}
```

### config.yaml Addition

```yaml
llm:
  enabled: true
  api_key: "${OPENAI_API_KEY}"
  model: "gpt-4.1-mini"
  timeout_secs: 15
  max_retries: 1
  max_rpm: 30
  top_candidates: 5
  min_confidence: 60
  frontrun_exec_interval_secs: 300  # execute best FRONTRUN intent every 5m
  call_cooldown_secs: 300           # skip LLM call if inputs unchanged within this window

scheduler:
  scan_interval: "1m"         # uniform 1m (replaces early/late split)
  window_start_minutes: 30
```

### Defaults

```go
func setDefaults(cfg *Config) {
    // ... existing defaults ...

    // Phase 3: LLM defaults
    if cfg.LLM.Model == "" {
        cfg.LLM.Model = "gpt-4.1-mini"
    }
    if cfg.LLM.TimeoutSecs == 0 {
        cfg.LLM.TimeoutSecs = 15
    }
    if cfg.LLM.MaxRetries == 0 {
        cfg.LLM.MaxRetries = 1
    }
    if cfg.LLM.MaxRPM == 0 {
        cfg.LLM.MaxRPM = 30
    }
    if cfg.LLM.TopCandidates == 0 {
        cfg.LLM.TopCandidates = 5
    }
    if cfg.LLM.MinConfidence == 0 {
        cfg.LLM.MinConfidence = 60
    }
    if cfg.LLM.FrontrunExecIntervalSecs == 0 {
        cfg.LLM.FrontrunExecIntervalSecs = 300 // 5 minutes
    }
    if cfg.LLM.CallCooldownSecs == 0 {
        cfg.LLM.CallCooldownSecs = 300 // 5 minutes
    }

    // Phase 3: uniform scan interval
    if cfg.Scheduler.ScanInterval == "" {
        cfg.Scheduler.ScanInterval = "1m"
    }
}
```

---

## 9. Intent Queue (`internal/intent/`)

The intent queue is the bridge between LLM decisions and execution. It holds **multiple ranked pending intents** and fires them at the correct entry windows.

### Design

- **Multiple intents per cycle**: each LLM call can add an intent for a different symbol or entry mode. Intents are ranked by confidence descending.
- **Dedup per (symbol, entry_mode)**: if an intent for the same symbol+mode already exists, the new one replaces it only when its confidence is strictly higher.
- **One fire per window type per cycle**: FRONTRUN, LAST_MINUTE, and AFTER each fire at most once per funding cycle, independently. A FRONTRUN fire does not block LAST_MINUTE or AFTER.
- **FRONTRUN interval throttle**: FRONTRUN fires at most once per `frontrun_exec_interval_secs` (default 5m), to accumulate candidates before committing.

### 9.1 TradeIntent Type (`internal/intent/intent.go`)

```go
package intent

import (
    "time"

    "futures/internal/domain"
)

type IntentStatus string

const (
    IntentPending  IntentStatus = "PENDING"
    IntentFired    IntentStatus = "FIRED"
    IntentExpired  IntentStatus = "EXPIRED"
    IntentRejected IntentStatus = "REJECTED"
)

type TradeIntent struct {
    Symbol          string
    Candidate       *domain.ScoredCandidate
    Decision        *domain.LLMDecision
    TargetEntryMode domain.EntryMode // when to fire
    CreatedAt       time.Time
    ExpiresAt       time.Time        // auto-expire after funding settlement + 1m
    Status          IntentStatus
}

func (i *TradeIntent) IsExpired(now time.Time) bool {
    return now.After(i.ExpiresAt)
}
```

### 9.2 Intent Queue (`internal/intent/queue.go`)

Key fields:

```go
type Queue struct {
    mu               sync.Mutex
    intents          []*TradeIntent       // sorted by confidence descending
    execFn           ExecuteFn
    frontrunInterval time.Duration
    lastFrontrunExec time.Time
    firedInWindow    map[scheduler.WindowType]bool // one fire per window type per cycle
}
```

Key methods:

- **`Enqueue(intent)`**: dedup per (symbol, mode) — upgrade if higher confidence, discard otherwise; re-sort after change.
- **`Tick(ctx, currentWindow)`**: expire stale intents, then route to `tickFrontrunLocked` or `tickTransitionLocked`.
- **`tickFrontrunLocked`**: checks `firedInWindow[FRONTRUN]`, then interval elapsed, then fires best eligible. Sets `firedInWindow[FRONTRUN] = true`.
- **`tickTransitionLocked`**: checks `firedInWindow[currentWindow]`, fires best eligible, sets `firedInWindow[currentWindow] = true`.
- **`bestEligibleLocked(window)`**: first pending intent where `windowReady(current, targetMode)` — queue is sorted, so first match is highest confidence.
- **`ClearAfterSettlement`**: clears intents, resets `lastFrontrunExec`, resets `firedInWindow = make(...)`.

```go
func NewQueue(execFn ExecuteFn, frontrunInterval time.Duration) *Queue {
    return &Queue{
        execFn:           execFn,
        frontrunInterval: frontrunInterval,
        firedInWindow:    make(map[scheduler.WindowType]bool),
    }
}
```

---

## 10. Modified Scan Function (app.go)

The scanFn now produces intents instead of executing immediately. The queue fires them at the right time.

### Scheduler Change

Uniform 1m scan interval replaces the previous 5m/1m split:

```go
type SchedulerConfig struct {
    ScanInterval       string `yaml:"scan_interval"`        // "1m" (uniform)
    WindowStartMinutes int    `yaml:"window_start_minutes"` // 30
}
```

### Key app.go Fields

```go
// llmCallState tracks the last LLM call inputs for cooldown dedup.
type llmCallState struct {
    symbol   string
    score    float64
    btcTrend string
    window   scheduler.WindowType
    calledAt time.Time
}

type App struct {
    // ... existing fields ...
    llmEngine   *llm.DecisionEngine
    intentQueue *intent.Queue
    lastLLMCall *llmCallState
}
```

### Modified scanFn (Phase 3 path, LLM enabled)

```go
// Wire up intent queue — risk re-validated at fire time
frontrunInterval := time.Duration(cfg.LLM.FrontrunExecIntervalSecs) * time.Second
a.intentQueue = intent.NewQueue(func(ctx context.Context, sc *domain.ScoredCandidate, decision *domain.LLMDecision) error {
    btcAtFire, _ := a.indEngine.ComputeBTCContext(ctx)
    if err := a.riskEngine.EvaluateCandidate(ctx, sc, btcAtFire); err != nil {
        slog.Info("intent rejected by risk engine at fire time",
            "symbol", sc.Candidate.Symbol, "reason", err)
        return nil
    }
    return a.execEng.ExecuteScoredWithLLM(ctx, sc, decision)
}, frontrunInterval)

// ... inside scanFn, after scoring + sorting ...

// LLM call cooldown: skip if inputs haven't materially changed since last call.
// Window change always bypasses the cooldown.
btcTrend := ""
if btc != nil {
    btcTrend = btc.Trend
}
cooldown := time.Duration(cfg.LLM.CallCooldownSecs) * time.Second
if lc := a.lastLLMCall; lc != nil &&
    lc.window == window &&
    lc.symbol == top[0].Candidate.Symbol &&
    math.Abs(lc.score-top[0].CompositeScore) < 5 &&
    lc.btcTrend == btcTrend &&
    time.Since(lc.calledAt) < cooldown {
    return nil // inputs unchanged, existing queue intents still valid
}

// Call LLM — passes base tpPct; LLM decides entry_mode independently
decision, err := a.llmEngine.Evaluate(ctx, top, btc, a.cfg.Execution.TpPct)
if err != nil {
    slog.Error("LLM engine error", "error", err)
    return nil
}

// Record call state for next cooldown check
a.lastLLMCall = &llmCallState{
    symbol: top[0].Candidate.Symbol, score: top[0].CompositeScore,
    btcTrend: btcTrend, window: window, calledAt: time.Now(),
}

if decision.Action == "SKIP" {
    slog.Info("LLM decided to skip",
        "reason", decision.SkipReason,
        "queue_depth", a.intentQueue.PendingCount(),
    )
    return nil // existing queue intents are not cancelled on a SKIP
}

// Find selected candidate, size by confidence, enqueue
selected.PositionSizePct = confidenceToSize(decision.Confidence)
nextSettlement := scheduler.NextFundingTime(time.Now().UTC())
ti := &intent.TradeIntent{
    Symbol:          decision.Symbol,
    Candidate:       selected,
    Decision:        decision,
    TargetEntryMode: decision.EntryMode,
    CreatedAt:       time.Now(),
    ExpiresAt:       nextSettlement.Add(1 * time.Minute),
    Status:          intent.IntentPending,
}
a.intentQueue.Enqueue(ti)
```

### Intent Queue Tick (integrated into scheduler)

```go
func (s *Scheduler) tick(ctx context.Context) {
    window := CurrentWindow(time.Now(), s.cfg.WindowStartMinutes)

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
    // ... existing lock + scan logic ...
}
```

### confidenceToSize helper

```go
func confidenceToSize(confidence int) float64 {
    switch {
    case confidence >= 90: return 5.0
    case confidence >= 80: return 4.0
    case confidence >= 70: return 3.0
    case confidence >= 60: return 2.0
    default:               return 0
    }
}
```

---

## 10.1 Execution Engine Extension

New method to handle LLM-driven execution (called by intent queue's execFn):

```go
func (e *ExecutionEngine) ExecuteScoredWithLLM(
    ctx context.Context,
    sc *domain.ScoredCandidate,
    decision *domain.LLMDecision,
) error {
    windowType := entryModeToWindow(decision.EntryMode)
    return e.executeInternalLLM(ctx, sc.Candidate, windowType, sc.PositionSizePct, decision)
}
```

### Example Timeline

```
T-30m: Scan #1 → LLM called → OPEN_SHORT PEPE (LAST_MINUTE, conf=82), OPEN_SHORT WIF (AFTER, conf=75)
        → Queue: [{PEPE,LM,82}, {WIF,AFTER,75}]

T-29m: Scan #2 → cooldown active (same symbol/score/BTC/window) → skip LLM, queue unchanged

T-25m: Scan #6 → BTC trend changed to bullish → cooldown bypassed → LLM called
        → LLM: OPEN_SHORT PEPE (LAST_MINUTE, conf=78) [same symbol → always replaces]
        → LLM: OPEN_SHORT DOGE (FRONTRUN, conf=70) [new symbol, added]
        → Queue: [{PEPE,LM,78}, {WIF,AFTER,75}, {DOGE,FR,70}]

T-15m: FRONTRUN interval (5m) elapsed
        → Queue tick fires DOGE (conf=70, highest FRONTRUN-eligible)
        → Risk check → execute DOGE short
        → firedInWindow[FRONTRUN] = true

T-5m:  Window → LAST_MINUTE  (scans continue down to T-3m, then stop)
        → Queue tick fires PEPE (conf=78, highest LAST_MINUTE-eligible)
        → Risk check → execute PEPE short
        → firedInWindow[LAST_MINUTE] = true

T-3m:  Scan gate: scheduler skips scanFn; intent queue still ticks

T+0:   Window → AFTER
        → Queue tick fires WIF (conf=75, highest AFTER-eligible)
        → Risk check → execute WIF short
        → firedInWindow[AFTER] = true

T+1m:  ClearAfterSettlement → queue emptied, firedInWindow reset
```

---

## 11. Database Migration

```sql
-- migrations/004_add_llm_fields_to_trades.up.sql

ALTER TABLE trades ADD COLUMN IF NOT EXISTS llm_confidence INT;
ALTER TABLE trades ADD COLUMN IF NOT EXISTS llm_entry_mode VARCHAR(16);
ALTER TABLE trades ADD COLUMN IF NOT EXISTS llm_tp_strategy VARCHAR(16);
ALTER TABLE trades ADD COLUMN IF NOT EXISTS llm_entry_reasons TEXT[];
ALTER TABLE trades ADD COLUMN IF NOT EXISTS llm_warnings TEXT[];
ALTER TABLE trades ADD COLUMN IF NOT EXISTS llm_skip_reason TEXT;
```

```sql
-- migrations/004_add_llm_fields_to_trades.down.sql

ALTER TABLE trades DROP COLUMN IF EXISTS llm_confidence;
ALTER TABLE trades DROP COLUMN IF EXISTS llm_entry_mode;
ALTER TABLE trades DROP COLUMN IF EXISTS llm_tp_strategy;
ALTER TABLE trades DROP COLUMN IF EXISTS llm_entry_reasons;
ALTER TABLE trades DROP COLUMN IF EXISTS llm_warnings;
ALTER TABLE trades DROP COLUMN IF EXISTS llm_skip_reason;
```

---

## 12. OpenAI Structured Output JSON Schema

Used in the API call to enforce response format:

```json
{
  "name": "trade_decision",
  "strict": true,
  "schema": {
    "type": "object",
    "properties": {
      "action": {
        "type": "string",
        "enum": ["OPEN_SHORT", "SKIP"]
      },
      "symbol": {
        "type": "string",
        "description": "Selected candidate symbol. Empty string if SKIP."
      },
      "confidence": {
        "type": "integer",
        "description": "Confidence score 0-100. Must be >= 60 for OPEN_SHORT."
      },
      "entry_mode": {
        "type": "string",
        "enum": ["FRONTRUN", "LAST_MINUTE", "AFTER"]
      },
      "tp_strategy": {
        "type": "string",
        "enum": ["BASE", "AGGRESSIVE", "TRAILING"]
      },
      "entry_reasons": {
        "type": "array",
        "items": { "type": "string" },
        "description": "2-4 concise reasons for the decision"
      },
      "warnings": {
        "type": "array",
        "items": { "type": "string" },
        "description": "0-3 risk warnings"
      },
      "skip_reason": {
        "type": "string",
        "description": "Reason for skipping. Empty string if OPEN_SHORT."
      }
    },
    "required": ["action", "symbol", "confidence", "entry_mode", "tp_strategy", "entry_reasons", "warnings", "skip_reason"],
    "additionalProperties": false
  }
}
```

---

## 13. Error Handling & Edge Cases

| Scenario                          | Behavior                                           |
|-----------------------------------|----------------------------------------------------|
| LLM timeout (>15s)                | Return SKIP, log warning                           |
| LLM API error (500, 429)          | Retry once (5s timeout), then SKIP                 |
| LLM returns invalid JSON          | SKIP, log raw response for debugging               |
| LLM returns symbol not in list    | SKIP, log mismatch                                 |
| LLM confidence < 60 but OPEN      | Override to SKIP                                   |
| LLM entry_mode is future window   | Queue intent, fire when window arrives             |
| LLM says SKIP                      | Existing queued intents are NOT cancelled; SKIP only means no new intent added |
| Intent expires (past settlement)  | Auto-clear, log expiry                             |
| Market changed since intent queued| Risk check at fire time catches this               |
| Rate limit exceeded locally       | SKIP this cycle, resume next cycle                 |
| `llm.enabled: false`              | Use Phase 2 deterministic scoring (backward compat)|
| OPENAI_API_KEY not set            | Disable LLM at startup, warn in logs               |
| All candidates score < 50         | Still send to LLM (it will likely SKIP)            |
| Only 1 candidate after filters    | Send 1 to LLM (still valid)                       |

---

## 14. Logging & Observability

Every LLM call should log:

```go
slog.Info("llm_call",
    "window", window,
    "candidates_count", len(candidates),
    "latency_ms", elapsed.Milliseconds(),
    "action", decision.Action,
    "symbol", decision.Symbol,
    "confidence", decision.Confidence,
    "entry_mode", decision.EntryMode,
    "tp_strategy", decision.TPStrategy,
    "tokens_used", usage.TotalTokens,
)
```

On SKIP:
```go
slog.Info("llm_skip",
    "reason", decision.SkipReason,
    "candidates", symbolList(candidates),
)
```

---

## 15. Testing Strategy

### Unit Tests — LLM

| Test                                | What it validates                                   |
|-------------------------------------|-----------------------------------------------------|
| `TestMapToLLMRequest`               | Domain types correctly mapped to LLM schema         |
| `TestMapResponseToDecision`         | LLM response correctly mapped to domain decision    |
| `TestConfidenceToSize`              | Confidence brackets map to correct position sizes   |
| `TestPromptBuilder`                 | Prompt renders correctly with sample data           |
| `TestDecisionEngine_LLMFailure`     | Returns SKIP on API error                           |
| `TestDecisionEngine_InvalidSymbol`  | Returns SKIP when LLM hallucinates a symbol         |
| `TestDecisionEngine_LowConfidence`  | Overrides to SKIP when confidence < 60              |

### Unit Tests — Intent Queue

| Test                                          | What it validates                                              |
|-----------------------------------------------|----------------------------------------------------------------|
| `TestEnqueue_AddsNewIntent`                   | Intent is stored and count increases                           |
| `TestEnqueue_SortsByConfidenceDesc`           | Queue is always sorted highest confidence first                |
| `TestEnqueue_SameSymbolSameMode_Upgrade`      | Higher confidence replaces lower for same (symbol, mode)       |
| `TestEnqueue_SameSymbolSameMode_Discard`      | Lower/equal confidence is discarded                            |
| `TestEnqueue_SameSymbolDifferentMode`         | Different modes for same symbol both kept                      |
| `TestTick_Frontrun_FiresAfterInterval`        | FRONTRUN intent fires after frontrun_exec_interval elapses     |
| `TestTick_Frontrun_DoesNotFireBeforeInterval` | FRONTRUN intent held until interval elapsed                    |
| `TestTick_Frontrun_PicksHighestConfidence`    | Highest-confidence eligible intent fires first                 |
| `TestTick_LastMinute_FiresOnWindowEntry`      | LAST_MINUTE intent fires on first LAST_MINUTE tick             |
| `TestTick_LastMinute_FiresOnlyOnce`           | Second LAST_MINUTE tick does not fire again                    |
| `TestTick_FrontrunFire_DoesNotBlockLM`        | FRONTRUN fire does not prevent LAST_MINUTE or AFTER firing     |
| `TestTick_LastMinuteFire_DoesNotBlockAfter`   | LAST_MINUTE fire does not prevent AFTER from firing            |
| `TestTick_OneFirePerWindowTypePerCycle`       | Each window type fires at most once; up to 3 trades per cycle  |
| `TestTick_ExpiredIntentNotFired`              | Expired intent is removed, not fired                           |
| `TestClearAfterSettlement_ResetsWindowFlags`  | firedInWindow is fully reset after settlement                  |
| `TestWindowReady_AllCombinations`             | All window/mode combinations produce correct result            |

### Integration Tests (with mock HTTP server)

| Test                                | What it validates                                   |
|-------------------------------------|-----------------------------------------------------|
| `TestDecisionEngine_FullFlow`       | Complete pipeline from candidates → decision        |
| `TestDecisionEngine_Timeout`        | Proper fallback on simulated timeout                |
| `TestDecisionEngine_RateLimit`      | Rate limiter prevents excess calls                  |
| `TestIntentQueue_EndToEnd`          | LLM → queue → wait → fire → execute                |

### Manual Validation

- Run in paper mode with `llm.enabled: true`
- Monitor logs for LLM decisions, intent lifecycle, and fire events
- Verify intent holds correctly (LLM says AFTER, intent waits until T+0)
- Verify dedup works (repeated scans don't produce redundant LLM calls)
- Compare win rates: LLM-selected trades vs score-threshold trades (backtester)

---

## 16. Phase 4 Integration Points (Future)

The LLM request schema already has a placeholder for similar past trades:

```go
// Will be added in Phase 4
type LLMSimilarTrade struct {
    Outcome     string  `json:"outcome"`
    ProfitPct   float64 `json:"profit_pct"`
    Similarity  float64 `json:"similarity"`
    KeyFactors  string  `json:"key_factors"`
}
```

Phase 4 will:
1. After scoring, query pgvector for top 3-5 similar historical trades
2. Embed: `funding_rate + roi + oi_delta + btc_trend + candle_pattern + atr_ratio`
3. Inject summarized lessons into the user message:
   ```
   === SIMILAR PAST TRADES ===
   [1] 92% similar | WIN +3.2% | Key: "extreme funding + OI exhaustion preceded dump"
   [2] 87% similar | LOSS -1.8% | Key: "BTC pumped 2% during position, stop triggered"
   [3] 85% similar | WIN +2.1% | Key: "waited for after-settlement confirmation worked better"
   ```
4. Add to system prompt: "Use similar past trades to calibrate confidence. If most similar setups lost, reduce confidence or SKIP."

---

## 17. Implementation Checklist

| # | Task                                                  | Dependencies |
|---|-------------------------------------------------------|--------------|
| 1 | Add `openai-go` to go.mod                             | None         |
| 2 | Add `LLMConfig` to config struct + defaults           | None         |
| 3 | Update `SchedulerConfig` to uniform `scan_interval`   | None         |
| 4 | Create `internal/domain/llm_decision.go`              | None         |
| 5 | Create `internal/llm/schema.go`                       | None         |
| 6 | Create `internal/llm/client.go`                       | #2           |
| 7 | Create `internal/llm/prompt.go`                       | #5           |
| 8 | Create `internal/llm/mapper.go`                       | #4, #5       |
| 9 | Create `internal/llm/decision_engine.go`              | #6, #7, #8   |
| 10| Create `internal/intent/intent.go`                    | #4           |
| 11| Create `internal/intent/queue.go`                     | #10          |
| 12| Add `ExecuteScoredWithLLM` to execution engine        | #4           |
| 13| Modify scheduler for uniform 1m + intent queue tick   | #3, #11      |
| 14| Modify `app.go`: wire LLM engine + intent queue       | #9, #11, #12 |
| 15| Add migration `004_add_llm_fields`                    | None         |
| 16| Update trade repo to persist LLM fields               | #15          |
| 17| Write unit tests (llm + intent queue)                 | #9, #11      |
| 18| Write integration tests (mock server)                 | #9           |
| 19| End-to-end paper trading test                         | All          |

---

## 18. Dependency Addition

```bash
go get github.com/openai/openai-go
```

---

## 19. Summary

Phase 3 introduces an LLM decision layer + intent queue between the scoring pipeline and execution:

- **Scan:** Uniform 1m interval throughout T-30m → T+1m (no more 5m/1m split)
- **LLM:** Evaluates top 3-5 candidates, returns OPEN_SHORT or SKIP
- **Intent Queue:** Holds trade intent until target entry window arrives, then fires
- **Dedup:** Skips redundant LLM calls when candidates/conditions haven't shifted
- **Model:** GPT-4.1-mini (fast, cheap, excellent at structured JSON)
- **Fallback:** Any LLM failure → SKIP (no trade is safer than blind trade)
- **Risk at fire time:** Re-validates market conditions when intent actually fires
- **Backward compatible:** `llm.enabled: false` falls back to Phase 2 deterministic scoring
- **Phase 4 ready:** Schema already supports `similar_past_trades` injection
