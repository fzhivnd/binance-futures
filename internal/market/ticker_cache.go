package market

import "sync"

type TickerCache struct {
	mu   sync.RWMutex
	data map[string]float64
}

func NewTickerCache() *TickerCache {
	return &TickerCache{data: make(map[string]float64)}
}

func (c *TickerCache) Update(symbol string, price float64) {
	c.mu.Lock()
	c.data[symbol] = price
	c.mu.Unlock()
}

func (c *TickerCache) GetPrice(symbol string) (float64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.data[symbol]
	return p, ok
}
