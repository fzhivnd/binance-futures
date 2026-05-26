package indicator

import (
	"testing"

	"futures/internal/domain"
)

func makeOHLC(opens, highs, lows, closes []float64) []domain.Candle {
	cs := make([]domain.Candle, len(closes))
	for i := range cs {
		cs[i] = domain.Candle{Open: opens[i], High: highs[i], Low: lows[i], Close: closes[i], IsClosed: true}
	}
	return cs
}

func TestATR_InsufficientData(t *testing.T) {
	_, err := ATR(makeCandles([]float64{1, 2}), 14)
	if err == nil {
		t.Error("expected error")
	}
}

func TestATR_ZeroRange(t *testing.T) {
	cs := make([]domain.Candle, 16)
	for i := range cs {
		cs[i] = domain.Candle{Open: 100, High: 100, Low: 100, Close: 100}
	}
	atr, err := ATR(cs, 14)
	if err != nil {
		t.Fatal(err)
	}
	if atr != 0 {
		t.Errorf("expected 0 ATR for flat candles, got %.6f", atr)
	}
}

func TestATR_PositiveResult(t *testing.T) {
	opens := make([]float64, 16)
	highs := make([]float64, 16)
	lows := make([]float64, 16)
	closes := make([]float64, 16)
	for i := range opens {
		opens[i] = 100
		highs[i] = 102
		lows[i] = 98
		closes[i] = 100
	}
	atr, err := ATR(makeOHLC(opens, highs, lows, closes), 14)
	if err != nil {
		t.Fatal(err)
	}
	if atr <= 0 {
		t.Errorf("expected positive ATR, got %.6f", atr)
	}
}

func TestATRRatio(t *testing.T) {
	r := ATRRatio(2.0, 100.0)
	if r != 2.0 {
		t.Errorf("expected 2.0, got %.2f", r)
	}
	r = ATRRatio(0, 100)
	if r != 0 {
		t.Errorf("expected 0 for zero ATR, got %.2f", r)
	}
}
