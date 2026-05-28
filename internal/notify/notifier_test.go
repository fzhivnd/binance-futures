package notify

import (
	"testing"

	"futures/internal/domain"
)

func TestNotifier_DisabledNoOp(t *testing.T) {
	n := NewNotifier(false, "", "", 0)
	if n.enabled {
		t.Fatal("expected notifier to be disabled")
	}
	// Should not panic or send anything.
	n.NotifyRiskEvent(nil, RiskEvent{Type: "kill_switch", Message: "test"})
}

func TestFormatDailySummary_ZeroTrades(t *testing.T) {
	s := &domain.DailySummary{TradeCount: 0, IsPaper: true}
	msg := FormatDailySummary(s)
	if msg == "" {
		t.Fatal("expected non-empty message")
	}
}

func TestEscapeMarkdownV2(t *testing.T) {
	got := escapeMarkdownV2("hello.world!")
	want := "hello\\.world\\!"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
