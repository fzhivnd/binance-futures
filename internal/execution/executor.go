package execution

import (
	"context"

	"futures/internal/domain"
)

type Executor interface {
	PlaceMarketOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error)
	PlaceLimitOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error)
	PlaceStopLimitOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error)
	PlaceStopMarketOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error)
	CancelOrder(ctx context.Context, symbol string, orderID string) error
	GetPosition(ctx context.Context, symbol string) (*domain.Position, error)
	GetAccountBalance(ctx context.Context) (*domain.Balance, error)
	SetLeverage(ctx context.Context, symbol string, leverage int) error
}
