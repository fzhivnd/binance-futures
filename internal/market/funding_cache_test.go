package market

import (
	"testing"
	"time"
)

func TestFundingCache_UpdateAndGet(t *testing.T) {
	c := NewFundingCache()
	now := time.Now()

	c.Update("BTCUSDT", -0.01, now.Add(4*time.Hour), 50000.0)

	rate, ok := c.GetRate("BTCUSDT")
	if !ok {
		t.Fatal("expected rate to exist")
	}
	if rate != -0.01 {
		t.Errorf("got rate %v, want -0.01", rate)
	}

	_, ok = c.GetRate("ETHUSDT")
	if ok {
		t.Error("expected missing symbol to return false")
	}
}

func TestFundingCache_UpdatePreservesInterval(t *testing.T) {
	c := NewFundingCache()
	c.SetFundingInterval("BTCUSDT", 4)
	c.Update("BTCUSDT", -0.005, time.Now(), 50000.0)

	h := c.GetIntervalHours("BTCUSDT")
	if h != 4 {
		t.Errorf("interval overwritten: got %d, want 4", h)
	}
}

func TestFundingCache_GetIntervalDefault(t *testing.T) {
	c := NewFundingCache()
	h := c.GetIntervalHours("UNKNOWN")
	if h != 8 {
		t.Errorf("got %d, want 8 (default)", h)
	}
}

func TestFundingCache_GetInfo(t *testing.T) {
	c := NewFundingCache()
	next := time.Now().Add(4 * time.Hour).Truncate(time.Second)
	c.Update("ETHUSDT", -0.003, next, 3000.0)

	info, ok := c.GetInfo("ETHUSDT")
	if !ok {
		t.Fatal("expected info to exist")
	}
	if info.Symbol != "ETHUSDT" || info.Rate != -0.003 || info.MarkPrice != 3000.0 {
		t.Errorf("unexpected info: %+v", info)
	}
}

func TestFundingCache_GetAll(t *testing.T) {
	c := NewFundingCache()
	c.Update("BTCUSDT", -0.01, time.Now(), 50000.0)
	c.Update("ETHUSDT", -0.005, time.Now(), 3000.0)
	c.Update("SOLUSDT", 0.001, time.Now(), 150.0)

	all := c.GetAll()
	if len(all) != 3 {
		t.Fatalf("got %d entries, want 3", len(all))
	}
	if all["BTCUSDT"] != -0.01 {
		t.Errorf("BTCUSDT rate mismatch")
	}
}

func TestFundingCache_TopNegative(t *testing.T) {
	c := NewFundingCache()
	c.Update("A", -0.01, time.Now(), 1.0)
	c.Update("B", -0.05, time.Now(), 1.0)
	c.Update("C", -0.02, time.Now(), 1.0)
	c.Update("D", 0.001, time.Now(), 1.0) // positive — excluded

	top := c.TopNegative(2)
	if len(top) != 2 {
		t.Fatalf("got %d results, want 2", len(top))
	}
	if top[0] != "B" {
		t.Errorf("most negative should be B, got %s", top[0])
	}
	if top[1] != "C" {
		t.Errorf("second most negative should be C, got %s", top[1])
	}
}

func TestFundingCache_TopNegative_FewerThanN(t *testing.T) {
	c := NewFundingCache()
	c.Update("A", -0.01, time.Now(), 1.0)

	top := c.TopNegative(10)
	if len(top) != 1 {
		t.Errorf("got %d, want 1", len(top))
	}
}

func TestFundingCache_TopNegative_NoneNegative(t *testing.T) {
	c := NewFundingCache()
	c.Update("A", 0.01, time.Now(), 1.0)

	top := c.TopNegative(5)
	if len(top) != 0 {
		t.Errorf("expected empty slice, got %v", top)
	}
}
