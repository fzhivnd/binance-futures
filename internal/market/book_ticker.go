package market

import (
	"sync"
	"time"
)

// BookTicker holds the best bid/ask for a symbol from the @bookTicker stream.
type BookTicker struct {
	BidPrice  float64
	BidQty    float64
	AskPrice  float64
	AskQty    float64
	UpdatedAt time.Time
}

// BookTickerCache stores the latest BookTicker per symbol. Thread-safe.
type BookTickerCache struct {
	mu   sync.RWMutex
	data map[string]*BookTicker
}

func NewBookTickerCache() *BookTickerCache {
	return &BookTickerCache{data: make(map[string]*BookTicker)}
}

func (c *BookTickerCache) Update(symbol string, bt *BookTicker) {
	c.mu.Lock()
	c.data[symbol] = bt
	c.mu.Unlock()
}

func (c *BookTickerCache) Get(symbol string) (*BookTicker, bool) {
	c.mu.RLock()
	bt, ok := c.data[symbol]
	c.mu.RUnlock()
	return bt, ok
}

// SpreadBps returns the bid-ask spread in basis points, or false if not available.
func (c *BookTickerCache) SpreadBps(symbol string) (float64, bool) {
	bt, ok := c.Get(symbol)
	if !ok || bt.BidPrice == 0 || bt.AskPrice == 0 {
		return 0, false
	}
	return (bt.AskPrice - bt.BidPrice) / bt.BidPrice * 10000, true
}

// BidDepthUSDT returns bestBidQty * bestBidPrice, or false if not available.
func (c *BookTickerCache) BidDepthUSDT(symbol string) (float64, bool) {
	bt, ok := c.Get(symbol)
	if !ok || bt.BidPrice == 0 {
		return 0, false
	}
	return bt.BidQty * bt.BidPrice, true
}
