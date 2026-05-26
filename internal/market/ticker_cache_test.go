package market

import "testing"

func TestTickerCache_UpdateAndGet(t *testing.T) {
	c := NewTickerCache()

	c.Update("BTCUSDT", 50000.0)
	p, ok := c.GetPrice("BTCUSDT")
	if !ok {
		t.Fatal("expected price to exist")
	}
	if p != 50000.0 {
		t.Errorf("got %v, want 50000", p)
	}
}

func TestTickerCache_MissingSymbol(t *testing.T) {
	c := NewTickerCache()
	_, ok := c.GetPrice("UNKNOWN")
	if ok {
		t.Error("expected false for unknown symbol")
	}
}

func TestTickerCache_Overwrite(t *testing.T) {
	c := NewTickerCache()
	c.Update("ETHUSDT", 3000.0)
	c.Update("ETHUSDT", 3200.0)

	p, _ := c.GetPrice("ETHUSDT")
	if p != 3200.0 {
		t.Errorf("got %v, want 3200 after overwrite", p)
	}
}
