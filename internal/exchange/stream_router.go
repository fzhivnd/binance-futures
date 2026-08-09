package exchange

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"futures/internal/domain"
	"futures/internal/market"
)

// UserDataRouter parses events from the /private/ws stream, which is opened
// with events=ORDER_TRADE_UPDATE so no client-side msgType filtering is needed.
type UserDataRouter struct {
	handler func(UserDataEvent)
}

func NewUserDataRouter(handler func(UserDataEvent)) *UserDataRouter {
	return &UserDataRouter{handler: handler}
}

func (r *UserDataRouter) Handle(msgType string, data []byte) {
	var event UserDataEvent
	if err := json.Unmarshal(data, &event); err != nil {
		slog.Debug("parse ORDER_TRADE_UPDATE failed", "error", err)
		return
	}
	r.handler(event)
}

type StreamRouter struct {
	engine     *market.MarketEngine
	bookTicker *market.BookTickerCache // may be nil
}

func NewStreamRouter(engine *market.MarketEngine) *StreamRouter {
	return &StreamRouter{engine: engine}
}

// SetBookTickerCache wires an optional BookTickerCache for @bookTicker events.
func (r *StreamRouter) SetBookTickerCache(c *market.BookTickerCache) {
	r.bookTicker = c
}

func (r *StreamRouter) Handle(msgType string, data []byte) {
	switch msgType {
	case "markPriceUpdate":
		r.handleMarkPriceArray(data)
	case "kline":
		r.handleKline(data)
	case "bookTicker":
		r.handleBookTicker(data)
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
	if strings.HasSuffix(stream, "@bookTicker") {
		return "bookTicker"
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

func (r *StreamRouter) handleBookTicker(data []byte) {
	if r.bookTicker == nil {
		return
	}
	var event BookTickerEvent
	if err := json.Unmarshal(data, &event); err != nil {
		slog.Debug("parse bookTicker failed", "error", err)
		return
	}
	if event.Symbol == "" {
		return
	}
	bt := &market.BookTicker{UpdatedAt: time.Now()}
	bt.BidPrice, _ = strconv.ParseFloat(event.BidPrice, 64)
	bt.BidQty, _ = strconv.ParseFloat(event.BidQty, 64)
	bt.AskPrice, _ = strconv.ParseFloat(event.AskPrice, 64)
	bt.AskQty, _ = strconv.ParseFloat(event.AskQty, 64)
	r.bookTicker.Update(event.Symbol, bt)
}
