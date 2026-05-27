package exchange

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"futures/internal/domain"
)

type BinanceClient struct {
	apiKey    string
	apiSecret string
	baseURL   string
	http      *http.Client
}

func NewBinanceClient(apiKey, apiSecret, baseURL string) *BinanceClient {
	return &BinanceClient{
		apiKey:    apiKey,
		apiSecret: apiSecret,
		baseURL:   baseURL,
		http:      &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *BinanceClient) GetExchangeInfo(ctx context.Context) (*ExchangeInfoResponse, error) {
	body, err := c.get(ctx, "/fapi/v1/exchangeInfo", nil, false)
	if err != nil {
		return nil, err
	}
	var resp ExchangeInfoResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse exchangeInfo: %w", err)
	}
	return &resp, nil
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
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))

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
	return &resp, nil
}

func (c *BinanceClient) CancelOrder(ctx context.Context, symbol, orderID string) error {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("orderId", orderID)
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))

	sig := c.sign(params.Encode())
	params.Set("signature", sig)

	_, err := c.delete(ctx, "/fapi/v1/order", params)
	return err
}

func (c *BinanceClient) SetLeverage(ctx context.Context, symbol string, leverage int) error {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("leverage", strconv.Itoa(leverage))
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))

	sig := c.sign(params.Encode())
	params.Set("signature", sig)

	_, err := c.post(ctx, "/fapi/v1/leverage", params)
	return err
}

func (c *BinanceClient) GetPositionRisk(ctx context.Context, symbol string) (*PositionRiskResponse, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))

	sig := c.sign(params.Encode())
	params.Set("signature", sig)

	body, err := c.get(ctx, "/fapi/v2/positionRisk", params, true)
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

// GetPriceChange returns the percentage price change between from and to for a symbol.
// Uses 1-minute klines: compares the close at `from` to the close at `to`.
// A negative return means price dropped (a short would have profited).
func (c *BinanceClient) GetPriceChange(ctx context.Context, symbol string, from, to time.Time) (float64, error) {
	// Fetch one 1m candle at `from`
	fromCandles, err := c.GetHistoricalKlines(ctx, symbol, "1m", from, from.Add(time.Minute))
	if err != nil {
		return 0, fmt.Errorf("GetPriceChange from kline: %w", err)
	}
	if len(fromCandles) == 0 {
		return 0, fmt.Errorf("no kline data at entry time for %s", symbol)
	}

	// Fetch one 1m candle at `to`
	toCandles, err := c.GetHistoricalKlines(ctx, symbol, "1m", to, to.Add(time.Minute))
	if err != nil {
		return 0, fmt.Errorf("GetPriceChange to kline: %w", err)
	}
	if len(toCandles) == 0 {
		return 0, fmt.Errorf("no kline data at exit time for %s", symbol)
	}

	entryPrice := fromCandles[0].Close
	exitPrice := toCandles[0].Close
	if entryPrice == 0 {
		return 0, fmt.Errorf("zero entry price for %s", symbol)
	}

	return (exitPrice - entryPrice) / entryPrice * 100, nil
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
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))

	sig := c.sign(params.Encode())
	params.Set("signature", sig)

	body, err := c.get(ctx, "/fapi/v2/account", params, true)
	if err != nil {
		return nil, err
	}
	var resp AccountResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse account: %w", err)
	}
	return &resp, nil
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

func (c *BinanceClient) delete(ctx context.Context, path string, params url.Values) ([]byte, error) {
	u := c.baseURL + path + "?" + params.Encode()
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
