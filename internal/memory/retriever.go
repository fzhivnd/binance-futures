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
// The DB returns a wider pool (limit*3) ordered by raw cosine distance; we apply
// multiplicative bonuses for the four priority match fields (symbol, entryMode,
// fundingBucket, roiBucket) and a regime penalty for BTC regime mismatch.
func (r *Retriever) FindSimilar(
	ctx context.Context,
	embedding pgvector.Vector,
	symbol string,
	entryMode string,
	btcRegime string,
	fundingBucket int,
	roiBucket string,
) ([]domain.SimilarTrade, error) {
	results, err := r.repo.FindSimilar(ctx, embedding, r.cfg.TopSimilar)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	var similar []domain.SimilarTrade

	for _, res := range results {
		if res.Memory.Outcome == "" {
			continue
		}

		// Weighted composite score — cosine is one component, not the whole score.
		// This prevents saturated cosine (0.99+) from collapsing all scores to 1.0.
		sim := res.Similarity * 0.50

		if res.Memory.Symbol == symbol {
			sim += 0.20
		}
		if res.Memory.EntryMode == entryMode {
			sim += 0.15
		}
		if res.Memory.FundingBucket == fundingBucket {
			sim += 0.08
		}
		if ROIBucket(res.Memory.DailyROI) == roiBucket {
			sim += 0.07
		}
		if res.Memory.BTCRegime != btcRegime {
			sim -= 0.05
		}

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

		if len(similar) == r.cfg.TopSimilar {
			break
		}
	}

	return similar, nil
}
