package scoring

// WeightConfig defines the maximum points for each scoring category (total = 100).
type WeightConfig struct {
	Funding    float64
	OI         float64
	BTC        float64
	Candle     float64
	Volume     float64
	ROI        float64
	Volatility float64
}

var DefaultWeights = WeightConfig{
	Funding:    25,
	OI:         15,
	BTC:        10,
	Candle:     20,
	Volume:     10,
	ROI:        15,
	Volatility: 5,
}

func mapScoreToConfidence(score float64) (confidence string, sizePct float64) {
	switch {
	case score >= 90:
		return "VERY_HIGH", 5.0
	case score >= 80:
		return "HIGH", 4.0
	case score >= 70:
		return "MEDIUM", 3.0
	case score >= 60:
		return "LOW", 2.0
	default:
		return "SKIP", 0.0
	}
}
