package market

import (
	"sync"

	"futures/internal/domain"
)

const maxCandlesPerSeries = 200

type seriesKey struct {
	symbol    string
	timeframe domain.Timeframe
}

type CandleStore struct {
	mu   sync.RWMutex
	data map[seriesKey][]domain.Candle
}

func NewCandleStore() *CandleStore {
	return &CandleStore{data: make(map[seriesKey][]domain.Candle)}
}

func (s *CandleStore) Update(c domain.Candle) {
	key := seriesKey{c.Symbol, c.Timeframe}
	s.mu.Lock()
	defer s.mu.Unlock()

	candles := s.data[key]
	if len(candles) > 0 {
		last := &candles[len(candles)-1]
		if last.OpenTime.Equal(c.OpenTime) {
			// update current (open) candle
			*last = c
			return
		}
	}
	// append new candle
	candles = append(candles, c)
	if len(candles) > maxCandlesPerSeries {
		candles = candles[len(candles)-maxCandlesPerSeries:]
	}
	s.data[key] = candles
}

func (s *CandleStore) Get(symbol string, tf domain.Timeframe, limit int) []domain.Candle {
	key := seriesKey{symbol, tf}
	s.mu.RLock()
	defer s.mu.RUnlock()
	candles := s.data[key]
	if len(candles) == 0 {
		return nil
	}
	if limit > len(candles) {
		limit = len(candles)
	}
	result := make([]domain.Candle, limit)
	copy(result, candles[len(candles)-limit:])
	return result
}
