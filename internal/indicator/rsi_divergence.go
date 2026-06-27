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
	Lookback:            80,
	MinSwingDistance:    3,
	MinSwingProminence:  0.15,
	StrongPriceDiffPct:  1.0,
	StrongRSIDiffPts:    6,
	MinRSIPeak:          50,
	MaxBarsSinceSwing:   15,
	RSIPeakSearchRadius: 3,
}

func DetectRSIDivergence(
	candles []domain.Candle,
	tf domain.Timeframe,
) *domain.RSIDivergence {
	const rsiPeriod = 14

	cfg := RSIDivConfig

	if len(candles) < rsiPeriod+cfg.Lookback+10 {
		return nil
	}

	rsiSeries := computeRSISeries(candles, rsiPeriod)
	if len(rsiSeries) == 0 {
		return nil
	}

	offset := len(candles) - len(rsiSeries)

	windowStart := max(
		0,
		len(candles)-cfg.Lookback,
	)

	windowCandles := candles[windowStart:]

	swings := findSwingHighs(
		windowCandles,
		cfg.MinSwingDistance,
		cfg.MinSwingProminence,
	)

	if len(swings) < 2 {
		return nil
	}

	var best *domain.RSIDivergence
	bestScore := -1.0

	for i := len(swings) - 1; i >= 1; i-- {
		b := swings[i]

		if len(windowCandles)-1-b >
			cfg.MaxBarsSinceSwing {
			continue
		}

		for j := i - 1; j >= 0; j-- {
			a := swings[j]

			globalA := windowStart + a
			globalB := windowStart + b

			rsiIdxA := globalA - offset
			rsiIdxB := globalB - offset

			if rsiIdxA < 0 ||
				rsiIdxB < 0 ||
				rsiIdxA >= len(rsiSeries) ||
				rsiIdxB >= len(rsiSeries) {
				continue
			}

			priceA := windowCandles[a].High
			priceB := windowCandles[b].High

			if priceB <= priceA {
				continue
			}

			rsiA := localRSIPeak(
				rsiSeries,
				rsiIdxA,
				cfg.RSIPeakSearchRadius,
			)

			rsiB := localRSIPeak(
				rsiSeries,
				rsiIdxB,
				cfg.RSIPeakSearchRadius,
			)

			if rsiB >= rsiA {
				continue
			}

			if math.Max(rsiA, rsiB) <
				cfg.MinRSIPeak {
				continue
			}

			priceDiffPct :=
				(priceB - priceA) /
					priceA *
					100

			rsiDiffPts :=
				rsiA - rsiB

			score :=
				priceDiffPct +
					rsiDiffPts

			if score <= bestScore {
				continue
			}

			var strength domain.PatternStrength

			switch {
			case priceDiffPct >= cfg.StrongPriceDiffPct &&
				rsiDiffPts >= cfg.StrongRSIDiffPts:
				strength = domain.StrengthStrong

			case priceDiffPct >= cfg.StrongPriceDiffPct ||
				rsiDiffPts >= cfg.StrongRSIDiffPts:
				strength = domain.StrengthMedium

			default:
				strength = domain.StrengthWeak
			}

			// Overbought bonus.
			if rsiA >= 75 ||
				rsiB >= 75 {
				switch strength {
				case domain.StrengthWeak:
					strength =
						domain.StrengthMedium
				case domain.StrengthMedium:
					strength =
						domain.StrengthStrong
				}
			}

			bestScore = score

			best = &domain.RSIDivergence{
				Timeframe: tf,
				Strength:  strength,
			}
		}
	}

	return best
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

		for j := i - minDist; j <= i+minDist; j++ {
			if j == i {
				continue
			}

			if candles[j].High > high {
				isSwing = false
				break
			}
		}

		if !isSwing {
			continue
		}

		left := candles[i-minDist].High
		right := candles[i+minDist].High

		ref := math.Max(left, right)

		if ref <= 0 {
			continue
		}

		prominence :=
			(high - ref) / ref * 100

		if prominence < minProminencePct {
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
