package execution

import (
	"context"

	"futures/internal/domain"
)

// TrailingStopRequest is the input for TRAILING_STOP_MARKET orders (Phase 5).
type TrailingStopRequest struct {
	Symbol       string
	Side         domain.Side
	Quantity     float64
	CallbackRate float64 // e.g. 0.5 = 0.5%
	ReduceOnly   bool
}

type Executor interface {
	PlaceMarketOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error)
	PlaceLimitOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error)
	PlaceStopLimitOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error)
	PlaceStopMarketOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error)
	PlaceTrailingStopOrder(ctx context.Context, req TrailingStopRequest) (*domain.OrderResult, error)
	CancelOrder(ctx context.Context, symbol string, orderID string) error
	GetPosition(ctx context.Context, symbol string) (*domain.Position, error)
	GetAccountBalance(ctx context.Context) (*domain.Balance, error)
	SetLeverage(ctx context.Context, symbol string, leverage int) error
}
