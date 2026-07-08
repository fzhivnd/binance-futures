package exchange

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"futures/internal/domain"
)

type BinanceClient struct {
	apiKey        string
	apiSecret     string
	baseURL       string
	http          *http.Client
	clockOffsetMs atomic.Int64 // Binance server time minus local time, in milliseconds
}

func NewBinanceClient(apiKey, apiSecret, baseURL string) *BinanceClient {
	return &BinanceClient{
		apiKey:    apiKey,
		apiSecret: apiSecret,
		baseURL:   baseURL,
		http:      &http.Client{Timeout: 30 * time.Second},
	}
}

// SetClockOffset stores the measured offset (serverTime - localTime) so that
// all signed requests use a timestamp Binance will accept.
func (c *BinanceClient) SetClockOffset(offset time.Duration) {
	c.clockOffsetMs.Store(offset.Milliseconds())
}

func (c *BinanceClient) nowMs() int64 {
	return time.Now().UnixMilli() + c.clockOffsetMs.Load()
}

func (c *BinanceClient) GetExchangeInfo(ctx context.Context) (*ExchangeInfoCache, error) {
	body, err := c.get(ctx, "/fapi/v1/exchangeInfo", nil, false)
	if err != nil {
		return nil, err
	}
	var raw ExchangeInfoResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse exchangeInfo: %w", err)
	}
	cache := &ExchangeInfoCache{
		Symbols: make(map[string]SymbolRules, len(raw.Symbols)),
	}
	for _, s := range raw.Symbols {
		rules := SymbolRules{
			Symbol: s.Symbol,
		}

		for _, f := range s.Filters {
			switch f.FilterType {

			case "LOT_SIZE":
				rules.StepSize, _ = strconv.ParseFloat(f.StepSize, 64)
				rules.MinQty, _ = strconv.ParseFloat(f.MinQty, 64)
				rules.MaxQty, _ = strconv.ParseFloat(f.MaxQty, 64)

			case "PRICE_FILTER":
				rules.TickSize, _ = strconv.ParseFloat(f.TickSize, 64)
			}
		}

		if rules.StepSize > 0 && rules.TickSize > 0 {
			cache.Symbols[s.Symbol] = rules
		}
	}

	return cache, nil
}

func (c *BinanceClient) GetFundingInfo(ctx context.Context) ([]FundingInfoItem, error) {
	body, err := c.get(ctx, "/fapi/v1/fundingInfo", nil, false)
	if err != nil {
		return nil, err
	}
	var resp []FundingInfoItem
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse fundingInfo: %w", err)
	}
	return resp, nil
}

func (c *BinanceClient) GetOpenInterest(ctx context.Context, symbol string) (*OpenInterestResponse, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	body, err := c.get(ctx, "/fapi/v1/openInterest", params, false)
	if err != nil {
		return nil, err
	}
	var resp OpenInterestResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse openInterest: %w", err)
	}
	return &resp, nil
}

func (c *BinanceClient) NewOrder(ctx context.Context, req NewOrderRequest) (*NewOrderResponse, error) {
	params := url.Values{}
	params.Set("symbol", req.Symbol)
	params.Set("side", req.Side)
	params.Set("type", req.Type)
	params.Set("quantity", req.Quantity)
	if req.Price != "" {
		params.Set("price", req.Price)
		params.Set("timeInForce", "GTC")
	}
	if req.StopPrice != "" {
		params.Set("stopPrice", req.StopPrice)
	}
	if req.ReduceOnly {
		params.Set("reduceOnly", "true")
	}
	if req.CallbackRate != "" {
		params.Set("callbackRate", req.CallbackRate)
	}
	if req.NewClientOrderId != "" {
		params.Set("newClientOrderId", req.NewClientOrderId)
	}
	params.Set("timestamp", strconv.FormatInt(c.nowMs(), 10))

	slog.Info("binance_client new order", "params", params.Encode())

	sig := c.sign(params.Encode())
	params.Set("signature", sig)

	body, err := c.post(ctx, "/fapi/v1/order", params)
	if err != nil {
		return nil, err
	}
	var resp NewOrderResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse newOrder: %w", err)
	}
	slog.Info("binance_client new order", "response", resp)
	return &resp, nil
}

