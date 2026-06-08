package telemetry

import (
	"context"
	"time"

	"futures/internal/domain"
	"futures/internal/execution"
)

type InstrumentedExecutor struct {
	inner execution.Executor
}

func NewInstrumentedExecutor(e execution.Executor) execution.Executor {
	return &InstrumentedExecutor{inner: e}
}

func (i *InstrumentedExecutor) PlaceMarketOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error) {
	start := time.Now()
	res, err := i.inner.PlaceMarketOrder(ctx, req)
	record("Executor.PlaceMarketOrder", time.Since(start), err,
		"symbol", req.Symbol, "side", req.Side, "qty", req.Quantity)
	return res, err
}

func (i *InstrumentedExecutor) PlaceLimitOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error) {
	start := time.Now()
	res, err := i.inner.PlaceLimitOrder(ctx, req)
	record("Executor.PlaceLimitOrder", time.Since(start), err,
		"symbol", req.Symbol, "side", req.Side, "qty", req.Quantity, "price", req.Price)
	return res, err
}

func (i *InstrumentedExecutor) PlaceStopLimitOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error) {
	start := time.Now()
	res, err := i.inner.PlaceStopLimitOrder(ctx, req)
	record("Executor.PlaceStopLimitOrder", time.Since(start), err,
		"symbol", req.Symbol, "stop_price", req.Price, "qty", req.Quantity)
	return res, err
}

func (i *InstrumentedExecutor) PlaceStopMarketOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error) {
	start := time.Now()
	res, err := i.inner.PlaceStopMarketOrder(ctx, req)
	record("Executor.PlaceStopMarketOrder", time.Since(start), err,
		"symbol", req.Symbol, "stop_price", req.TriggerPrice, "qty", req.Quantity)
	return res, err
}

func (i *InstrumentedExecutor) PlaceTrailingStopOrder(ctx context.Context, req execution.TrailingStopRequest) (*domain.OrderResult, error) {
	start := time.Now()
	res, err := i.inner.PlaceTrailingStopOrder(ctx, req)
	record("Executor.PlaceTrailingStopOrder", time.Since(start), err,
		"symbol", req.Symbol, "callback_rate", req.CallbackRate, "qty", req.Quantity)
	return res, err
}

func (i *InstrumentedExecutor) CancelOrder(ctx context.Context, symbol, orderID string) error {
	start := time.Now()
	err := i.inner.CancelOrder(ctx, symbol, orderID)
	record("Executor.CancelOrder", time.Since(start), err, "symbol", symbol, "order_id", orderID)
	return err
}

func (i *InstrumentedExecutor) GetPosition(ctx context.Context, symbol string) (*domain.Position, error) {
	start := time.Now()
	res, err := i.inner.GetPosition(ctx, symbol)
	record("Executor.GetPosition", time.Since(start), err, "symbol", symbol)
	return res, err
}

func (i *InstrumentedExecutor) GetAccountBalance(ctx context.Context) (*domain.Balance, error) {
	start := time.Now()
	res, err := i.inner.GetAccountBalance(ctx)
	if err == nil && res != nil {
		record("Executor.GetAccountBalance", time.Since(start), err,
			"available", res.AvailableBalance, "total", res.TotalBalance)
	} else {
		record("Executor.GetAccountBalance", time.Since(start), err)
	}
	return res, err
}

func (i *InstrumentedExecutor) SetLeverage(ctx context.Context, symbol string, leverage int) error {
	start := time.Now()
	err := i.inner.SetLeverage(ctx, symbol, leverage)
	record("Executor.SetLeverage", time.Since(start), err, "symbol", symbol, "leverage", leverage)
	return err
}
