package memory

import (
	"fmt"
	"strings"

	"futures/internal/domain"
)

// BuildFeatureText creates a normalized text representation of a trade setup
// for embedding. Structured so semantically similar setups produce similar vectors.
func BuildFeatureText(
	snap *domain.IndicatorSnapshot,
	btc *domain.BTCContext,
	candidate *domain.Candidate,
	score float64,
	entryMode string,
	minutesToSettle int,
) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("funding_rate: %.3f%% | daily_roi: %.2f%% | composite_score: %.1f\n",
		candidate.FundingRate*100, candidate.DailyROI, score))

	sb.WriteString(fmt.Sprintf("rsi_14_15m: %.2f | rsi_7_5m: %.2f | momentum_loss: %v\n",
		snap.RSI14_15m, snap.RSI7_5m, snap.MomentumLoss))

	sb.WriteString(fmt.Sprintf("oi_delta_1h: %+.2f%% | oi_delta_15m: %+.2f%%\n",
		snap.OIDelta1h, snap.OIDelta15m))

	sb.WriteString(fmt.Sprintf("atr_ratio: %.2f | vol_change_5m: %+.2f%% | volume_spike: %v\n",
		snap.ATRRatio, snap.VolChange5m, snap.VolumeSpike))

	if len(snap.Patterns) > 0 {
		sb.WriteString("candle_patterns: ")
		for _, p := range snap.Patterns {
			sb.WriteString(fmt.Sprintf("%s:%s(%s) ", p.Timeframe, p.Pattern, p.Strength))
		}
		sb.WriteString("\n")
	}

	if len(snap.RSIDivergences) > 0 {
		sb.WriteString("rsi_divergences: ")
		for _, d := range snap.RSIDivergences {
			sb.WriteString(fmt.Sprintf("%s:%s ", d.Timeframe, d.Strength))
		}
		sb.WriteString("\n")
	}

	if btc != nil {
		sb.WriteString(fmt.Sprintf("btc_trend: %s | btc_momentum: %d | btc_rsi: %.2f | btc_breakout: %v | btc_change_1h: %+.2f%%\n",
			btc.Trend, btc.MomentumScore, btc.RSI14_1h, btc.IsBreakout, btc.PriceChange1h))
	}

	sb.WriteString(fmt.Sprintf("entry_mode: %s | minutes_to_settlement: %d\n",
		entryMode, minutesToSettle))

	return sb.String()
}

// FundingBucket categorizes funding rate severity for metadata filtering.
// Input is the raw rate (e.g. -0.008 = -0.8%), internally sign-flipped.
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

// BTCRegime maps BTC trend to a regime category for metadata filtering.
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
