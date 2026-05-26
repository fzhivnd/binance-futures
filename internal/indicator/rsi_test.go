package indicator

import (
	"testing"

	"futures/internal/domain"
)

func makeCandles(closes []float64) []domain.Candle {
	cs := make([]domain.Candle, len(closes))
	for i, c := range closes {
		cs[i] = domain.Candle{Close: c, Open: c, High: c, Low: c, IsClosed: true}
	}
	return cs
}

func TestRSI_AllGains(t *testing.T) {
	// All gains → RSI should be 100
	closes := make([]float64, 16)
	for i := range closes {
		closes[i] = float64(i + 1)
	}
	rsi, err := RSI(makeCandles(closes), 14)
	if err != nil {
		t.Fatal(err)
	}
	if rsi != 100 {
		t.Errorf("expected 100, got %.2f", rsi)
	}
}

func TestRSI_AllLosses(t *testing.T) {
	closes := make([]float64, 16)
	for i := range closes {
		closes[i] = float64(16 - i)
	}
	rsi, err := RSI(makeCandles(closes), 14)
	if err != nil {
		t.Fatal(err)
	}
	if rsi != 0 {
		t.Errorf("expected 0, got %.2f", rsi)
	}
}

func TestRSI_FlatPrice(t *testing.T) {
	closes := make([]float64, 16)
	for i := range closes {
		closes[i] = 100
	}
	rsi, err := RSI(makeCandles(closes), 14)
	if err != nil {
		t.Fatal(err)
	}
	// avgLoss == 0 → return 100
	if rsi != 100 {
		t.Errorf("expected 100 for flat price, got %.2f", rsi)
	}
}

func TestRSI_InsufficientData(t *testing.T) {
	_, err := RSI(makeCandles([]float64{1, 2}), 14)
	if err == nil {
		t.Error("expected error for insufficient data")
	}
}

func TestRSI_ExactlyPeriodPlusOne(t *testing.T) {
	closes := make([]float64, 15)
	for i := range closes {
		closes[i] = float64(i % 3) // zigzag
	}
	_, err := RSI(makeCandles(closes), 14)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
