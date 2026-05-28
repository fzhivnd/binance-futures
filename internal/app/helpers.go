package app

import (
	"context"
	"time"
)

func confidenceToSize(confidence int) float64 {
	switch {
	case confidence >= 90:
		return 5.0
	case confidence >= 80:
		return 4.0
	case confidence >= 70:
		return 3.0
	case confidence >= 60:
		return 2.0
	default:
		return 0
	}
}

// checkHistoricalPrice returns the % price change from entryTime to entryTime+2h.
// Negative return means price dropped (short would have profited).
func (a *App) checkHistoricalPrice(symbol string, entryTime time.Time) (float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return a.binanceClient.GetPriceChange(ctx, symbol, entryTime, entryTime.Add(2*time.Hour))
}
