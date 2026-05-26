package indicator

import (
	"fmt"
	"math"

	"futures/internal/domain"
)

// ATR computes the Average True Range over period candles using Wilder's smoothing.
func ATR(candles []domain.Candle, period int) (float64, error) {
	if len(candles) < period+1 {
		return 0, fmt.Errorf("need %d candles, got %d", period+1, len(candles))
	}

	trValues := make([]float64, len(candles)-1)
	for i := 1; i < len(candles); i++ {
		hl := candles[i].High - candles[i].Low
		hpc := math.Abs(candles[i].High - candles[i-1].Close)
		lpc := math.Abs(candles[i].Low - candles[i-1].Close)
		trValues[i-1] = math.Max(hl, math.Max(hpc, lpc))
	}

	var atr float64
	for i := 0; i < period; i++ {
		atr += trValues[i]
	}
	atr /= float64(period)

	for i := period; i < len(trValues); i++ {
		atr = (atr*float64(period-1) + trValues[i]) / float64(period)
	}

	return atr, nil
}

// ATRRatio returns ATR as a percentage of the current price.
func ATRRatio(atr, currentPrice float64) float64 {
	if currentPrice == 0 {
		return 0
	}
	return atr / currentPrice * 100
}
