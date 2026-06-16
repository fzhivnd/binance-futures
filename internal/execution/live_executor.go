package execution

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"futures/internal/domain"
	"futures/internal/exchange"
)

type LiveExecutor struct {
	client       *exchange.BinanceClient
	exchangeInfo *exchange.ExchangeInfoCache
}

type symbolRule struct {
	StepSize float64
	TickSize float64
}

func NewLiveExecutor(client *exchange.BinanceClient, exchangeInfo *exchange.ExchangeInfoCache) *LiveExecutor {
	return &LiveExecutor{client: client, exchangeInfo: exchangeInfo}
}

func (l *LiveExecutor) PlaceMarketOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error) {
	resp, err := l.client.NewOrder(ctx, exchange.NewOrderRequest{
		Symbol:           req.Symbol,
		Side:             string(req.Side),
		Type:             string(domain.OrderTypeMarket),
		Quantity:         l.formatQty(req.Symbol, req.Quantity),
		NewClientOrderId: req.NewClientOrderID,
		ReduceOnly:       req.ReduceOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("binance market order: %w", err)
	}
	return &domain.OrderResult{
		OrderID:       strconv.FormatInt(resp.OrderID, 10),
		Symbol:        resp.Symbol,
		Side:          domain.Side(resp.Side),
		FillPrice:     resp.AvgPrice,
		Quantity:      resp.ExecutedQty,
		Status:        resp.Status,
		IsPaper:       false,
		Timestamp:     time.UnixMilli(resp.UpdateTime),
		ClientOrderId: resp.ClientOrderId,
	}, nil
}

func (l *LiveExecutor) PlaceLimitOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error) {
	resp, err := l.client.NewOrder(ctx, exchange.NewOrderRequest{
		Symbol:   req.Symbol,
		Side:     string(req.Side),
		Type:     "LIMIT",
		Quantity: l.formatQty(req.Symbol, req.Quantity),
		Price:    l.formatPrice(req.Symbol, req.Price),
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
	resp, err := l.client.NewAlgoOrder(ctx, exchange.NewOrderRequest{
		Symbol:       req.Symbol,
		Side:         string(req.Side),
		Type:         string(req.Type),
		Quantity:     l.formatQty(req.Symbol, req.Quantity),
		TriggerPrice: l.formatPrice(req.Symbol, req.TriggerPrice),
		Price:        l.formatPrice(req.Symbol, req.Price),
		ReduceOnly:   req.ReduceOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("binance stop-limit order: %w", err)
	}
	return &domain.OrderResult{
		OrderID:       strconv.FormatInt(resp.AlgoId, 10),
		Symbol:        resp.Symbol,
		Side:          domain.Side(resp.Side),
		FillPrice:     resp.Price,
		Quantity:      resp.Quantity,
		Status:        resp.AlgoStatus,
		IsPaper:       false,
		Timestamp:     time.UnixMilli(resp.UpdateTime),
		ClientOrderId: resp.ClientAlgoId,
	}, nil
}

func (l *LiveExecutor) PlaceStopMarketOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error) {
	resp, err := l.client.NewAlgoOrder(ctx, exchange.NewOrderRequest{
		Symbol:        req.Symbol,
		Side:          string(req.Side),
		Type:          string(req.Type),
		ClosePosition: req.ClosePosition,
		TriggerPrice:  l.formatPrice(req.Symbol, req.TriggerPrice),
	})
	if err != nil {
		return nil, fmt.Errorf("binance stop-market order: %w", err)
	}
	return &domain.OrderResult{
		OrderID:       strconv.FormatInt(resp.AlgoId, 10),
		Symbol:        resp.Symbol,
		Side:          domain.Side(resp.Side),
		FillPrice:     resp.Price,
		Quantity:      resp.Quantity,
		Status:        resp.AlgoStatus,
		IsPaper:       false,
		Timestamp:     time.UnixMilli(resp.UpdateTime),
		ClientOrderId: resp.ClientAlgoId,
	}, nil
}

func (l *LiveExecutor) PlaceTrailingStopOrder(ctx context.Context, req TrailingStopRequest) (*domain.OrderResult, error) {
	resp, err := l.client.NewAlgoOrder(ctx, exchange.NewOrderRequest{
		Symbol:       req.Symbol,
		Side:         string(req.Side),
		Type:         string(domain.OrderTypeTrailingStop),
		Quantity:     l.formatQty(req.Symbol, req.Quantity),
		ReduceOnly:   req.ReduceOnly,
		CallbackRate: fmt.Sprintf("%.2f", req.CallbackRate),
	})
	if err != nil {
		return nil, fmt.Errorf("binance trailing stop order: %w", err)
	}
	return &domain.OrderResult{
		OrderID:       strconv.FormatInt(resp.AlgoId, 10),
		Symbol:        resp.Symbol,
		Side:          domain.Side(resp.Side),
		FillPrice:     resp.Price,
		Quantity:      resp.Quantity,
		Status:        resp.AlgoStatus,
		IsPaper:       false,
		Timestamp:     time.UnixMilli(resp.UpdateTime),
		ClientOrderId: resp.ClientAlgoId,
	}, nil
}

func (l *LiveExecutor) CancelOrder(ctx context.Context, symbol string, orderID string) error {
	err := l.client.CancelOrder(ctx, symbol, "", orderID)
	if err != nil {
		if strings.Contains(err.Error(), "-2011") {
			algoErr := l.client.CancelAlgoOrder(ctx, "", orderID)
			if algoErr != nil {
				return algoErr
			}
			return nil
		}
		return err
	}
	return nil
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
		Symbol:      risk.Symbol,
		Side:        side,
		EntryPrice:  risk.EntryPrice,
		Quantity:    qty,
		OriginalQty: qty,
		MarkPrice:   risk.MarkPrice,
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
	return nil, fmt.Errorf("USDT asset not found in account response")
}

func (l *LiveExecutor) SetLeverage(ctx context.Context, symbol string, leverage int) error {
	return l.client.SetLeverage(ctx, symbol, leverage)
}

func (l *LiveExecutor) formatQty(symbol string, v float64) string {
	step := l.rules(symbol).StepSize
	v = roundDown(v, step)
	precision := getQuantityPrecision(step)
	return strconv.FormatFloat(v, 'f', precision, 64)
}

func (l *LiveExecutor) formatPrice(symbol string, v float64) string {
	tick := l.rules(symbol).TickSize
	if tick == 0 {
		return strconv.FormatFloat(v, 'f', 2, 64) // Safe default
	}
	v = roundDown(v, tick)
	precision := getPricePrecision(tick)
	return strconv.FormatFloat(v, 'f', precision, 64)
}

func roundDown(value, step float64) float64 {
	if step == 0 {
		return value
	}
	return math.Floor(value/step) * step
}

func getPricePrecision(tick float64) int {
	tickStr := strconv.FormatFloat(tick, 'f', 10, 64)

	tickStr = strings.TrimRight(tickStr, "0")

	if strings.Contains(tickStr, ".") {
		parts := strings.Split(tickStr, ".")
		return len(parts[1])
	}

	return 0
}

func getQuantityPrecision(step float64) int {
	s := strconv.FormatFloat(step, 'f', -1, 64)

	idx := strings.IndexByte(s, '.')
	if idx == -1 {
		return 0
	}

	return len(s[idx+1:])
}

func (l *LiveExecutor) rules(symbol string) symbolRule {
	r, ok := l.exchangeInfo.Symbols[symbol]
	if !ok {
		return symbolRule{}
	}

	return symbolRule{
		StepSize: r.StepSize,
		TickSize: r.TickSize,
	}
}
