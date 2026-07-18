package notify

import (
	"context"
	"log/slog"
	"time"

	"futures/internal/domain"
)

type TradeOpenedEvent struct {
	Symbol      string
	Side        string
	EntryPrice  float64
	Quantity    float64
	Leverage    int
	StopLoss    float64
	TakeProfit  float64
	EntryMode   string
	Confidence  int
	IsPaper     bool
	OpenedAt    time.Time
	FundingRate float64
}

type TradeClosedEvent struct {
	Symbol       string
	Side         string
	EntryPrice   float64
	ExitPrice    float64
	PnL          float64
	PnLPct       float64
	Result       string
	CloseReason  string
	HoldDuration string
	IsPaper      bool
}

type RiskEvent struct {
	Type    string
	Message string
}

type FundingEvent struct {
	FeePaid     float64
	Symbol      string
	FundingRate float64
}

type Tp1Event struct {
	Symbol      string
	ClosedAt    time.Time
	AvgPrice    float64
	PnLPct      float64 // price move % on the TP1 leg (e.g. 2.0 = 2%)
	PnLROI      float64 // leveraged ROI % (e.g. 20.0 = 20%)
	SecuredUSDT float64 // realised profit in USDT from the TP1 leg
}

// Notifier dispatches trade/risk notifications to Telegram. All methods are
// fire-and-forget; failures are logged but never propagate to callers.
type Notifier struct {
	telegram *TelegramClient
	enabled  bool
}

func NewNotifier(enabled bool, botToken, chatID string, timeoutSecs int) *Notifier {
	if !enabled || botToken == "" || chatID == "" {
		return &Notifier{enabled: false}
	}
	return &Notifier{
		telegram: NewTelegramClient(botToken, chatID, timeoutSecs),
		enabled:  true,
	}
}

func (n *Notifier) NotifyTradeOpened(ctx context.Context, event TradeOpenedEvent) {
	if !n.enabled {
		return
	}
	go func() {
		msg := FormatTradeOpened(event)
		if err := n.telegram.SendMessage(ctx, msg); err != nil {
			slog.Warn("telegram: trade opened notification failed", "symbol", event.Symbol, "error", err)
		}
	}()
}

func (n *Notifier) NotifyTradeClosed(ctx context.Context, event TradeClosedEvent) {
	if !n.enabled {
		return
	}
	go func() {
		msg := FormatTradeClosed(event)
		if err := n.telegram.SendMessage(ctx, msg); err != nil {
			slog.Warn("telegram: trade closed notification failed", "symbol", event.Symbol, "error", err)
		}
	}()
}

func (n *Notifier) NotifyDailySummary(ctx context.Context, s *domain.DailySummary) {
	if !n.enabled {
		return
	}
	go func() {
		msg := FormatDailySummary(s)
		if err := n.telegram.SendMessage(ctx, msg); err != nil {
			slog.Warn("telegram: daily summary notification failed", "error", err)
		}
	}()
}

func (n *Notifier) NotifyRiskEvent(ctx context.Context, event RiskEvent) {
	if !n.enabled {
		return
	}
	go func() {
		msg := FormatRiskEvent(event)
		if err := n.telegram.SendMessage(ctx, msg); err != nil {
			slog.Warn("telegram: risk event notification failed", "type", event.Type, "error", err)
		}
	}()
}

func (n *Notifier) NotifyFundingSettlement(ctx context.Context, event FundingEvent) {
	if !n.enabled {
		return
	}
	go func() {
		msg := FormatFundingEvent(event)
		if err := n.telegram.SendMessage(ctx, msg); err != nil {
			slog.Warn("telegram: funding event notification failed", "error", err)
		}
	}()
}

func (n *Notifier) NotifyTp1Event(ctx context.Context, event Tp1Event) {
	if !n.enabled {
		return
	}
	go func() {
		msg := FormatTp1Event(event)
		if err := n.telegram.SendMessage(ctx, msg); err != nil {
			slog.Warn("telegram: tp1 event notification failed", "error", err)
		}
	}()
}
