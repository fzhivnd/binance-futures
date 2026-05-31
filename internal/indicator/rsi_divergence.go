package indicator

import (
	"math"

	"futures/internal/domain"
)

// RSIDivergenceConfig holds per-call thresholds for divergence detection.
type RSIDivergenceConfig struct {
	Lookback           int
	MinSwingDistance   int
	StrongPriceDiffPct float64
	StrongRSIDiffPts   float64
}

var RSIDivConfig = RSIDivergenceConfig{
	Lookback:           20,
	MinSwingDistance:   3,
	StrongPriceDiffPct: 0.5,
	StrongRSIDiffPts:   5.0,
}

// DetectRSIDivergence returns a bearish RSI divergence if price makes a higher
// high while RSI makes a lower high over the lookback window. Returns nil if no
// divergence is found or there are not enough candles.
// candles must be closed candles sorted oldest→newest.
func DetectRSIDivergence(candles []domain.Candle, tf domain.Timeframe) *domain.RSIDivergence {
	const rsiPeriod = 14
	if len(candles) < rsiPeriod+RSIDivConfig.Lookback+2 {
		return nil
	}

	// Compute RSI for each candle position using a sliding window.
	rsiSeries := computeRSISeries(candles, rsiPeriod)
	if len(rsiSeries) == 0 {
		return nil
	}

	// Work within the lookback window at the tail of the series.
	window := candles[len(candles)-RSIDivConfig.Lookback:]
	rsiWindow := rsiSeries[len(rsiSeries)-RSIDivConfig.Lookback:]

	swings := findSwingHighs(window, RSIDivConfig.MinSwingDistance)
	if len(swings) < 2 {
		return nil
	}

	// Compare the two most recent swing highs.
	a := swings[len(swings)-2]
	b := swings[len(swings)-1]

	priceA := window[a].High
	priceB := window[b].High
	rsiA := rsiWindow[a]
	rsiB := rsiWindow[b]

	// Bearish divergence: price higher high, RSI lower high.
	if priceB <= priceA || rsiB >= rsiA {
		return nil
	}

	priceDiffPct := (priceB - priceA) / priceA * 100
	rsiDiffPts := rsiA - rsiB

	var strength domain.PatternStrength
	switch {
	case priceDiffPct >= RSIDivConfig.StrongPriceDiffPct && rsiDiffPts >= RSIDivConfig.StrongRSIDiffPts:
		strength = domain.StrengthStrong
	case priceDiffPct >= RSIDivConfig.StrongPriceDiffPct || rsiDiffPts >= RSIDivConfig.StrongRSIDiffPts:
		strength = domain.StrengthMedium
	default:
		strength = domain.StrengthWeak
	}

	return &domain.RSIDivergence{
		Timeframe: tf,
		Strength:  strength,
	}
}

// computeRSISeries returns one RSI value per candle starting at index rsiPeriod.
// The returned slice is aligned with candles[rsiPeriod:] (index 0 of rsiSeries
// corresponds to candles[rsiPeriod]).
func computeRSISeries(candles []domain.Candle, period int) []float64 {
	if len(candles) < period+1 {
		return nil
	}

	changes := make([]float64, len(candles)-1)
	for i := 1; i < len(candles); i++ {
		changes[i-1] = candles[i].Close - candles[i-1].Close
	}

	var avgGain, avgLoss float64
	for i := 0; i < period; i++ {
		if changes[i] > 0 {
			avgGain += changes[i]
		} else {
			avgLoss += -changes[i]
		}
	}
	avgGain /= float64(period)
	avgLoss /= float64(period)

	out := make([]float64, 0, len(changes)-period+1)
	appendRSI := func(g, l float64) {
		if l == 0 {
			out = append(out, 100)
			return
		}
		out = append(out, 100-(100/(1+g/l)))
	}
	appendRSI(avgGain, avgLoss)

	for i := period; i < len(changes); i++ {
		if changes[i] > 0 {
			avgGain = (avgGain*float64(period-1) + changes[i]) / float64(period)
			avgLoss = (avgLoss * float64(period-1)) / float64(period)
		} else {
			avgGain = (avgGain * float64(period-1)) / float64(period)
			avgLoss = (avgLoss*float64(period-1) + math.Abs(changes[i])) / float64(period)
		}
		appendRSI(avgGain, avgLoss)
	}
	return out
}

// findSwingHighs returns indices (within the slice) of local price highs.
// A swing high at index i requires candles[i].High to be the highest within
// [i-minDist, i+minDist].
func findSwingHighs(candles []domain.Candle, minDist int) []int {
	var out []int
	for i := minDist; i < len(candles)-minDist; i++ {
		high := candles[i].High
		isHigh := true
		for j := i - minDist; j <= i+minDist; j++ {
			if j != i && candles[j].High >= high {
				isHigh = false
				break
			}
		}
		if isHigh {
			out = append(out, i)
		}
	}
	return out
}
