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
// soft downranking for regime/bucket mismatch here so the vector search can
// surface genuinely dissimilar records for contrast rather than only same-cluster results.
func (r *Retriever) FindSimilar(
	ctx context.Context,
	embedding pgvector.Vector,
	btcRegime string,
	fundingBucket int,
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

		sim := res.Similarity

		// Soft penalty for regime mismatch: -0.05 per degree of difference.
		if res.Memory.BTCRegime != btcRegime {
			sim -= 0.05
		}
		// Soft penalty for funding bucket distance beyond adjacent.
		bucketDist := res.Memory.FundingBucket - fundingBucket
		if bucketDist < 0 {
			bucketDist = -bucketDist
		}
		if bucketDist > 1 {
			sim -= 0.05 * float64(bucketDist-1)
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
