package risk

import (
	"testing"
)

func TestDrawdown_InitialZero(t *testing.T) {
	d := NewDrawdownTracker(1000)
	if d.CurrentDrawdownPct() != 0 {
		t.Error("expected 0 initial drawdown")
	}
}

func TestDrawdown_Recovery(t *testing.T) {
	d := NewDrawdownTracker(1000)
	d.Update(800)
	if d.CurrentDrawdownPct() != 20 {
		t.Errorf("expected 20%% drawdown, got %.2f", d.CurrentDrawdownPct())
	}
	d.Update(1000)
	if d.CurrentDrawdownPct() != 0 {
		t.Errorf("expected 0%% after recovery, got %.2f", d.CurrentDrawdownPct())
	}
}

func TestDrawdown_PeakTracking(t *testing.T) {
	d := NewDrawdownTracker(1000)
	d.Update(1200) // new peak
	d.Update(1000) // pull back
	expected := (1200 - 1000) / 1200.0 * 100
	got := d.CurrentDrawdownPct()
	// compare with tolerance for floating point
	if got < expected-0.01 || got > expected+0.01 {
		t.Errorf("expected ~%.2f%%, got %.2f%%", expected, got)
	}
}

func TestDrawdown_ExactThreshold(t *testing.T) {
	d := NewDrawdownTracker(1000)
	d.Update(900)
	if d.CurrentDrawdownPct() != 10 {
		t.Errorf("expected exactly 10%% drawdown, got %.2f", d.CurrentDrawdownPct())
	}
}
