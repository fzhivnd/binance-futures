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

// checkHistoricalPrice scans 1m candles over 2h from entryTime and returns the outcome
// and hypothetical profit pct for a short. Positive profitPct = short would have profited.
func (a *App) checkHistoricalPrice(symbol string, entryTime time.Time) (string, float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	profitPct, err := a.binanceClient.GetPriceOutcome(
		ctx, symbol,
		entryTime, entryTime.Add(2*time.Hour),
		a.cfg.Execution.TpPct, a.cfg.Execution.SlPct,
	)
	if err != nil {
		return "", 0, err
	}

	if profitPct > 0 {
		return "SKIP_MISSED", profitPct, nil
	}
	return "SKIP_VALIDATED", -profitPct, nil
}
