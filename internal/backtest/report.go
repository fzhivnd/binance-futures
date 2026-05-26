package backtest

import (
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"strconv"
	"time"
)

func buildReport(trades []Trade, finalBalance, initialBalance, maxDD float64) *Result {
	if len(trades) == 0 {
		return &Result{MaxDrawdownPct: maxDD}
	}

	var wins, totalProfit, totalLoss float64
	for _, t := range trades {
		if t.Result == "WIN" {
			wins++
			totalProfit += t.PnL
		} else if t.Result == "LOSS" {
			totalLoss += t.PnL
		}
	}

	n := float64(len(trades))
	winRate := wins / n * 100
	avgProfit := totalProfit / wins
	var avgLoss float64
	if losses := n - wins; losses > 0 {
		avgLoss = totalLoss / losses
	}

	sharpe := computeSharpe(trades)

	return &Result{
		TotalTrades:    len(trades),
		WinRate:        winRate,
		AvgProfit:      avgProfit,
		AvgLoss:        avgLoss,
		TotalPnL:       finalBalance - initialBalance,
		MaxDrawdownPct: maxDD,
		SharpeRatio:    sharpe,
		Trades:         trades,
	}
}

func computeSharpe(trades []Trade) float64 {
	if len(trades) < 2 {
		return 0
	}
	var sum float64
	for _, t := range trades {
		sum += t.PnL
	}
	mean := sum / float64(len(trades))

	var variance float64
	for _, t := range trades {
		d := t.PnL - mean
		variance += d * d
	}
	variance /= float64(len(trades) - 1)
	stddev := math.Sqrt(variance)
	if stddev == 0 {
		return 0
	}
	return mean / stddev
}

// PrintSummary prints a human-readable backtest report to stdout.
func (r *Result) PrintSummary() {
	fmt.Printf("\n=== Backtest Report ===\n")
	fmt.Printf("Total Trades:    %d\n", r.TotalTrades)
	fmt.Printf("Win Rate:        %.1f%%\n", r.WinRate)
	fmt.Printf("Avg Profit:      %.4f\n", r.AvgProfit)
	fmt.Printf("Avg Loss:        %.4f\n", r.AvgLoss)
	fmt.Printf("Total PnL:       %.4f\n", r.TotalPnL)
	fmt.Printf("Max Drawdown:    %.2f%%\n", r.MaxDrawdownPct)
	fmt.Printf("Sharpe Ratio:    %.3f\n", r.SharpeRatio)

	// Distribution by confidence bucket
	buckets := map[string]int{}
	for _, t := range r.Trades {
		buckets[t.Confidence]++
	}
	fmt.Printf("\nScore Distribution:\n")
	for _, conf := range []string{"VERY_HIGH", "HIGH", "MEDIUM", "LOW"} {
		if n, ok := buckets[conf]; ok {
			fmt.Printf("  %-10s %d trades\n", conf, n)
		}
	}
}

// WriteCSV exports all simulated trades to a CSV file.
func (r *Result) WriteCSV(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create csv: %w", err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	header := []string{
		"timestamp", "symbol", "funding_rate", "composite_score", "confidence",
		"entry_price", "exit_price", "pnl", "result", "hold_minutes",
		"score_funding", "score_oi", "score_btc", "score_candle",
		"score_volume", "score_roi", "score_volatility",
	}
	if err := w.Write(header); err != nil {
		return err
	}

	for _, t := range r.Trades {
		row := []string{
			t.Timestamp.Format(time.RFC3339),
			t.Symbol,
			strconv.FormatFloat(t.FundingRate, 'f', 6, 64),
			strconv.FormatFloat(t.CompositeScore, 'f', 2, 64),
			t.Confidence,
			strconv.FormatFloat(t.EntryPrice, 'f', 8, 64),
			strconv.FormatFloat(t.ExitPrice, 'f', 8, 64),
			strconv.FormatFloat(t.PnL, 'f', 4, 64),
			t.Result,
			strconv.FormatFloat(t.HoldDuration.Minutes(), 'f', 0, 64),
			strconv.FormatFloat(t.Breakdown.FundingScore, 'f', 2, 64),
			strconv.FormatFloat(t.Breakdown.OIScore, 'f', 2, 64),
			strconv.FormatFloat(t.Breakdown.BTCScore, 'f', 2, 64),
			strconv.FormatFloat(t.Breakdown.CandleScore, 'f', 2, 64),
			strconv.FormatFloat(t.Breakdown.VolumeScore, 'f', 2, 64),
			strconv.FormatFloat(t.Breakdown.ROIScore, 'f', 2, 64),
			strconv.FormatFloat(t.Breakdown.VolatilityScore, 'f', 2, 64),
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	return nil
}
