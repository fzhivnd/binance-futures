package risk

import "sync"

// DrawdownTracker tracks peak balance and current drawdown percentage.
type DrawdownTracker struct {
	mu             sync.Mutex
	peakBalance    float64
	currentBalance float64
}

func NewDrawdownTracker(initialBalance float64) *DrawdownTracker {
	return &DrawdownTracker{
		peakBalance:    initialBalance,
		currentBalance: initialBalance,
	}
}

func (d *DrawdownTracker) Update(newBalance float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.currentBalance = newBalance
	if newBalance > d.peakBalance {
		d.peakBalance = newBalance
	}
}

func (d *DrawdownTracker) CurrentDrawdownPct() float64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.peakBalance == 0 {
		return 0
	}
	return (d.peakBalance - d.currentBalance) / d.peakBalance * 100
}
