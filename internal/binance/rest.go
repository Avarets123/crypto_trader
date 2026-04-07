package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
	"github.com/osman/bot-traider/internal/shared/exchange"
)

// Убеждаемся, что RestClient реализует exchange.RestClient.
var _ exchange.RestClient = (*RestClient)(nil)

// AccountInfo содержит базовую информацию об аккаунте Binance.
type AccountInfo struct {
	AccountType string
	Balances    []AccountBalance
}

// AccountBalance — баланс одного актива.
type AccountBalance struct {
	Asset  string
	Free   float64
	Locked float64
}

// RestClient — клиент Binance REST API (signed endpoints).
type RestClient struct {
	baseURL string
	apiKey  string
	secret  string
	http    *http.Client
	log     *zap.Logger
}

// NewRestClient создаёт RestClient.
// Если DEV_MODE=true — использует testnet.
func NewRestClient(log *zap.Logger) *RestClient {
	devMode := sharedconfig.GetEnv("DEV_MODE", "false") == "true"
	baseURL := "https://api.binance.com"
	if devMode {
		baseURL = "https://testnet.binance.vision"
	}
	SetStepSizeBaseURL(baseURL)
	log.Info("binance rest client created",
		zap.String("base_url", baseURL),
		zap.Bool("dev_mode", devMode),
	)
	return &RestClient{
		baseURL: baseURL,
		apiKey:  sharedconfig.GetEnv("BINANCE_API_KEY", ""),
		secret:  sharedconfig.GetEnv("BINANCE_API_SECRET", ""),
		http:    &http.Client{Timeout: 10 * time.Second},
		log:     log,
	}
}

