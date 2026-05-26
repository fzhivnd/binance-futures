package market

import (
	"sort"
	"sync"
	"time"

	"futures/internal/domain"
)

type fundingEntry struct {
	rate          float64
	nextFunding   time.Time
	markPrice     float64
	updatedAt     time.Time
	intervalHours int // from /fapi/v1/fundingInfo; 0 means unknown (default 8)
}

type FundingCache struct {
	mu   sync.RWMutex
	data map[string]*fundingEntry
}

func NewFundingCache() *FundingCache {
	return &FundingCache{data: make(map[string]*fundingEntry)}
}

func (c *FundingCache) Update(symbol string, rate float64, nextFunding time.Time, markPrice float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.data[symbol]; ok {
		// preserve intervalHours set from funding info
		e.rate = rate
		e.nextFunding = nextFunding
		e.markPrice = markPrice
		e.updatedAt = time.Now()
	} else {
		c.data[symbol] = &fundingEntry{
			rate:        rate,
			nextFunding: nextFunding,
			markPrice:   markPrice,
			updatedAt:   time.Now(),
		}
	}
}

// SetFundingInterval stores the Binance-provided funding interval for a symbol.
func (c *FundingCache) SetFundingInterval(symbol string, intervalHours int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.data[symbol]; ok {
		e.intervalHours = intervalHours
	} else {
		c.data[symbol] = &fundingEntry{intervalHours: intervalHours}
	}
}

// GetIntervalHours returns the funding interval in hours for a symbol (defaults to 8).
func (c *FundingCache) GetIntervalHours(symbol string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if e, ok := c.data[symbol]; ok && e.intervalHours > 0 {
		return e.intervalHours
	}
	return 8
}

func (c *FundingCache) GetRate(symbol string) (float64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.data[symbol]
	if !ok {
		return 0, false
	}
	return e.rate, true
}

func (c *FundingCache) GetInfo(symbol string) (*domain.FundingRate, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.data[symbol]
	if !ok {
		return nil, false
	}
	return &domain.FundingRate{
		Symbol:      symbol,
		Rate:        e.rate,
		NextFunding: e.nextFunding,
		MarkPrice:   e.markPrice,
		UpdatedAt:   e.updatedAt,
	}, true
}

func (c *FundingCache) GetAll() map[string]float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]float64, len(c.data))
	for k, v := range c.data {
		out[k] = v.rate
	}
	return out
}

func (c *FundingCache) TopNegative(n int) []string {
	c.mu.RLock()
	type kv struct {
		symbol string
		rate   float64
	}
	var entries []kv
	for s, e := range c.data {
		if e.rate < 0 {
			entries = append(entries, kv{s, e.rate})
		}
	}
	c.mu.RUnlock()

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].rate < entries[j].rate // most negative first
	})

	if n > len(entries) {
		n = len(entries)
	}
	symbols := make([]string, n)
	for i := 0; i < n; i++ {
		symbols[i] = entries[i].symbol
	}
	return symbols
}