func (c *BinanceClient) NewAlgoOrder(ctx context.Context, req NewOrderRequest) (*NewAlgoOrderResponse, error) {
	params := url.Values{}
	params.Set("algoType", "CONDITIONAL")
	params.Set("symbol", req.Symbol)
	params.Set("side", req.Side)
	params.Set("type", req.Type)
	if req.Quantity != "" {
		params.Set("quantity", req.Quantity)
	}
	if req.ClosePosition {
		params.Set("closePosition", "true")
	}
	if req.Price != "" {
		params.Set("price", req.Price)
		params.Set("timeInForce", "GTC")
	}
	if req.TriggerPrice != "" {
		params.Set("triggerPrice", req.TriggerPrice)
	}
	if req.ReduceOnly {
		params.Set("reduceOnly", "true")
	}
	if req.CallbackRate != "" {
		params.Set("callbackRate", req.CallbackRate)
	}
	if req.NewClientOrderId != "" {
		params.Set("clientAlgoId", req.NewClientOrderId)
	}
	params.Set("timestamp", strconv.FormatInt(c.nowMs(), 10))

	slog.Info("binance_client new order", "params", params.Encode())

	sig := c.sign(params.Encode())
	params.Set("signature", sig)

	body, err := c.post(ctx, "/fapi/v1/algoOrder", params)
	if err != nil {
		return nil, err
	}
	var resp NewAlgoOrderResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse newOrder: %w", err)
	}
	slog.Info("binance_client new algo order", "response", resp)
	return &resp, nil
}

func (c *BinanceClient) CancelOrder(ctx context.Context, symbol, orderID string, clientOrderId string) error {
	params := url.Values{}
	if clientOrderId != "" {
		params.Set("origClientOrderId", clientOrderId)
	} else {
		params.Set("orderId", orderID)
	}
	params.Set("symbol", symbol)
	params.Set("timestamp", strconv.FormatInt(c.nowMs(), 10))

	query := params.Encode()
	sig := c.sign(query)

	fullQuery := query + "&signature=" + sig

	_, err := c.delete(ctx, "/fapi/v1/order", fullQuery)
	return err
}

func (c *BinanceClient) CancelAlgoOrder(ctx context.Context, orderID string, clientOrderId string) error {
	params := url.Values{}
	if clientOrderId != "" {
		params.Set("clientAlgoId", clientOrderId)
	} else {
		params.Set("algoId", strings.TrimSpace(orderID))
	}
	params.Set("timestamp", strconv.FormatInt(c.nowMs(), 10))

	query := params.Encode()
	sig := c.sign(query)

	fullQuery := query + "&signature=" + sig

	_, err := c.delete(ctx, "/fapi/v1/algoOrder", fullQuery)
	return err
}

func (c *BinanceClient) SetLeverage(ctx context.Context, symbol string, leverage int) error {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("leverage", strconv.Itoa(leverage))
	params.Set("timestamp", strconv.FormatInt(c.nowMs(), 10))

	sig := c.sign(params.Encode())
	params.Set("signature", sig)

	_, err := c.post(ctx, "/fapi/v1/leverage", params)
	return err
}

func (c *BinanceClient) GetPositionRisk(ctx context.Context, symbol string) (*PositionRiskResponse, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("timestamp", strconv.FormatInt(c.nowMs(), 10))

	body, err := c.getSigned(ctx, "/fapi/v2/positionRisk", params)

	if err != nil {
		return nil, err
	}
	var resp []PositionRiskResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse positionRisk: %w", err)
	}
	if len(resp) == 0 {
		return nil, nil
	}
	return &resp[0], nil
}

// GetOpenOrders returns all open orders across all symbols.
func (c *BinanceClient) GetOpenOrders(ctx context.Context) ([]OpenOrderResponse, error) {
	params := url.Values{}
	params.Set("timestamp", strconv.FormatInt(c.nowMs(), 10))

	body, err := c.getSigned(ctx, "/fapi/v1/openOrders", params)
	if err != nil {
		return nil, err
	}
	var resp []OpenOrderResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse openOrders: %w", err)
	}
	return resp, nil
}

