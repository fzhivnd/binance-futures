package memory

import (
	"context"
	"sort"
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

const statsPoolSize = 200
const statsMinSimilarity = 0.55

// FindSimilar retrieves the most similar historical trade setups.
// It fetches a large pool (statsPoolSize) from the DB for winrate aggregation, scores
// all results with structural bonuses/penalties, then returns:
//   - the top TopSimilar trades as detail for the LLM prompt
//   - per-mode win/loss counts computed from the full scored pool
func (r *Retriever) FindSimilar(
	ctx context.Context,
	embedding pgvector.Vector,
	snap *domain.IndicatorSnapshot,
	btc *domain.BTCContext,
	candidate *domain.Candidate,
	at time.Time,
) ([]domain.SimilarTrade, map[string]domain.ModeWinRate, error) {
	results, err := r.repo.FindSimilar(ctx, embedding, statsPoolSize)
	if err != nil {
		return nil, nil, err
	}

	btcRegime := BTCRegime(btc.Trend, btc.IsBreakout, btc.MomentumScore)
	fundingBucket := FundingBucket(candidate.FundingRate * 100)
	roiBucket := ROIBucket(candidate.DailyROI)
	dayOfWeek := DayOfWeekLabel(at)
	fundingWindow := FundingWindowLabel(at)
	atrBucket := ATRBucket(snap.ATRRatio)

	now := time.Now()
	var scored []domain.SimilarTrade

	for _, res := range results {
		if res.Memory.Outcome == "" {
			continue
		}

		// Cosine similarity is the base; structural bonuses/penalties push
		// same-regime setups up and cross-regime setups down in ranking.
		sim := res.Similarity * 0.60

		if res.Memory.FundingBucket == fundingBucket {
			sim += 0.1
		}
		if ROIBucket(res.Memory.DailyROI) == roiBucket {
			sim += 0.08
		}
		if DayOfWeekLabel(res.Memory.CreatedAt) == dayOfWeek {
			sim += 0.01
		}
		if FundingWindowLabel(res.Memory.CreatedAt) == fundingWindow {
			sim += 0.015
		}
		if ATRBucket(res.Memory.ATRRatio) == atrBucket {
			sim += 0.06
		}
		if RSIBucket(res.Memory.RSI14_15m) == RSIBucket(snap.RSI14_15m) {
			sim += 0.05
		}
		if RSIBucket(res.Memory.RSI7_5m) == RSIBucket(snap.RSI7_5m) {
			sim += 0.03
		}
		if OIDeltaBucket(res.Memory.OIDelta1h) == OIDeltaBucket(snap.OIDelta1h) {
			sim += 0.05
		}
		if OIDeltaBucket(res.Memory.OIDelta15m) == OIDeltaBucket(snap.OIDelta15m) {
			sim += 0.03
		}
		if res.Memory.BTCRegime != btcRegime {
			sim -= 0.05
		}

		if sim < r.cfg.MinSimilarity {
			continue
		}

		daysAgo := int(now.Sub(res.Memory.CreatedAt).Hours() / 24)
		scored = append(scored, domain.SimilarTrade{
			Outcome:     res.Memory.Outcome,
			ProfitPct:   res.Memory.ProfitPct,
			Similarity:  sim,
			Lesson:      res.Memory.Lesson,
			DaysAgo:     daysAgo,
			EntryMode:   res.Memory.EntryMode,
			FundingRate: res.Memory.FundingRate,
		})
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Similarity > scored[j].Similarity
	})

	// Compute per-mode winrates from the high-confidence subset only.
	winRates := computeModeWinRates(scored, statsMinSimilarity)

	// Trim to top N for LLM detail block.
	detail := scored
	if len(detail) > r.cfg.TopSimilar {
		detail = detail[:r.cfg.TopSimilar]
	}

	return detail, winRates, nil
}

func computeModeWinRates(trades []domain.SimilarTrade, minSimilarity float64) map[string]domain.ModeWinRate {
	stats := map[string]*domain.ModeWinRate{}
	for _, t := range trades {
		if t.Similarity < minSimilarity {
			continue
		}
		mode := t.EntryMode
		if mode == "" {
			continue
		}
		if stats[mode] == nil {
			stats[mode] = &domain.ModeWinRate{}
		}
		s := stats[mode]
		s.Total++
		switch t.Outcome {
		case "WIN", "PARTIAL_WIN", "SKIP_MISSED":
			s.Wins++
		case "LOSS", "FORCE_SL", "SKIP_VALIDATED":
			s.Losses++
		}
	}
	out := make(map[string]domain.ModeWinRate, len(stats))
	for k, v := range stats {
		out[k] = *v
	}
	return out
}
