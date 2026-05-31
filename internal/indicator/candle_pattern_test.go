package indicator

import (
	"testing"

	"futures/internal/domain"
)

func candle(o, h, l, c float64) domain.Candle {
	return domain.Candle{Open: o, High: h, Low: l, Close: c, IsClosed: true}
}

//func TestDetectPatterns_ShootingStar(t *testing.T) {
//	// uptrend (5 candles) then shooting star — satisfies 1h lookback of 4
//	candles := []domain.Candle{
//		candle(88, 90, 87, 89),  // green
//		candle(89, 91, 88, 90),  // green
//		candle(90, 92, 89, 91),  // green
//		candle(91, 93, 90, 92),  // p: green
//		candle(92, 100, 92, 93), // c: tiny body, huge upper wick
//	}
//	sigs := DetectPatterns(candles, domain.Timeframe1h, 0, 0)
//	found := false
//	for _, s := range sigs {
//		if s.Pattern == domain.PatternShootingStar {
//			found = true
//		}
//	}
//	if !found {
//		t.Error("expected ShootingStar")
//	}
//}

func TestDetectPatterns_BearishEngulfing(t *testing.T) {
	candles := []domain.Candle{
		candle(90, 92, 89, 91),
		candle(91, 93, 90, 93), // green
		candle(94, 95, 88, 89), // red, engulfs previous
	}
	sigs := DetectPatterns(candles, domain.Timeframe1h, 0, 0)
	found := false
	for _, s := range sigs {
		if s.Pattern == domain.PatternBearishEngulfing {
			found = true
		}
	}
	if !found {
		t.Error("expected BearishEngulfing")
	}
}

func TestDetectPatterns_NoSignal(t *testing.T) {
	// small doji, no trend
	candles := []domain.Candle{
		candle(100, 101, 99, 100),
		candle(100, 101, 99, 100),
		candle(100, 101, 99, 100),
	}
	sigs := DetectPatterns(candles, domain.Timeframe1h, 0, 0)
	if len(sigs) != 0 {
		t.Errorf("expected no signals, got %v", sigs)
	}
}

func TestDetectPatterns_InsufficientCandles(t *testing.T) {
	sigs := DetectPatterns([]domain.Candle{candle(100, 101, 99, 100)}, domain.Timeframe1h, 0, 0)
	if len(sigs) != 0 {
		t.Error("expected no signals for < 3 candles")
	}
}
