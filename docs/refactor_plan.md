# Futures Bot — Refactor Plan

## Overview

The codebase grew through 8 incremental phases and the structure shows it. The three main problems are: `PositionManager` has become a god object (~1,255 lines with 8 distinct responsibilities), state for a single position is split across Redis, Binance live, the trade repo, and WS events with no clear owner, and `scan.go` mixes risk filtering, indicator compute, LLM calling, memory retrieval, cooldown state, and intent enqueueing in one function. The plan below fixes these in four independently-shippable phases, from most dangerous to least.

---

## Guiding Principles

- **No rewrites, only splits and extractions.** Every refactor item moves existing logic, not replaces it.
- **Each phase ships.** The bot must compile and run correctly after every phase. No half-finished state.
- **Don't touch what works.** Market data ingestion, scoring weights, LLM client/schema, memory embedder — these are healthy. Leave them alone.
- **Fix state ownership first.** Phase 1 tackles the race-prone parts before anything else is split out.
- **Parallel items within a phase are marked.** Only sequential items have blocking dependencies.

---

## Phase 1 — Fix Dangerous State Races (Safety)

**Goal:** No position can be left unprotected due to a partial failure or concurrent state write. Every mutation to a position goes through one owner.

| # | Title | Key Files | Size | Parallel? |
|---|-------|-----------|------|-----------|
| 1.1 | Add per-position mutex | `execution/position_manager.go` | S | — |
| 1.2 | Fix live executor hardcoded balance | `execution/live_executor.go` | S | Yes |
| 1.3 | Propagate protection placement errors | `execution/position_manager.go` | M | — |
| 1.4 | Validate SL/TP prices in reconciler | `execution/reconciler.go` | M | Yes |

### Items

#### 1.1 — Add per-position mutex
**Action:** Add a `sync.Mutex` per `ActivePosition` (or a `sync.Map[symbol → *sync.Mutex]` in `PositionManager`). Acquire it in both `check()` and `HandleUserDataEvent()` before reading or writing position state.  
**Why:** Both the 1-second tick loop and WS fill events can race on the same position — e.g., a TP1 fill arrives while `check()` is running breakeven logic. Currently nothing prevents this.  
**Files modified:** `execution/position_manager.go`  
**Migration note:** None — internal change only.

#### 1.2 — Fix live executor hardcoded balance
**Action:** Replace the hardcoded `return 100.0` at `live_executor.go:169` with a real `GetAccountBalance()` call to Binance REST.  
**Why:** The current code will silently under-size every position in live mode.  
**Files modified:** `execution/live_executor.go`  
**Migration note:** `Executor` interface may need a `GetBalance(ctx) (float64, error)` — check paper executor implements it too.

#### 1.3 — Propagate protection placement errors
**Action:** In `finalizeSLTP`, if either the SL or TP order placement fails, cancel the other order and return an error. Currently errors are logged but the function continues — this is how a position ends up with SL but no TP.  
**Why:** Partial order placement is the single most dangerous failure mode in the whole system.  
**Files modified:** `execution/position_manager.go`  
**Depends on:** 1.1 (must hold position lock during finalize)

#### 1.4 — Validate SL/TP prices in reconciler
**Action:** In `Reconcile` Case 1 and Case 3, after fetching live Binance orders, verify that the existing SL/TP prices match what the system would compute from the current fill price. If they diverge by more than 0.1%, cancel and re-place.  
**Why:** A restart after a partial placement leaves prices that don't match the config. Reconciler currently re-places missing orders but doesn't validate prices on existing ones.  
**Files modified:** `execution/reconciler.go`

---

## Phase 2 — Split PositionManager (Structural)

**Goal:** `PositionManager` is a coordinator, not an implementer. Each sub-concern lives in its own type with a clear interface.

| # | Title | Key Files | Size | Parallel? |
|---|-------|-----------|------|-----------|
| 2.1 | Extract `ProtectionManager` | new `execution/protection_manager.go` | M | — |
| 2.2 | Extract `ExitHandler` | new `execution/exit_handler.go` | M | Yes |
| 2.3 | Extract `PaperSimulator` | new `execution/paper_simulator.go` | M | Yes |
| 2.4 | Extract `PreSettlementChecker` | new `execution/pre_settlement.go` | S | Yes |
| 2.5 | Extract `BreakevenTrailer` | new `execution/breakeven_trailer.go` | S | Yes |

### Items

