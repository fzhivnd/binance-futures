package exchange

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"futures/internal/domain"
	"futures/internal/market"
)

// UserDataRouter dispatches ORDER_TRADE_UPDATE events to a handler.
type UserDataRouter struct {
	handler func(UserDataEvent)
}

func NewUserDataRouter(handler func(UserDataEvent)) *UserDataRouter {
	return &UserDataRouter{handler: handler}
}

func (r *UserDataRouter) Handle(msgType string, data []byte) {
	if msgType != "ORDER_TRADE_UPDATE" {
		return
	}
	var event UserDataEvent
	if err := json.Unmarshal(data, &event); err != nil {
		slog.Debug("parse ORDER_TRADE_UPDATE failed", "error", err)
		return
	}
	r.handler(event)
}

type StreamRouter struct {
	engine *market.MarketEngine
}

func NewStreamRouter(engine *market.MarketEngine) *StreamRouter {
	return &StreamRouter{engine: engine}
}

func (r *StreamRouter) Handle(msgType string, data []byte) {
	switch msgType {
	case "markPriceUpdate":
		r.handleMarkPriceArray(data)
	case "kline":
		r.handleKline(data)
	default:
		// combined stream wrapper or subscription response — try both
		var wrapped struct {
			Stream string          `json:"stream"`
			Data   json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(data, &wrapped); err == nil && wrapped.Stream != "" {
			r.Handle(detectStreamType(wrapped.Stream), wrapped.Data)
			return
		}
		// could be array of mark prices at top level
		var arr []MarkPriceEvent
		if err := json.Unmarshal(data, &arr); err == nil && len(arr) > 0 {
			r.processMarkPriceEvents(arr)
		}
	}
}

func detectStreamType(stream string) string {
	if len(stream) > 10 && stream[len(stream)-5:] == "kline" {
		return "kline"
	}
	return stream
}

func (r *StreamRouter) handleMarkPriceArray(data []byte) {
	var events []MarkPriceEvent
	if err := json.Unmarshal(data, &events); err != nil {
		slog.Debug("parse markPrice array failed", "error", err)
		return
	}
	r.processMarkPriceEvents(events)
}

func (r *StreamRouter) processMarkPriceEvents(events []MarkPriceEvent) {
	for _, e := range events {
		rate, err := strconv.ParseFloat(e.FundingRate, 64)
		if err != nil {
			continue
		}
		price, err := strconv.ParseFloat(e.MarkPrice, 64)
		if err != nil {
			continue
		}
		nextFunding := time.UnixMilli(e.NextFundingTime).UTC()
		r.engine.UpdateFunding(e.Symbol, rate, nextFunding, price)
	}
}

func (r *StreamRouter) handleKline(data []byte) {
	var event KlineEvent
	if err := json.Unmarshal(data, &event); err != nil {
		slog.Debug("parse kline failed", "error", err)
		return
	}
	k := event.Kline
	candle := domain.Candle{
		Symbol:    event.Symbol,
		Timeframe: domain.Timeframe(k.Interval),
		OpenTime:  time.UnixMilli(k.StartTime).UTC(),
		CloseTime: time.UnixMilli(k.EndTime).UTC(),
		IsClosed:  k.IsKlineClosed,
	}
	candle.Open, _ = strconv.ParseFloat(k.Open, 64)
	candle.High, _ = strconv.ParseFloat(k.High, 64)
	candle.Low, _ = strconv.ParseFloat(k.Low, 64)
	candle.Close, _ = strconv.ParseFloat(k.Close, 64)
	candle.Volume, _ = strconv.ParseFloat(k.Volume, 64)
	r.engine.UpdateCandle(candle)
}