// GetAccountInfo возвращает информацию об аккаунте (GET /api/v3/account).
func (c *RestClient) GetAccountInfo(ctx context.Context) (AccountInfo, error) {
	params := url.Values{}
	signREST(c.secret, params)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/v3/account?"+params.Encode(), nil)
	if err != nil {
		return AccountInfo{}, err
	}
	req.Header.Set("X-MBX-APIKEY", c.apiKey)

	resp, err := c.doWithRetry(req)
	if err != nil {
		return AccountInfo{}, fmt.Errorf("binance get account: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return AccountInfo{}, fmt.Errorf("binance get account: status %d body: %s", resp.StatusCode, body)
	}

	var raw struct {
		AccountType string `json:"accountType"`
		Balances    []struct {
			Asset  string `json:"asset"`
			Free   string `json:"free"`
			Locked string `json:"locked"`
		} `json:"balances"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return AccountInfo{}, fmt.Errorf("binance get account unmarshal: %w", err)
	}

	info := AccountInfo{AccountType: raw.AccountType}
	for _, b := range raw.Balances {
		free, _ := strconv.ParseFloat(b.Free, 64)
		locked, _ := strconv.ParseFloat(b.Locked, 64)
		if free > 0 || locked > 0 {
			info.Balances = append(info.Balances, AccountBalance{
				Asset: b.Asset, Free: free, Locked: locked,
			})
		}
	}
	return info, nil
}

// PlaceMarketOrder размещает рыночный ордер (POST /api/v3/order).
func (c *RestClient) PlaceMarketOrder(ctx context.Context, symbol, side string, qty float64) (exchange.OrderResult, error) {
	c.log.Info("binance: placing market order",
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.Float64("qty", qty),
	)

	stepSize := getStepSize(ctx, symbol, c.log)
	formattedQty := formatQtyWithStep(qty, stepSize)
	c.log.Info("binance: formatted quantity for order",
		zap.String("symbol", symbol),
		zap.Float64("raw_qty", qty),
		zap.Float64("step_size", stepSize),
		zap.String("formatted_qty", formattedQty),
	)

	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("side", strings.ToUpper(side))
	params.Set("type", "MARKET")
	params.Set("quantity", formattedQty)
	signREST(c.secret, params)

	c.log.Info("binance: order request body", zap.String("body", params.Encode()))

	body := strings.NewReader(params.Encode())
	reqFn := func() (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v3/order", strings.NewReader(params.Encode()))
		if err != nil {
			return nil, err
		}
		r.Header.Set("X-MBX-APIKEY", c.apiKey)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return r, nil
	}
	_ = body

	resp, err := c.doWithRetryFn(reqFn)
	if err != nil {
		return exchange.OrderResult{},fmt.Errorf("binance place market order: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return exchange.OrderResult{},fmt.Errorf("binance place market order: status %d body: %s", resp.StatusCode, respBody)
	}

	var raw struct {
		OrderID             int64  `json:"orderId"`
		Symbol              string `json:"symbol"`
		Side                string `json:"side"`
		ExecutedQty         string `json:"executedQty"`
		CummulativeQuoteQty string `json:"cummulativeQuoteQty"`
		Fills               []struct {
			Commission      string `json:"commission"`
			CommissionAsset string `json:"commissionAsset"`
		} `json:"fills"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return exchange.OrderResult{}, fmt.Errorf("binance place market order unmarshal: %w", err)
	}

	execQty, _ := strconv.ParseFloat(raw.ExecutedQty, 64)
	quoteQty, _ := strconv.ParseFloat(raw.CummulativeQuoteQty, 64)
	avgPrice := 0.0
	if execQty > 0 {
		avgPrice = quoteQty / execQty
	}

	// Вычисляем фактически полученное количество: если комиссия взята из базового актива,
	// вычитаем её — именно столько окажется на балансе и можно продать.
	netQty := netQtyAfterFills(symbol, execQty, raw.Fills)

	c.log.Info("binance: market order placed",
		zap.String("order_id", strconv.FormatInt(raw.OrderID, 10)),
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.Float64("exec_qty", execQty),
		zap.Float64("net_qty", netQty),
		zap.Float64("avg_price", avgPrice),
	)

	result := exchange.OrderResult{
		OrderID: strconv.FormatInt(raw.OrderID, 10),
		Qty:     netQty,
		Price:   avgPrice,
	}
	return result, nil
}

// CancelOrder отменяет ордер (DELETE /api/v3/order).
func (c *RestClient) CancelOrder(ctx context.Context, symbol, orderID string) error {
	c.log.Info("binance: cancelling order", zap.String("symbol", symbol), zap.String("order_id", orderID))

	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("orderId", orderID)
	signREST(c.secret, params)

	reqFn := func() (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodDelete,
			c.baseURL+"/api/v3/order?"+params.Encode(), nil)
		if err != nil {
			return nil, err
		}
		r.Header.Set("X-MBX-APIKEY", c.apiKey)
		return r, nil
	}

	resp, err := c.doWithRetryFn(reqFn)
	if err != nil {
		return fmt.Errorf("binance cancel order: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("binance cancel order: status %d body: %s", resp.StatusCode, body)
	}

	c.log.Info("binance: order cancelled", zap.String("order_id", orderID))
	return nil
}

// doWithRetry выполняет запрос с повтором при 429 (rate-limit).
func (c *RestClient) doWithRetry(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := c.http.Do(req)
	latency := time.Since(start)

	c.log.Debug("binance rest request",
		zap.String("method", req.Method),
		zap.String("url", req.URL.String()),
		zap.Duration("latency", latency),
	)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		return resp, nil
	}
	resp.Body.Close()
	return nil, fmt.Errorf("rate limit")
}

// doWithRetryFn выполняет запрос с повтором при rate-limit (использует фабрику).
func (c *RestClient) doWithRetryFn(reqFn func() (*http.Request, error)) (*http.Response, error) {
	wait := time.Second
	for attempt := 0; attempt < 3; attempt++ {
		req, err := reqFn()
		if err != nil {
			return nil, err
		}

		start := time.Now()
		resp, err := c.http.Do(req)
		latency := time.Since(start)

		c.log.Debug("binance rest request",
			zap.String("method", req.Method),
			zap.String("url", req.URL.Path),
			zap.Duration("latency", latency),
			zap.Int("attempt", attempt+1),
		)

		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			c.log.Warn("binance: rate limit hit, retrying",
				zap.Duration("wait", wait),
				zap.Int("attempt", attempt+1),
			)
			time.Sleep(wait)
			wait *= 2
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("binance: max retries exceeded")
}

