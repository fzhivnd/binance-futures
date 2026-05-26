package indicator

import (
	"fmt"

	"futures/internal/domain"
)

// RSI computes the 14-period RSI using Wilder's smoothing method.
func RSI(candles []domain.Candle, period int) (float64, error) {
	if len(candles) < period+1 {
		return 0, fmt.Errorf("need %d candles, got %d", period+1, len(candles))
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

	for i := period; i < len(changes); i++ {
		if changes[i] > 0 {
			avgGain = (avgGain*float64(period-1) + changes[i]) / float64(period)
			avgLoss = (avgLoss * float64(period-1)) / float64(period)
		} else {
			avgGain = (avgGain * float64(period-1)) / float64(period)
			avgLoss = (avgLoss*float64(period-1) + (-changes[i])) / float64(period)
		}
	}

	if avgLoss == 0 {
		return 100, nil
	}
	rs := avgGain / avgLoss
	return 100 - (100 / (1 + rs)), nil
}
