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

// FindSimilar retrieves the most similar historical trade setups.
// The DB returns a wider pool (limit*3) ordered by raw cosine distance; we apply
// bonuses for priority match fields and a penalty for BTC regime mismatch.
func (r *Retriever) FindSimilar(
	ctx context.Context,
	embedding pgvector.Vector,
	snap *domain.IndicatorSnapshot,
	btc *domain.BTCContext,
	candidate *domain.Candidate,
	entryMode string,
	at time.Time,
) ([]domain.SimilarTrade, error) {
	results, err := r.repo.FindSimilar(ctx, embedding, r.cfg.TopSimilar)
	if err != nil {
		return nil, err
	}

	_ = BTCRegime(btc.Trend, btc.IsBreakout, btc.MomentumScore)
	_ = FundingBucket(candidate.FundingRate * 100)
	_ = ROIBucket(candidate.DailyROI)
	_ = DayOfWeekLabel(at)
	_ = FundingWindowLabel(at)
	_ = ATRBucket(snap.ATRRatio)

	now := time.Now()
	var similar []domain.SimilarTrade

	for _, res := range results {
		if res.Memory.Outcome == "" {
			continue
		}

		sim := res.Similarity

		// Weighted composite score — cosine is one component, not the whole score.
		// This prevents saturated cosine (0.99+) from collapsing all scores to 1.0.
		// sim := res.Similarity * 0.50

		// if res.Memory.EntryMode == entryMode {
		// 	sim += 0.15
		// }
		// if res.Memory.FundingBucket == fundingBucket {
		// 	sim += 0.08
		// }
		// if ROIBucket(res.Memory.DailyROI) == roiBucket {
		// 	sim += 0.07
		// }
		// if DayOfWeekLabel(res.Memory.CreatedAt) == dayOfWeek {
		// 	sim += 0.05
		// }
		// if FundingWindowLabel(res.Memory.CreatedAt) == fundingWindow {
		// 	sim += 0.07
		// }
		// if ATRBucket(res.Memory.ATRRatio) == atrBucket {
		// 	sim += 0.07
		// }
		// if res.Memory.BTCRegime != btcRegime {
		// 	sim -= 0.05
		// }

		if sim < r.cfg.MinSimilarity {
			continue
		}

		daysAgo := int(now.Sub(res.Memory.CreatedAt).Hours() / 24)
		similar = append(similar, domain.SimilarTrade{
			Outcome:     res.Memory.Outcome,
			ProfitPct:   res.Memory.ProfitPct,
			Similarity:  sim,
			Lesson:      res.Memory.Lesson,
			DaysAgo:     daysAgo,
			EntryMode:   res.Memory.EntryMode,
			FundingRate: res.Memory.FundingRate,
		})
	}

	sort.Slice(similar, func(i, j int) bool {
		return similar[i].Similarity > similar[j].Similarity
	})

	if len(similar) > r.cfg.TopSimilar {
		similar = similar[:r.cfg.TopSimilar]
	}

	return similar, nil
}
