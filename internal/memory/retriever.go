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

	now := time.Now()
	var similar []domain.SimilarTrade

	for _, res := range results {
		if res.Similarity < r.cfg.MinSimilarity {
			continue
		}
		if res.Memory.Outcome == "" {
			continue // outcome not yet determined
		}

		daysAgo := int(now.Sub(res.Memory.CreatedAt).Hours() / 24)

		similar = append(similar, domain.SimilarTrade{
			Outcome:     res.Memory.Outcome,
			ProfitPct:   res.Memory.ProfitPct,
			Similarity:  res.Similarity,
			Lesson:      res.Memory.Lesson,
			DaysAgo:     daysAgo,
			EntryMode:   res.Memory.EntryMode,
			FundingRate: res.Memory.FundingRate,
		})
	}

	return similar, nil
}
