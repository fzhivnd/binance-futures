package scanner

import "futures/internal/domain"

// CandleProvider fetches the most recent candles for a symbol and timeframe.
type CandleProvider interface {
	GetCandles(symbol string, tf domain.Timeframe, limit int) []domain.Candle
}

func mapFundingToScore(rate float64) float64 {
	switch {
	case rate >= -0.002:
		return 0
	case rate >= -0.005:
		// linear 40–60 between -0.002 and -0.005
		t := (rate - (-0.002)) / (-0.005 - (-0.002))
		return 40 + t*20
	case rate >= -0.01:
		// linear 60–80 between -0.005 and -0.01
		t := (rate - (-0.005)) / (-0.01 - (-0.005))
		return 60 + t*20
	case rate > -0.02:
		// linear 80–90 between -0.01 and -0.02
		t := (rate - (-0.01)) / (-0.02 - (-0.01))
		return 80 + t*10
	default:
		return 0
	}
}

// calcPriceROI returns (close-open)/open*100 for the last closed candle of the
// given timeframe. Returns 0 if no closed candle is available.
func calcPriceROI(cp CandleProvider, symbol string, tf domain.Timeframe) float64 {
	// Fetch 2 so that index 0 is guaranteed to be a fully-closed candle even
	// when the last one is still open.
	candles := cp.GetCandles(symbol, tf, 2)
	for i := len(candles) - 1; i >= 0; i-- {
		c := candles[i]
		if c.IsClosed && c.Open != 0 {
			return (c.Close - c.Open) / c.Open * 100
		}
	}
	return 0
}

// calc24hROI returns the 24h ROI by comparing the current mark price against
// the close price of the 6th closed 4h candle (≈24h ago).
// Returns 0 if there are fewer than 6 closed 4h candles available.
func calc24hROI(cp CandleProvider, symbol string, currentPrice float64) float64 {
	// Fetch 7 candles: up to 1 may still be open, so we need 7 to guarantee 6 closed.
	candles := cp.GetCandles(symbol, domain.Timeframe4h, 7)
	closed := make([]domain.Candle, 0, 6)
	for i := len(candles) - 1; i >= 0; i-- {
		if candles[i].IsClosed {
			closed = append(closed, candles[i])
		}
		if len(closed) == 6 {
			break
		}
	}
	if len(closed) < 6 {
		return 0
	}
	base := closed[5].Close // 6th closed candle = 24h ago
	if base == 0 {
		return 0
	}
	return (currentPrice - base) / base * 100
}

// calc24hVolume sums the last 24 closed 1h candles to get 24h quote volume.
func calc24hVolume(cp CandleProvider, symbol string) float64 {
	candles := cp.GetCandles(symbol, domain.Timeframe1h, 25)
	var sum float64
	count := 0
	for i := len(candles) - 1; i >= 0 && count < 24; i-- {
		if candles[i].IsClosed {
			sum += candles[i].Volume
			count++
		}
	}
	return sum
}

func buildCandidate(symbol string, rate, price float64, cp CandleProvider) domain.Candidate {

	var roi1d, roi4h, roi1h float64
	var volume24h float64
	if cp != nil {
		roi1d = calc24hROI(cp, symbol, price)
		roi4h = calcPriceROI(cp, symbol, domain.Timeframe4h)
		roi1h = calcPriceROI(cp, symbol, domain.Timeframe1h)
		volume24h = calc24hVolume(cp, symbol)
	}

	return domain.Candidate{
		Symbol:      symbol,
		FundingRate: rate,
		MarkPrice:   price,
		DailyROI:    roi1d,
		ROI1D:       roi1d,
		ROI4H:       roi4h,
		ROI1H:       roi1h,
		Volume24h:   volume24h,
		Score:       mapFundingToScore(rate),
	}
}

// FilterByROI removes candidates whose 24h price change doesn't meet the minimum threshold.
func FilterByROI(candidates []domain.Candidate, minROIPct float64) []domain.Candidate {
	filtered := make([]domain.Candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.ROI1D >= minROIPct {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

// FilterByVolume removes candidates whose 24h trading volume is below the minimum threshold.
// volumeThresholdM is in millions of USDT.
func FilterByVolume(candidates []domain.Candidate, volumeThresholdM float64) []domain.Candidate {
	thresholdRaw := volumeThresholdM * 1_000_000
	filtered := make([]domain.Candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.Volume24h >= thresholdRaw {
			filtered = append(filtered, c)
		}
	}
	return filtered
}
