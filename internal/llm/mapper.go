package llm

import (
	"time"

	"futures/internal/domain"
	"futures/internal/scheduler"
)

func MapToLLMRequest(
	candidates []*domain.ScoredCandidate,
	btc *domain.BTCContext,
	window scheduler.WindowType,
) *LLMRequest {
	req := &LLMRequest{
		Timestamp:     time.Now().UTC().Unix(),
		FundingWindow: string(window),
		Candidates:    make([]LLMCandidate, 0, len(candidates)),
	}

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

	for _, sc := range candidates {
		c := LLMCandidate{
			Symbol:         sc.Candidate.Symbol,
			FundingRate:    sc.Candidate.FundingRate * 100,
			DailyROI:       sc.Candidate.DailyROI,
			CompositeScore: sc.CompositeScore,
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
		TPStrategy:   resp.TPStrategy,
		EntryReasons: resp.EntryReasons,
		Warnings:     resp.Warnings,
		SkipReason:   resp.SkipReason,
	}

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
