package memory

import (
	"fmt"
	"strings"
	"time"

	"futures/internal/domain"
)

// BuildFeatureText creates a normalized text representation of a trade setup
// for embedding. Structured so semantically similar market conditions produce similar vectors.
//
// Symbol and entry_mode are intentionally excluded — they are metadata fields only.
// Key regime fields (funding_bucket, roi_bucket, RSI zones, OI direction, ATR, patterns)
// appear first and are repeated at the bottom as setup_key to reinforce embedding weight.
func BuildFeatureText(
	snap *domain.IndicatorSnapshot,
	btc *domain.BTCContext,
	candidate *domain.Candidate,
	score float64,
	entryMode string,
	at time.Time,
) string {
	var sb strings.Builder

	dayOfWeek := DayOfWeekLabel(at)
	fundingWindow := FundingWindowLabel(at)

	roiBucket := ROIBucket(candidate.DailyROI)
	fundingLabel := FundingBucketLabel(FundingBucket(candidate.FundingRate * 100))

	// Key discriminating fields first — position gives them higher embedding weight.
	// Symbol and entry_mode intentionally excluded: we want market-condition similarity,
	// not ticker identity or decision identity. Entry mode is metadata only (retriever bonus).
	sb.WriteString(fmt.Sprintf("funding_bucket: %s | roi_bucket: %s | day: %s | funding_window: %02dUTC\n",
		fundingLabel, roiBucket, dayOfWeek, fundingWindow))

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
		sb.WriteString(fmt.Sprintf("btc_regime: %s | btc_momentum: %d | btc_rsi: %.2f | btc_breakout: %v | btc_change_1h: %+.2f%%\n",
			BTCRegime(btc.Trend, btc.IsBreakout, btc.MomentumScore), btc.MomentumScore, btc.RSI14_1h, btc.IsBreakout, btc.PriceChange1h))
	}

	// Repeat key fields at the bottom to reinforce their embedding weight.
	btcRegimeLabel := ""
	if btc != nil {
		btcRegimeLabel = BTCRegime(btc.Trend, btc.IsBreakout, btc.MomentumScore)
	}
	sb.WriteString(fmt.Sprintf("setup_key: funding_bucket=%s roi_bucket=%s day=%s funding_window=%02dUTC btc_regime=%s\n",
		fundingLabel, roiBucket, dayOfWeek, fundingWindow, btcRegimeLabel))

	return sb.String()
}

// FundingBucket categorizes funding rate severity for metadata filtering.
// Input is the raw rate (e.g. -0.008 = -0.8%), internally sign-flipped.
func FundingBucket(fundingRatePct float64) int {
	absFunding := -fundingRatePct // funding is negative for shorts
	switch {
	case absFunding >= 1.5:
		return 5 // critical
	case absFunding >= 1.0:
		return 4 // extreme
	case absFunding >= 0.6:
		return 3 // high
	case absFunding >= 0.3:
		return 2 // moderate
	default:
		return 1 // mild
	}
}

// FundingBucketLabel returns a human-readable label for use in feature text.
func FundingBucketLabel(bucket int) string {
	switch bucket {
	case 5:
		return "CRITICAL"
	case 4:
		return "EXTREME"
	case 3:
		return "HIGH"
	case 2:
		return "MODERATE"
	default:
		return "MILD"
	}
}

// ROIBucket categorizes daily ROI into 7 semantic ranges for feature text.
func ROIBucket(dailyROI float64) string {
	switch {
	case dailyROI >= 75:
		return "EXTREME"
	case dailyROI >= 50:
		return "VERY_HIGH"
	case dailyROI >= 35:
		return "HIGH"
	case dailyROI >= 20:
		return "MEDIUM"
	case dailyROI >= 10:
		return "BELOW_MEDIUM"
	case dailyROI >= 3:
		return "LOW"
	default:
		return "VERY_LOW"
	}
}

// RSIBucket labels RSI into 7 semantic zones.
func RSIBucket(rsi float64) string {
	switch {
	case rsi >= 80:
		return "EXTREME_OVERBOUGHT"
	case rsi >= 70:
		return "OVERBOUGHT"
	case rsi >= 55:
		return "BULLISH"
	case rsi >= 46:
		return "NEUTRAL"
	case rsi >= 31:
		return "BEARISH"
	case rsi >= 21:
		return "OVERSOLD"
	default:
		return "EXTREME_OVERSOLD"
	}
}

// OIDeltaBucket labels open-interest delta direction into 7 zones.
func OIDeltaBucket(delta float64) string {
	switch {
	case delta >= 10.0:
		return "EXTREME_RISING"
	case delta >= 5.0:
		return "STRONG_RISING"
	case delta >= 2.0:
		return "RISING"
	case delta <= -10.0:
		return "EXTREME_FALLING"
	case delta <= -5.0:
		return "STRONG_FALLING"
	case delta <= -2.0:
		return "FALLING"
	default:
		return "FLAT"
	}
}

// ATRBucket labels ATR ratio relative to baseline volatility into 7 zones.
func ATRBucket(atrRatio float64) string {
	switch {
	case atrRatio >= 10.0:
		return "EXTREME"
	case atrRatio >= 7.0:
		return "HIGH"
	case atrRatio >= 5.0:
		return "ELEVATED"
	case atrRatio >= 3.5:
		return "NORMAL"
	case atrRatio >= 2.0:
		return "BELOW_NORMAL"
	case atrRatio >= 1.0:
		return "LOW"
	default:
		return "VERY_LOW"
	}
}

// DayOfWeekLabel returns a 3-letter UTC weekday label from a time.Time.
func DayOfWeekLabel(t time.Time) string {
	days := [7]string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}
	return days[t.UTC().Weekday()]
}

// FundingWindowLabel returns the UTC hour of the next funding settlement
// (0, 4, 8, 12, 16, 20). A time exactly on a boundary (e.g. 8:00) returns
// that boundary — covering the AFTER window that opens at settlement.
func FundingWindowLabel(t time.Time) int {
	t = t.UTC()
	h, m := t.Hour(), t.Minute()
	if h%4 == 0 && m == 0 {
		return h
	}
	return ((h / 4) + 1) * 4 % 24
}

// BTCRegime maps BTC trend, breakout flag, and momentum score to a composite regime label.
func BTCRegime(btcTrend string, isBreakout bool, momentum int) string {
	switch btcTrend {
	case "bullish":
		if isBreakout {
			return "BULLISH_BREAKOUT"
		}
		if momentum >= 2 {
			return "BULLISH_STRONG"
		}
		return "BULLISH_WEAK"
	case "bearish":
		if isBreakout {
			return "BEARISH_BREAKDOWN"
		}
		if momentum <= -2 {
			return "BEARISH_STRONG"
		}
		return "BEARISH_WEAK"
	default:
		return "NEUTRAL"
	}
}
