package memory

import (
	"fmt"
	"strings"

	"futures/internal/domain"
)

// BuildFeatureText creates a normalized text representation of a trade setup
// for embedding. Structured so semantically similar setups produce similar vectors.
//
// Key discriminating fields (symbol, entry_mode, funding_bucket, roi_bucket) appear
// first and are repeated at the bottom as setup_key — text-embedding-3-small weights
// tokens near both ends more heavily, amplifying their influence on cosine similarity.
func BuildFeatureText(
	snap *domain.IndicatorSnapshot,
	btc *domain.BTCContext,
	candidate *domain.Candidate,
	score float64,
	entryMode string,
	minutesToSettle int,
) string {
	var sb strings.Builder

	roiBucket := ROIBucket(candidate.DailyROI)
	fundingLabel := FundingBucketLabel(FundingBucket(candidate.FundingRate * 100))

	// Key discriminating fields first — position gives them higher embedding weight.
	sb.WriteString(fmt.Sprintf("symbol: %s | entry_mode: %s | funding_bucket: %s | roi_bucket: %s\n",
		candidate.Symbol, entryMode, fundingLabel, roiBucket))

	sb.WriteString(fmt.Sprintf("funding_rate: %.3f%% | daily_roi: %.2f%%\n",
		candidate.FundingRate*100, candidate.DailyROI))

	sb.WriteString(fmt.Sprintf("rsi_14_15m: %s(%.2f) | rsi_7_5m: %s(%.2f) | momentum_loss: %v\n",
		RSIBucket(snap.RSI14_15m), snap.RSI14_15m, RSIBucket(snap.RSI7_5m), snap.RSI7_5m, snap.MomentumLoss))

	sb.WriteString(fmt.Sprintf("oi_delta_1h: %s(%+.2f%%) | oi_delta_15m: %+.2f%%\n",
		OIDeltaBucket(snap.OIDelta1h), snap.OIDelta1h, snap.OIDelta15m))

	sb.WriteString(fmt.Sprintf("atr_ratio: %s(%.2f) | vol_change_5m: %+.2f%% | volume_spike: %v\n",
		ATRBucket(snap.ATRRatio), snap.ATRRatio, snap.VolChange5m, snap.VolumeSpike))

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

	// Repeat key fields at the bottom to reinforce their embedding weight.
	sb.WriteString(fmt.Sprintf("setup_key: symbol=%s entry_mode=%s funding_bucket=%s roi_bucket=%s\n",
		candidate.Symbol, entryMode, fundingLabel, roiBucket))

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

// FundingBucketLabel returns a human-readable label for use in feature text.
func FundingBucketLabel(bucket int) string {
	switch bucket {
	case 3:
		return "EXTREME"
	case 2:
		return "MODERATE"
	default:
		return "MILD"
	}
}

// ROIBucket categorizes daily ROI into semantic ranges for feature text.
// Ranges: 0-15% LOW, 15-40% MEDIUM, 40-60% HIGH, >60% VERY_HIGH.
func ROIBucket(dailyROI float64) string {
	switch {
	case dailyROI >= 60:
		return "VERY_HIGH"
	case dailyROI >= 40:
		return "HIGH"
	case dailyROI >= 15:
		return "MEDIUM"
	default:
		return "LOW"
	}
}

// RSIBucket labels RSI into semantic zones.
func RSIBucket(rsi float64) string {
	switch {
	case rsi >= 70:
		return "OVERBOUGHT"
	case rsi <= 30:
		return "OVERSOLD"
	default:
		return "NEUTRAL"
	}
}

// OIDeltaBucket labels open-interest delta direction.
func OIDeltaBucket(delta float64) string {
	switch {
	case delta >= 2.0:
		return "RISING"
	case delta <= -2.0:
		return "FALLING"
	default:
		return "FLAT"
	}
}

// ATRBucket labels ATR ratio relative to baseline volatility.
func ATRBucket(atrRatio float64) string {
	switch {
	case atrRatio >= 7:
		return "HIGH_VOL"
	case atrRatio <= 2:
		return "LOW_VOL"
	default:
		return "NORMAL_VOL"
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
