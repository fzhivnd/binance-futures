package market

import (
	"testing"
	"time"

	"futures/internal/domain"
)

func makeCandle(symbol string, tf domain.Timeframe, openTime time.Time, close float64) domain.Candle {
	return domain.Candle{
		Symbol:    symbol,
		Timeframe: tf,
		OpenTime:  openTime,
		Close:     close,
		IsClosed:  true,
	}
}

func TestCandleStore_GetEmpty(t *testing.T) {
	s := NewCandleStore()
	got := s.Get("BTCUSDT", domain.Timeframe5m, 10)
	if got != nil {
		t.Errorf("expected nil for unknown symbol, got %v", got)
	}
}

func TestCandleStore_AppendAndGet(t *testing.T) {
	s := NewCandleStore()
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := range 5 {
		s.Update(makeCandle("BTCUSDT", domain.Timeframe5m, base.Add(time.Duration(i)*5*time.Minute), float64(i+1)))
	}

	got := s.Get("BTCUSDT", domain.Timeframe5m, 3)
	if len(got) != 3 {
		t.Fatalf("got %d candles, want 3", len(got))
	}
	// should be the last 3 candles (close = 3, 4, 5)
	if got[0].Close != 3 || got[2].Close != 5 {
		t.Errorf("unexpected candle values: %v", got)
	}
}

func TestCandleStore_UpdateCurrentCandle(t *testing.T) {
	s := NewCandleStore()
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	s.Update(makeCandle("ETHUSDT", domain.Timeframe1h, t0, 3000.0))
	// same OpenTime → update in place
	updated := makeCandle("ETHUSDT", domain.Timeframe1h, t0, 3100.0)
	s.Update(updated)

	got := s.Get("ETHUSDT", domain.Timeframe1h, 10)
	if len(got) != 1 {
		t.Fatalf("expected 1 candle after update, got %d", len(got))
	}
	if got[0].Close != 3100.0 {
		t.Errorf("close not updated: got %v, want 3100", got[0].Close)
	}
}

func TestCandleStore_LimitCap(t *testing.T) {
	s := NewCandleStore()
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// insert more than maxCandlesPerSeries (200)
	for i := range 210 {
		s.Update(makeCandle("BTCUSDT", domain.Timeframe15m, base.Add(time.Duration(i)*15*time.Minute), float64(i)))
	}

	got := s.Get("BTCUSDT", domain.Timeframe15m, 200)
	if len(got) != 200 {
		t.Errorf("expected 200 candles (cap), got %d", len(got))
	}
	// oldest retained should be index 10 (close = 10.0)
	if got[0].Close != 10.0 {
		t.Errorf("oldest candle close: got %v, want 10", got[0].Close)
	}
}

func TestCandleStore_LimitGetClamp(t *testing.T) {
	s := NewCandleStore()
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := range 3 {
		s.Update(makeCandle("SOLUSDT", domain.Timeframe5m, base.Add(time.Duration(i)*5*time.Minute), float64(i)))
	}

	got := s.Get("SOLUSDT", domain.Timeframe5m, 100)
	if len(got) != 3 {
		t.Errorf("limit clamped incorrectly: got %d, want 3", len(got))
	}
}

func TestCandleStore_IndependentByTimeframe(t *testing.T) {
	s := NewCandleStore()
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	s.Update(makeCandle("BTCUSDT", domain.Timeframe5m, t0, 100.0))
	s.Update(makeCandle("BTCUSDT", domain.Timeframe1h, t0, 200.0))

	got5m := s.Get("BTCUSDT", domain.Timeframe5m, 10)
	got1h := s.Get("BTCUSDT", domain.Timeframe1h, 10)

	if got5m[0].Close != 100.0 || got1h[0].Close != 200.0 {
		t.Errorf("timeframe data mixed up")
	}
}
