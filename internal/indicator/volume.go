package indicator

import "futures/internal/domain"

// VolumeAnomaly compares the most recent closed candle's volume against the
// trailing 24-candle average. Returns the ratio and whether it's a spike (>2x).
func VolumeAnomaly(candles []domain.Candle) (ratio float64, spike bool) {
	if len(candles) < 2 {
		return 1.0, false
	}

	current := candles[len(candles)-1].Volume

	count := len(candles) - 1
	if count > 24 {
		count = 24
	}
	var sum float64
	for i := len(candles) - 1 - count; i < len(candles)-1; i++ {
		sum += candles[i].Volume
	}
	avg := sum / float64(count)

	if avg == 0 {
		return 1.0, false
	}

	ratio = current / avg
	spike = ratio > 2.0
	return ratio, spike
}
