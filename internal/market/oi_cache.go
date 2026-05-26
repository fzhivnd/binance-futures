package market

import (
	"sync"
	"time"

	"futures/internal/indicator"
)

const oiHistoryMaxSize = 288 // 24h at 5-min intervals

type OICache struct {
	mu      sync.RWMutex
	current map[string]float64
	history *indicator.OIHistory
}

func NewOICache() *OICache {
	return &OICache{
		current: make(map[string]float64),
		history: indicator.NewOIHistory(oiHistoryMaxSize),
	}
}

func (c *OICache) Update(symbol string, oi float64) {
	c.mu.Lock()
	c.current[symbol] = oi
	c.mu.Unlock()
	c.history.Add(symbol, oi)
}

func (c *OICache) Get(symbol string) (float64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.current[symbol]
	return v, ok
}

// Delta returns the OI percentage change over the given window.
func (c *OICache) Delta(symbol string, window time.Duration) (float64, error) {
	return c.history.Delta(symbol, window)
}

// History returns the underlying OIHistory for use by the indicator engine.
func (c *OICache) History() *indicator.OIHistory {
	return c.history
}