// GetAllPositionRisk returns all positions with non-zero positionAmt.
func (c *BinanceClient) GetAllPositionRisk(ctx context.Context) ([]PositionRiskResponse, error) {
	params := url.Values{}
	params.Set("timestamp", strconv.FormatInt(c.nowMs(), 10))

	body, err := c.getSigned(ctx, "/fapi/v2/positionRisk", params)
	if err != nil {
		return nil, err
	}
	var resp []PositionRiskResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse positionRisk: %w", err)
	}
	return resp, nil
}

// FetchKlines fetches the most recent `limit` candles for a symbol and interval.
// Used for seeding the candle store on startup; only closed candles are returned.
func (c *BinanceClient) FetchKlines(ctx context.Context, symbol, interval string, limit int) ([]domain.Candle, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("interval", interval)
	params.Set("limit", strconv.Itoa(limit))

	body, err := c.get(ctx, "/fapi/v1/klines", params, false)
	if err != nil {
		return nil, err
	}

	var raw [][]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse klines: %w", err)
	}

	now := time.Now().UnixMilli()
	candles := make([]domain.Candle, 0, len(raw))
	for _, k := range raw {
		closeMs := int64(k[6].(float64))
		if closeMs >= now {
			continue // skip the current open candle
		}
		openMs := int64(k[0].(float64))
		open, _ := strconv.ParseFloat(k[1].(string), 64)
		high, _ := strconv.ParseFloat(k[2].(string), 64)
		low, _ := strconv.ParseFloat(k[3].(string), 64)
		closeP, _ := strconv.ParseFloat(k[4].(string), 64)
		vol, _ := strconv.ParseFloat(k[5].(string), 64)
		candles = append(candles, domain.Candle{
			Symbol:    symbol,
			Timeframe: domain.Timeframe(interval),
			OpenTime:  time.UnixMilli(openMs),
			Open:      open,
			High:      high,
			Low:       low,
			Close:     closeP,
			Volume:    vol,
			CloseTime: time.UnixMilli(closeMs),
			IsClosed:  true,
		})
	}
	return candles, nil
}

// GetHistoricalKlines fetches historical klines for a symbol and interval over a date range.
func (c *BinanceClient) GetHistoricalKlines(ctx context.Context, symbol, interval string, start, end time.Time) ([]domain.Candle, error) {
	var all []domain.Candle
	startMs := start.UnixMilli()
	endMs := end.UnixMilli()
	limit := 1000

	for {
		params := url.Values{}
		params.Set("symbol", symbol)
		params.Set("interval", interval)
		params.Set("startTime", strconv.FormatInt(startMs, 10))
		params.Set("endTime", strconv.FormatInt(endMs, 10))
		params.Set("limit", strconv.Itoa(limit))

		body, err := c.get(ctx, "/fapi/v1/klines", params, false)
		if err != nil {
			return nil, err
		}

		var raw [][]interface{}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("parse klines: %w", err)
		}
		if len(raw) == 0 {
			break
		}

		for _, k := range raw {
			openMs := int64(k[0].(float64))
			closeMs := int64(k[6].(float64))
			open, _ := strconv.ParseFloat(k[1].(string), 64)
			high, _ := strconv.ParseFloat(k[2].(string), 64)
			low, _ := strconv.ParseFloat(k[3].(string), 64)
			closeP, _ := strconv.ParseFloat(k[4].(string), 64)
			vol, _ := strconv.ParseFloat(k[5].(string), 64)

			all = append(all, domain.Candle{
				Symbol:    symbol,
				Timeframe: domain.Timeframe(interval),
				OpenTime:  time.UnixMilli(openMs),
				Open:      open,
				High:      high,
				Low:       low,
				Close:     closeP,
				Volume:    vol,
				CloseTime: time.UnixMilli(closeMs),
				IsClosed:  true,
			})
		}

		last := int64(raw[len(raw)-1][0].(float64))
		if last >= endMs || len(raw) < limit {
			break
		}
		startMs = last + 1
	}

	return all, nil
}