#### 2.1 — Extract `ProtectionManager`
**Action:** Move SL/TP retry logic (the `retryProtection()` function and `PendingProtection` cache handling) into a new `ProtectionManager` struct. `PositionManager.check()` calls `protMgr.Tick(pos)`.

```go
type ProtectionManager interface {
    Tick(ctx context.Context, pos *ActivePosition) error
}
```

**Files modified:** `execution/position_manager.go`  
**Files created:** `execution/protection_manager.go`

#### 2.2 — Extract `ExitHandler`
**Action:** Move `handleTP1Fill()`, `handleTrailingFill()`, `handleTrailingSLFill()`, `handleHardSLFill()`, and `persistClose()` into a new `ExitHandler` struct. `HandleUserDataEvent()` delegates to it.  
**Files modified:** `execution/position_manager.go`  
**Files created:** `execution/exit_handler.go`  
**Depends on:** 2.1 (ExitHandler needs to call ProtectionManager.Cancel on exit)

#### 2.3 — Extract `PaperSimulator`
**Action:** Move `checkPaperFills()` and all paper-mode fill simulation logic into a `PaperSimulator`. Guard its call with `if a.cfg.App.Mode == "paper"` at the `PositionManager` level — the simulator doesn't exist at all in live mode.  
**Files modified:** `execution/position_manager.go`  
**Files created:** `execution/paper_simulator.go`

#### 2.4 — Extract `PreSettlementChecker`
**Action:** The pre-settlement goroutine (`RunPreSettlementChecker`) and its emergency close / TP-widening logic already live in `position_manager.go`. Move it to a standalone type.  
**Files modified:** `execution/position_manager.go`, `internal/app/app.go`  
**Files created:** `execution/pre_settlement.go`

#### 2.5 — Extract `BreakevenTrailer`
**Action:** Move the breakeven and trailing stop update logic (the 6-min + 2% check, the `highSinceEntry` tracking) into a `BreakevenTrailer` that `check()` calls.  
**Files modified:** `execution/position_manager.go`  
**Files created:** `execution/breakeven_trailer.go`

**After Phase 2:** `PositionManager` becomes a ~200-line coordinator. Each extracted type is independently testable.

---

## Phase 3 — Clean Up scan.go (Pipeline Clarity)

**Goal:** `scanFn` reads as a pipeline — each stage has a name, a single responsibility, and passes a typed result to the next stage.

| # | Title | Key Files | Size | Parallel? |
|---|-------|-----------|------|-----------|
| 3.1 | Extract `ScanPipeline` struct | new `internal/app/scan_pipeline.go` | M | — |
| 3.2 | Move LLM cooldown state into `DecisionEngine` | `internal/llm/decision_engine.go`, `internal/app/app.go` | S | Yes |
| 3.3 | Move confidence→size mapping into `scoring` | `internal/scoring/scorer.go`, `internal/app/helpers.go` | S | Yes |

### Items

#### 3.1 — Extract `ScanPipeline`
**Action:** Move the body of `scanFn` into a `ScanPipeline` struct with named methods: `filterCandidates()`, `computeIndicators()`, `evaluateLLM()`, `enqueueIntent()`. `scanFn` becomes 5 lines that call these in order.

```go
type ScanPipeline struct {
    scanner    *scanner.FundingScanner
    indEngine  *indicator.Engine
    scorer     *scoring.Scorer
    llmEngine  *llm.DecisionEngine
    riskEngine *risk.Engine
    memory     *memory.Engine
    queue      *intent.Queue
    cfg        *config.Config
}
```

**Why:** Currently `scanFn` is 245 lines with 6 responsibilities. Extracting it makes each stage independently testable and the flow readable.  
**Files modified:** `internal/app/scan.go`, `internal/app/app.go`  
**Files created:** `internal/app/scan_pipeline.go`

#### 3.2 — Move LLM cooldown state into `DecisionEngine`
**Action:** Move the `llmCallState` struct and the "skip if inputs unchanged" check from `app.go` / `scan.go` into `DecisionEngine`. The engine tracks its own last call and returns a cached `SKIP` decision when inputs are identical within cooldown.  
**Why:** Cooldown is LLM engine behavior, not app orchestration behavior. It currently forces `App` to carry state that belongs elsewhere.  
**Files modified:** `internal/llm/decision_engine.go`, `internal/app/app.go`, `internal/app/scan.go`

#### 3.3 — Move `confidenceToSize` into scoring package
**Action:** Move `confidenceToSize()` from `internal/app/helpers.go` into `internal/scoring/scorer.go` as a package-level function, or better, attach it to `ScoringConfig` so the thresholds come from the DB config.  
**Why:** It's a scoring rule — it belongs in scoring, not in app. The thresholds are currently hardcoded in `helpers.go`.  
**Files modified:** `internal/app/helpers.go`, `internal/app/scan.go`, `internal/scoring/scorer.go`

