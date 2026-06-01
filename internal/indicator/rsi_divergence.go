package indicator

import (
	"math"

	"futures/internal/domain"
)

type RSIDivergenceConfig struct {
	Lookback            int
	MinSwingDistance    int
	MinSwingProminence  float64
	StrongPriceDiffPct  float64
	StrongRSIDiffPts    float64
	MinRSIPeak          float64
	MaxBarsSinceSwing   int
	RSIPeakSearchRadius int
}

var RSIDivConfig = RSIDivergenceConfig{
	Lookback:            50,
	MinSwingDistance:    5,
	MinSwingProminence:  0.5,
	StrongPriceDiffPct:  0.5,
	StrongRSIDiffPts:    5.0,
	MinRSIPeak:          65,
	MaxBarsSinceSwing:   5,
	RSIPeakSearchRadius: 2,
}

func DetectRSIDivergence(
	candles []domain.Candle,
	tf domain.Timeframe,
) *domain.RSIDivergence {
	const rsiPeriod = 14

	if len(candles) < rsiPeriod+RSIDivConfig.Lookback+10 {
		return nil
	}

	rsiSeries := computeRSISeries(candles, rsiPeriod)
	if len(rsiSeries) == 0 {
		return nil
	}

	offset := len(candles) - len(rsiSeries)

	windowCandles := candles[len(candles)-RSIDivConfig.Lookback:]
	windowStart := len(candles) - RSIDivConfig.Lookback

	swings := findSwingHighs(
		windowCandles,
		RSIDivConfig.MinSwingDistance,
		RSIDivConfig.MinSwingProminence,
	)

	if len(swings) < 2 {
		return nil
	}

	b := swings[len(swings)-1]

	if len(windowCandles)-1-b > RSIDivConfig.MaxBarsSinceSwing {
		return nil
	}

	var a = -1

	for i := len(swings) - 2; i >= 0; i-- {
		prev := swings[i]

		priceA := windowCandles[prev].High
		priceB := windowCandles[b].High

		diffPct := math.Abs(priceB-priceA) / priceA * 100

		if diffPct >= RSIDivConfig.MinSwingProminence {
			a = prev
			break
		}
	}

	if a == -1 {
		return nil
	}

	globalA := windowStart + a
	globalB := windowStart + b

	rsiIdxA := globalA - offset
	rsiIdxB := globalB - offset

	if rsiIdxA < 0 || rsiIdxB < 0 {
		return nil
	}

	rsiA := localRSIPeak(
		rsiSeries,
		rsiIdxA,
		RSIDivConfig.RSIPeakSearchRadius,
	)

	rsiB := localRSIPeak(
		rsiSeries,
		rsiIdxB,
		RSIDivConfig.RSIPeakSearchRadius,
	)

	if math.Max(rsiA, rsiB) < RSIDivConfig.MinRSIPeak {
		return nil
	}

	priceA := windowCandles[a].High
	priceB := windowCandles[b].High

	if priceB <= priceA {
		return nil
	}

	if rsiB >= rsiA {
		return nil
	}

	priceDiffPct := (priceB - priceA) / priceA * 100
	rsiDiffPts := rsiA - rsiB

	var strength domain.PatternStrength

	switch {
	case priceDiffPct >= RSIDivConfig.StrongPriceDiffPct &&
		rsiDiffPts >= RSIDivConfig.StrongRSIDiffPts:
		strength = domain.StrengthStrong

	case priceDiffPct >= RSIDivConfig.StrongPriceDiffPct ||
		rsiDiffPts >= RSIDivConfig.StrongRSIDiffPts:
		strength = domain.StrengthMedium

	default:
		strength = domain.StrengthWeak
	}

	return &domain.RSIDivergence{
		Timeframe: tf,
		Strength:  strength,
	}
}

func localRSIPeak(
	rsi []float64,
	center int,
	radius int,
) float64 {
	start := max(0, center-radius)
	end := min(len(rsi)-1, center+radius)

	peak := rsi[center]

	for i := start; i <= end; i++ {
		if rsi[i] > peak {
			peak = rsi[i]
		}
	}

	return peak
}

func findSwingHighs(
	candles []domain.Candle,
	minDist int,
	minProminencePct float64,
) []int {
	var swings []int

	for i := minDist; i < len(candles)-minDist; i++ {
		high := candles[i].High

		isSwing := true

		leftMax := candles[i-minDist].High
		rightMax := candles[i+minDist].High

		for j := i - minDist; j <= i+minDist; j++ {
			if j == i {
				continue
			}

			if candles[j].High >= high {
				isSwing = false
				break
			}

			if j < i {
				leftMax = math.Max(leftMax, candles[j].High)
			}

			if j > i {
				rightMax = math.Max(rightMax, candles[j].High)
			}
		}

		if !isSwing {
			continue
		}

		ref := math.Max(leftMax, rightMax)

		prominencePct := (high - ref) / ref * 100

		if prominencePct < minProminencePct {
			continue
		}

		swings = append(swings, i)
	}

	return swings
}

func computeRSISeries(
	candles []domain.Candle,
	period int,
) []float64 {
	if len(candles) < period+1 {
		return nil
	}

	changes := make([]float64, len(candles)-1)

	for i := 1; i < len(candles); i++ {
		changes[i-1] =
			candles[i].Close -
				candles[i-1].Close
	}

	var avgGain float64
	var avgLoss float64

	for i := 0; i < period; i++ {
		if changes[i] > 0 {
			avgGain += changes[i]
		} else {
			avgLoss += -changes[i]
		}
	}

	avgGain /= float64(period)
	avgLoss /= float64(period)

	var result []float64

	appendRSI := func() {
		if avgLoss == 0 {
			result = append(result, 100)
			return
		}

		rs := avgGain / avgLoss

		result = append(
			result,
			100-(100/(1+rs)),
		)
	}

	appendRSI()

	for i := period; i < len(changes); i++ {
		gain := math.Max(changes[i], 0)
		loss := math.Max(-changes[i], 0)

		avgGain =
			(avgGain*float64(period-1) + gain) /
				float64(period)

		avgLoss =
			(avgLoss*float64(period-1) + loss) /
				float64(period)

		appendRSI()
	}

	return result
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
