package execution

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"futures/internal/domain"
	"futures/internal/market"
)

type PaperExecutor struct {
	mu          sync.Mutex
	balance     float64
	slippageBps float64
	ticker      *market.TickerCache
}

func NewPaperExecutor(balance float64, slippageBps int, ticker *market.TickerCache) *PaperExecutor {
	return &PaperExecutor{
		balance:     balance,
		slippageBps: float64(slippageBps) / 10000.0,
		ticker:      ticker,
	}
}

func (p *PaperExecutor) PlaceMarketOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error) {
	price, ok := p.ticker.GetPrice(req.Symbol)
	if !ok || price == 0 {
		return nil, fmt.Errorf("no price available for %s", req.Symbol)
	}

	fillPrice := price
	if req.Side == domain.SideSell {
		fillPrice = price * (1 - p.slippageBps)
	} else {
		fillPrice = price * (1 + p.slippageBps)
	}

	return &domain.OrderResult{
		OrderID:   uuid.New().String(),
		Symbol:    req.Symbol,
		Side:      req.Side,
		FillPrice: fillPrice,
		Quantity:  req.Quantity,
		Status:    "FILLED",
		IsPaper:   true,
		Timestamp: time.Now(),
	}, nil
}

func (p *PaperExecutor) PlaceLimitOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error) {
	return &domain.OrderResult{
		OrderID:   uuid.New().String(),
		Symbol:    req.Symbol,
		Side:      req.Side,
		FillPrice: req.Price,
		Quantity:  req.Quantity,
		Status:    "NEW",
		IsPaper:   true,
		Timestamp: time.Now(),
	}, nil
}

func (p *PaperExecutor) PlaceStopLimitOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error) {
	return &domain.OrderResult{
		OrderID:   uuid.New().String(),
		Symbol:    req.Symbol,
		Side:      req.Side,
		FillPrice: req.Price,
		Quantity:  req.Quantity,
		Status:    "NEW",
		IsPaper:   true,
		Timestamp: time.Now(),
	}, nil
}

func (p *PaperExecutor) PlaceStopMarketOrder(ctx context.Context, req domain.OrderRequest) (*domain.OrderResult, error) {
	return &domain.OrderResult{
		OrderID:   uuid.New().String(),
		Symbol:    req.Symbol,
		Side:      req.Side,
		Quantity:  req.Quantity,
		Status:    "NEW",
		IsPaper:   true,
		Timestamp: time.Now(),
	}, nil
}

func (p *PaperExecutor) PlaceTrailingStopOrder(ctx context.Context, req TrailingStopRequest) (*domain.OrderResult, error) {
	return &domain.OrderResult{
		OrderID:   uuid.New().String(),
		Symbol:    req.Symbol,
		Side:      req.Side,
		Quantity:  req.Quantity,
		Status:    "NEW",
		IsPaper:   true,
		Timestamp: time.Now(),
	}, nil
}

func (p *PaperExecutor) CancelOrder(ctx context.Context, symbol string, orderID string) error {
	return nil
}

func (p *PaperExecutor) GetPosition(ctx context.Context, symbol string) (*domain.Position, error) {
	return nil, nil
}

func (p *PaperExecutor) GetAccountBalance(ctx context.Context) (*domain.Balance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return &domain.Balance{
		Asset:            "USDT",
		TotalBalance:     p.balance,
		AvailableBalance: p.balance,
	}, nil
}

func (p *PaperExecutor) SetLeverage(ctx context.Context, symbol string, leverage int) error {
	return nil
}
