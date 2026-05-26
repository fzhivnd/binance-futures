package execution

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"futures/internal/domain"
	"futures/internal/exchange"
)

type LiveExecutor struct {
	client *exchange.BinanceClient
}

func NewLiveExecutor(client *exchange.BinanceClient) *LiveExecutor {
	return &LiveExecutor{client: client}
}

func (l *LiveExecutor) PlaceMarketOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error) {
	resp, err := l.client.NewOrder(ctx, exchange.NewOrderRequest{
		Symbol:   req.Symbol,
		Side:     string(req.Side),
		Type:     "MARKET",
		Quantity: formatQty(req.Quantity),
	})
	if err != nil {
		return nil, fmt.Errorf("binance market order: %w", err)
	}
	return &domain.OrderResult{
		OrderID:   strconv.FormatInt(resp.OrderID, 10),
		Symbol:    resp.Symbol,
		Side:      domain.Side(resp.Side),
		FillPrice: resp.AvgPrice,
		Quantity:  resp.ExecutedQty,
		Status:    resp.Status,
		IsPaper:   false,
		Timestamp: time.UnixMilli(resp.UpdateTime),
	}, nil
}

func (l *LiveExecutor) PlaceLimitOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error) {
	resp, err := l.client.NewOrder(ctx, exchange.NewOrderRequest{
		Symbol:   req.Symbol,
		Side:     string(req.Side),
		Type:     "LIMIT",
		Quantity: formatQty(req.Quantity),
		Price:    formatQty(req.Price),
	})
	if err != nil {
		return nil, fmt.Errorf("binance limit order: %w", err)
	}
	return &domain.OrderResult{
		OrderID:   strconv.FormatInt(resp.OrderID, 10),
		Symbol:    resp.Symbol,
		Side:      domain.Side(resp.Side),
		FillPrice: resp.AvgPrice,
		Quantity:  resp.ExecutedQty,
		Status:    resp.Status,
		IsPaper:   false,
		Timestamp: time.UnixMilli(resp.UpdateTime),
	}, nil
}

func (l *LiveExecutor) PlaceStopLimitOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error) {
	resp, err := l.client.NewOrder(ctx, exchange.NewOrderRequest{
		Symbol:     req.Symbol,
		Side:       string(req.Side),
		Type:       string(req.Type),
		Quantity:   formatQty(req.Quantity),
		Price:      formatQty(req.Price),
		StopPrice:  formatQty(req.StopPrice),
		ReduceOnly: req.ReduceOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("binance stop-limit order: %w", err)
	}
	return &domain.OrderResult{
		OrderID:   strconv.FormatInt(resp.OrderID, 10),
		Symbol:    resp.Symbol,
		Side:      domain.Side(resp.Side),
		FillPrice: resp.AvgPrice,
		Quantity:  resp.ExecutedQty,
		Status:    resp.Status,
		IsPaper:   false,
		Timestamp: time.UnixMilli(resp.UpdateTime),
	}, nil
}

func (l *LiveExecutor) CancelOrder(ctx context.Context, symbol string, orderID string) error {
	return l.client.CancelOrder(ctx, symbol, orderID)
}

func (l *LiveExecutor) GetPosition(ctx context.Context, symbol string) (*domain.Position, error) {
	risk, err := l.client.GetPositionRisk(ctx, symbol)
	if err != nil {
		return nil, err
	}
	if risk == nil || risk.PositionAmt == 0 {
		return nil, nil // no open position
	}
	side := domain.SideSell
	if risk.PositionAmt > 0 {
		side = domain.SideBuy
	}
	qty := risk.PositionAmt
	if qty < 0 {
		qty = -qty
	}
	return &domain.Position{
		Symbol:     risk.Symbol,
		Side:       side,
		EntryPrice: risk.EntryPrice,
		Quantity:   qty,
		MarkPrice:  risk.MarkPrice,
	}, nil
}

func (l *LiveExecutor) GetAccountBalance(ctx context.Context) (*domain.Balance, error) {
	resp, err := l.client.GetAccount(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range resp.Assets {
		if a.Asset == "USDT" {
			return &domain.Balance{
				Asset:            "USDT",
				TotalBalance:     a.WalletBalance,
				AvailableBalance: a.AvailableBalance,
			}, nil
		}
	}
	return &domain.Balance{Asset: "USDT"}, nil
}

func (l *LiveExecutor) SetLeverage(ctx context.Context, symbol string, leverage int) error {
	return l.client.SetLeverage(ctx, symbol, leverage)
}

func formatQty(v float64) string {
	return strconv.FormatFloat(v, 'f', 8, 64)
}