// GetHistoricalFundingRates fetches historical funding rates for a symbol over a date range.
func (c *BinanceClient) GetHistoricalFundingRates(ctx context.Context, symbol string, start, end time.Time) ([]HistoricalFundingRate, error) {
	var all []HistoricalFundingRate
	startMs := start.UnixMilli()
	endMs := end.UnixMilli()
	limit := 1000

	for {
		params := url.Values{}
		params.Set("symbol", symbol)
		params.Set("startTime", strconv.FormatInt(startMs, 10))
		params.Set("endTime", strconv.FormatInt(endMs, 10))
		params.Set("limit", strconv.Itoa(limit))

		body, err := c.get(ctx, "/fapi/v1/fundingRate", params, false)
		if err != nil {
			return nil, err
		}

		var raw []fundingRateRaw
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("parse fundingRate: %w", err)
		}
		if len(raw) == 0 {
			break
		}

		for _, r := range raw {
			rate, _ := strconv.ParseFloat(r.FundingRate, 64)
			all = append(all, HistoricalFundingRate{
				Symbol:      r.Symbol,
				FundingTime: time.UnixMilli(r.FundingTime),
				FundingRate: rate,
			})
		}

		last := raw[len(raw)-1].FundingTime
		if last >= endMs || len(raw) < limit {
			break
		}
		startMs = last + 1
	}

	return all, nil
}

// GetPriceOutcome scans 1m candles from `from` to `to` and returns the hypothetical
// profit pct for a short position (positive = profitable). Stops early when TP or SL is hit;
// falls back to end-of-window close if neither triggers.
func (c *BinanceClient) GetPriceOutcome(ctx context.Context, symbol string, from, to time.Time, tpPct, slPct float64) (float64, error) {
	candles, err := c.GetHistoricalKlines(ctx, symbol, "1m", from, to)
	if err != nil {
		return 0, fmt.Errorf("GetPriceOutcome klines: %w", err)
	}
	if len(candles) == 0 {
		return 0, fmt.Errorf("no kline data for %s between %s and %s", symbol, from.Format(time.RFC3339), to.Format(time.RFC3339))
	}

	entryPrice := candles[0].Open
	if entryPrice == 0 {
		return 0, fmt.Errorf("zero entry price for %s", symbol)
	}

	tpPrice := entryPrice * (1 - tpPct/100)
	slPrice := entryPrice * (1 + slPct/100)

	for _, c := range candles {
		if c.Low <= tpPrice {
			return (entryPrice - tpPrice) / entryPrice * 100, nil
		}
		if c.High >= slPrice {
			return (entryPrice - slPrice) / entryPrice * 100, nil
		}
	}

	return (entryPrice - candles[len(candles)-1].Close) / entryPrice * 100, nil
}

func (c *BinanceClient) CreateListenKey(ctx context.Context) (string, error) {
	body, err := c.post(ctx, "/fapi/v1/listenKey", nil)
	if err != nil {
		return "", err
	}
	var resp struct {
		ListenKey string `json:"listenKey"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parse listenKey: %w", err)
	}
	return resp.ListenKey, nil
}

func (c *BinanceClient) KeepAliveListenKey(ctx context.Context, listenKey string) error {
	params := url.Values{}
	params.Set("listenKey", listenKey)
	_, err := c.put(ctx, "/fapi/v1/listenKey", params)
	return err
}

func (c *BinanceClient) GetAccount(ctx context.Context) (*AccountResponse, error) {
	params := url.Values{}
	params.Set("timestamp", strconv.FormatInt(c.nowMs(), 10))

	body, err := c.getSigned(ctx, "/fapi/v2/account", params)
	if err != nil {
		return nil, err
	}
	var resp AccountResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse account: %w", err)
	}
	return &resp, nil
}

// GetDepth fetches the order book for symbol up to `limit` levels (5/10/20/50/100/500/1000).
// Returns parsed bid and ask levels.
func (c *BinanceClient) GetDepth(ctx context.Context, symbol string, limit int) (*DepthResponse, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("limit", strconv.Itoa(limit))
	body, err := c.get(ctx, "/fapi/v1/depth", params, false)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Bids [][]string `json:"bids"`
		Asks [][]string `json:"asks"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse depth: %w", err)
	}
	parse := func(rows [][]string) []PriceLevel {
		out := make([]PriceLevel, 0, len(rows))
		for _, row := range rows {
			if len(row) < 2 {
				continue
			}
			price, err1 := strconv.ParseFloat(row[0], 64)
			qty, err2 := strconv.ParseFloat(row[1], 64)
			if err1 != nil || err2 != nil {
				continue
			}
			out = append(out, PriceLevel{Price: price, Qty: qty})
		}
		return out
	}
	return &DepthResponse{
		Bids: parse(raw.Bids),
		Asks: parse(raw.Asks),
	}, nil
}

