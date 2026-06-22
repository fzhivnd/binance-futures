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
	at time.Time,
) ([]domain.SimilarTrade, error) {
	results, err := r.repo.FindSimilar(ctx, embedding, r.cfg.TopSimilar)
	if err != nil {
		return nil, err
	}

	btcRegime := BTCRegime(btc.Trend, btc.IsBreakout, btc.MomentumScore)
	fundingBucket := FundingBucket(candidate.FundingRate * 100)
	roiBucket := ROIBucket(candidate.DailyROI)
	dayOfWeek := DayOfWeekLabel(at)
	fundingWindow := FundingWindowLabel(at)
	atrBucket := ATRBucket(snap.ATRRatio)

	now := time.Now()
	var similar []domain.SimilarTrade

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
