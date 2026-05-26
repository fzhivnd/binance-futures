package indicator

import (
	"fmt"
	"sync"
	"time"
)

type OISnapshot struct {
	OI        float64
	Timestamp time.Time
}

// OIHistory stores a ring buffer of OI snapshots per symbol (24h at 5-min intervals = 288 entries).
type OIHistory struct {
	mu       sync.RWMutex
	snapshots map[string][]OISnapshot
	maxSize  int
}

func NewOIHistory(maxSize int) *OIHistory {
	return &OIHistory{
		snapshots: make(map[string][]OISnapshot),
		maxSize:   maxSize,
	}
}

func (h *OIHistory) Add(symbol string, oi float64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	snap := OISnapshot{OI: oi, Timestamp: time.Now()}
	snaps := h.snapshots[symbol]
	snaps = append(snaps, snap)
	if len(snaps) > h.maxSize {
		snaps = snaps[len(snaps)-h.maxSize:]
	}
	h.snapshots[symbol] = snaps
}

// Purge removes all OI history for a symbol (called when it leaves the top-N watchlist).
func (h *OIHistory) Purge(symbol string) {
	h.mu.Lock()
	delete(h.snapshots, symbol)
	h.mu.Unlock()
}

// Delta returns (current - past) / past * 100 over the given window.
func (h *OIHistory) Delta(symbol string, window time.Duration) (float64, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	snaps, ok := h.snapshots[symbol]
	if !ok || len(snaps) == 0 {
		return 0, fmt.Errorf("no OI data for %s", symbol)
	}

	current := snaps[len(snaps)-1]
	cutoff := time.Now().Add(-window)

	var past OISnapshot
	for i := len(snaps) - 1; i >= 0; i-- {
		if snaps[i].Timestamp.Before(cutoff) {
			past = snaps[i]
			break
		}
	}

	if past.OI == 0 {
		return 0, fmt.Errorf("insufficient OI history for %s at window %v", symbol, window)
	}

	return (current.OI - past.OI) / past.OI * 100, nil
}