// BookTickerPrices holds the parsed best bid/ask prices and quantities.
type BookTickerPrices struct {
	BidPrice float64
	BidQty   float64
	AskPrice float64
	AskQty   float64
}

// GetBookTicker fetches the current best bid/ask for a symbol via REST.
// Use as a fallback when the @bookTicker WS stream has no cached data.
func (c *BinanceClient) GetBookTicker(ctx context.Context, symbol string) (*BookTickerPrices, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	body, err := c.get(ctx, "/fapi/v1/ticker/bookTicker", params, false)
	if err != nil {
		return nil, err
	}
	var raw struct {
		BidPrice string `json:"bidPrice"`
		BidQty   string `json:"bidQty"`
		AskPrice string `json:"askPrice"`
		AskQty   string `json:"askQty"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse bookTicker: %w", err)
	}
	bid, err1 := strconv.ParseFloat(raw.BidPrice, 64)
	bidQty, err2 := strconv.ParseFloat(raw.BidQty, 64)
	ask, err3 := strconv.ParseFloat(raw.AskPrice, 64)
	askQty, err4 := strconv.ParseFloat(raw.AskQty, 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return nil, fmt.Errorf("parse bookTicker prices: bid=%v ask=%v", err1, err3)
	}
	return &BookTickerPrices{BidPrice: bid, BidQty: bidQty, AskPrice: ask, AskQty: askQty}, nil
}

// GetServerTime returns the Binance server time (UTC).
func (c *BinanceClient) GetServerTime(ctx context.Context) (time.Time, error) {
	body, err := c.get(ctx, "/fapi/v1/time", nil, false)
	if err != nil {
		return time.Time{}, err
	}
	var resp struct {
		ServerTime int64 `json:"serverTime"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return time.Time{}, fmt.Errorf("parse serverTime: %w", err)
	}
	return time.UnixMilli(resp.ServerTime).UTC(), nil
}

func (c *BinanceClient) get(ctx context.Context, path string, params url.Values, signed bool) ([]byte, error) {
	u := c.baseURL + path
	if params != nil {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if signed {
		req.Header.Set("X-MBX-APIKEY", c.apiKey)
	}
	return c.do(req)
}

func (c *BinanceClient) getSigned(ctx context.Context, path string, params url.Values) ([]byte, error) {
	query := params.Encode()
	sig := c.sign(query)
	query = query + "&signature=" + sig
	u := c.baseURL + path
	if query != "" {
		u += "?" + query
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-MBX-APIKEY", c.apiKey)
	return c.do(req)
}

func (c *BinanceClient) post(ctx context.Context, path string, params url.Values) ([]byte, error) {
	body := ""
	if params != nil {
		body = params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path,
		strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-MBX-APIKEY", c.apiKey)
	return c.do(req)
}

func (c *BinanceClient) put(ctx context.Context, path string, params url.Values) ([]byte, error) {
	body := ""
	if params != nil {
		body = params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+path,
		strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-MBX-APIKEY", c.apiKey)
	return c.do(req)
}

func (c *BinanceClient) delete(ctx context.Context, path string, query string) ([]byte, error) {
	u := c.baseURL + path
	if query != "" {
		u += "?" + query
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-MBX-APIKEY", c.apiKey)
	return c.do(req)
}

func (c *BinanceClient) do(req *http.Request) ([]byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("binance API error %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

func (c *BinanceClient) sign(payload string) string {
	mac := hmac.New(sha256.New, []byte(c.apiSecret))
	mac.Write([]byte(payload))
	return fmt.Sprintf("%x", mac.Sum(nil))
}