---

## Phase 4 — Fix Scattered Concepts (Cohesion)

**Goal:** Funding rate semantics, confidence models, and prompt strategy live in one place each.

| # | Title | Key Files | Size | Parallel? |
|---|-------|-----------|------|-----------|
| 4.1 | Create `FundingRate` value type | new `internal/domain/funding.go` | S | Yes |
| 4.2 | Unify confidence models | `internal/scoring/scorer.go`, `internal/llm/decision_engine.go` | S | Yes |
| 4.3 | Externalize LLM prompt to config | `internal/llm/prompt.go`, config YAML | M | Yes |
| 4.4 | Add `PurgePolicy` to market caches | `internal/market/engine.go` | S | Yes |

### Items

#### 4.1 — Create `FundingRate` value type
**Action:** Add `type FundingRate float64` to `internal/domain/funding.go` with methods `Pct() float64`, `Bucket() int` (1/2/3 severity), `String() string`. Replace raw `float64` funding fields in `Candidate`, `LLMCandidate`, `TradeMemory` with this type.  
**Why:** Funding rate is converted between decimal and percent in at least 5 places (mapper, memory, summarizer, prompt, scanner filter). A value type eliminates the ambiguity.  
**Files modified:** `internal/domain/`, `internal/llm/mapper.go`, `internal/memory/feature_builder.go`, `internal/memory/summarizer.go`  
**Files created:** `internal/domain/funding.go`  
**Migration note:** Will touch function signatures in mapper and memory — audit all callers.

#### 4.2 — Unify confidence models
**Action:** The scorer produces a `ConfidenceTier` enum; the LLM produces an integer 0–100. The threshold for acting on LLM confidence is hardcoded at 60 in `decision_engine.go`. Move that threshold into `ScoringConfig.BotParams` so it reloads from DB every 60 minutes alongside everything else.  
**Files modified:** `internal/llm/decision_engine.go`, `internal/config/config.go`, `internal/domain/`

#### 4.3 — Externalize LLM prompt to config
**Action:** Move the hardcoded 158-line system prompt in `prompt.go` to a text file loaded at startup (e.g., `config/prompts/decision.txt`). Version it with a hash so you can detect prompt drift.  
**Why:** Right now changing a single line in the strategy prompt requires a recompile and redeploy. Externalizing makes A/B testing prompts possible.  
**Files modified:** `internal/llm/prompt.go`  
**Files created:** `config/prompts/decision.txt`, `config/prompts/force_sl.txt`, `config/prompts/summarizer.txt`

#### 4.4 — Add explicit `PurgePolicy` to market engine
**Action:** Give each cache type in `MarketEngine` an explicit `ShouldPurge() bool` flag. Document why funding and ticker are preserved on purge. Add a test that verifies purge behavior per cache.  
**Files modified:** `internal/market/engine.go`

---

## Non-Goals

- No changes to the exchange/WS layer (`stream_router`, `binance_client`, `binance_ws`). These are stable.
- No changes to scoring weights or LLM client/schema. They work correctly.
- No new features. This plan is purely structural.
- No database schema changes.
- No new external dependencies.
- No dashboard changes.

---

## Risk Register

| Risk | Likelihood | Mitigation |
|------|-----------|------------|
| Phase 2 split breaks position event ordering | Medium | Add integration test covering the full fill event sequence (entry → TP1 → trailing → close) before starting Phase 2 |
| Phase 1.3 over-cancels orders on transient API errors | Medium | Use retry with backoff before treating placement as failed; only cancel counterpart on non-retryable errors |
| Phase 3.2 cooldown-in-engine changes scan behavior | Low | The logic is identical — just moves; add a unit test before moving |
| Phase 4.1 `FundingRate` type migration breaks scanner filter | Low | The scanner filter uses raw float; type methods must expose the same comparison semantics |
| Phase 1.1 per-position mutex introduces deadlock | Low | Lock only one position at a time; never hold position lock while calling external APIs |

---

## Recommended Sequencing

Do **Phase 1** before shipping anything to live — the race conditions and hardcoded balance are production bugs, not refactor items.

**Phase 2** and **Phase 3** can be done in parallel by two people once Phase 1 is merged.

**Phase 4** is polish — do it after the structural work from Phases 2 and 3 is stable and running cleanly.
