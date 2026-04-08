package bybit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
	"github.com/osman/bot-traider/internal/shared/exchange"
)

// Убеждаемся, что RestClient реализует exchange.RestClient.
var _ exchange.RestClient = (*RestClient)(nil)

// AccountInfo содержит базовую информацию об аккаунте Bybit.
type AccountInfo struct {
	AccountType string
	Balances    []AccountBalance
}

// AccountBalance — баланс одного актива.
type AccountBalance struct {
	Coin          string
	WalletBalance float64
}

// RestClient — клиент Bybit REST API V5 (signed endpoints).
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
	baseURL := "https://api.bybit.com"
	if devMode {
		baseURL = "https://api-testnet.bybit.com"
	}
	log.Info("bybit rest client created",
		zap.String("base_url", baseURL),
		zap.Bool("dev_mode", devMode),
	)
	return &RestClient{
		baseURL: baseURL,
		apiKey:  sharedconfig.GetEnv("BYBIT_API_KEY", ""),
		secret:  sharedconfig.GetEnv("BYBIT_API_SECRET", ""),
		http:    &http.Client{Timeout: 10 * time.Second},
		log:     log,
	}
}

// GetAccountInfo возвращает информацию об аккаунте (GET /v5/account/wallet-balance).
func (c *RestClient) GetAccountInfo(ctx context.Context) (AccountInfo, error) {
	queryStr := "accountType=UNIFIED"
	resp, err := c.doWithRetryFn(func() (*http.Request, error) {
		return c.newSignedRequest(ctx, http.MethodGet, "/v5/account/wallet-balance", queryStr, "")
	})
	if err != nil {
		return AccountInfo{}, fmt.Errorf("bybit get account: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	c.log.Debug("bybit get account response",
		zap.Int("status", resp.StatusCode),
		zap.String("body", string(body)),
	)
	if resp.StatusCode != http.StatusOK {
		return AccountInfo{}, fmt.Errorf("bybit get account: status %d body: %s", resp.StatusCode, body)
	}

	var raw struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				AccountType string `json:"accountType"`
				Coin        []struct {
					Coin          string `json:"coin"`
					WalletBalance string `json:"walletBalance"`
				} `json:"coin"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return AccountInfo{}, fmt.Errorf("bybit get account unmarshal: %w", err)
	}
	if raw.RetCode != 0 {
		return AccountInfo{}, fmt.Errorf("bybit get account api error: %d %s", raw.RetCode, raw.RetMsg)
	}

	info := AccountInfo{}
	if len(raw.Result.List) > 0 {
		info.AccountType = raw.Result.List[0].AccountType
		for _, coin := range raw.Result.List[0].Coin {
			bal, _ := strconv.ParseFloat(coin.WalletBalance, 64)
			if bal > 0 {
				info.Balances = append(info.Balances, AccountBalance{Coin: coin.Coin, WalletBalance: bal})
			}
		}
	}
	return info, nil
}

// PlaceMarketOrder размещает рыночный ордер (POST /v5/order/create).
func (c *RestClient) PlaceMarketOrder(ctx context.Context, symbol, side string, qty float64) (exchange.OrderResult, error) {
	c.log.Info("bybit: placing market order",
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.Float64("qty", qty),
	)

	// Bybit V5: side = "Buy" | "Sell"
	bySide := strings.Title(strings.ToLower(side)) //nolint:staticcheck

	fmtQty := formatQty(qty)
	bodyMap := map[string]string{
		"category":   "spot",
		"symbol":     symbol,
		"side":       bySide,
		"orderType":  "Market",
		"qty":        fmtQty,
		"marketUnit": "baseCoin",
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	c.log.Info("bybit: order request body", zap.ByteString("body", bodyBytes))

	resp, err := c.doWithRetryFn(func() (*http.Request, error) {
		return c.newSignedRequest(ctx, http.MethodPost, "/v5/order/create", "", string(bodyBytes))
	})
	if err != nil {
		return exchange.OrderResult{}, fmt.Errorf("bybit place market order: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return exchange.OrderResult{}, fmt.Errorf("bybit place market order: status %d body: %s", resp.StatusCode, respBody)
	}

	var raw struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			OrderID string `json:"orderId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return exchange.OrderResult{}, fmt.Errorf("bybit place market order unmarshal: %w", err)
	}
	if raw.RetCode != 0 {
		return exchange.OrderResult{}, fmt.Errorf("bybit place market order api error: %d %s", raw.RetCode, raw.RetMsg)
	}

	// Для BUY запрашиваем реальные executions, чтобы знать точный netQty после комиссии.
	// Для SELL netQty не используется (продаём всё, что было куплено).
	execQty, _ := strconv.ParseFloat(fmtQty, 64)
	netQty := execQty
	if strings.EqualFold(side, "buy") {
		fallback := execQty * 0.999
		netQty = fetchNetQty(ctx, c.http, c.baseURL, c.apiKey, c.secret, symbol, raw.Result.OrderID, fallback, c.log)
	}

	c.log.Info("bybit: market order placed",
		zap.String("order_id", raw.Result.OrderID),
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.Float64("requested_qty", qty),
		zap.Float64("net_qty", netQty),
	)

	result := exchange.OrderResult{
		OrderID: raw.Result.OrderID,
		Qty:     netQty,
	}
	return result, nil
}

// CancelOrder отменяет ордер (POST /v5/order/cancel).
func (c *RestClient) CancelOrder(ctx context.Context, symbol, orderID string) error {
	c.log.Info("bybit: cancelling order", zap.String("symbol", symbol), zap.String("order_id", orderID))

	bodyMap := map[string]string{
		"category": "spot",
		"symbol":   symbol,
		"orderId":  orderID,
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	resp, err := c.doWithRetryFn(func() (*http.Request, error) {
		return c.newSignedRequest(ctx, http.MethodPost, "/v5/order/cancel", "", string(bodyBytes))
	})
	if err != nil {
		return fmt.Errorf("bybit cancel order: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var raw struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return fmt.Errorf("bybit cancel order unmarshal: %w", err)
	}
	if raw.RetCode != 0 {
		return fmt.Errorf("bybit cancel order api error: %d %s", raw.RetCode, raw.RetMsg)
	}

	c.log.Info("bybit: order cancelled", zap.String("order_id", orderID))
	return nil
}

// newSignedRequest создаёт подписанный HTTP-запрос для Bybit V5.
func (c *RestClient) newSignedRequest(ctx context.Context, method, path, queryStr, body string) (*http.Request, error) {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	recvWindow := "5000"

	var signPayload string
	if method == http.MethodGet {
		signPayload = timestamp + c.apiKey + recvWindow + queryStr
	} else {
		signPayload = timestamp + c.apiKey + recvWindow + body
	}

	signature := signBybit(c.secret, signPayload)

	fullURL := c.baseURL + path
	if queryStr != "" {
		fullURL += "?" + queryStr
	}

	var reqBody io.Reader
	if body != "" {
		reqBody = bytes.NewReader([]byte(body))
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, reqBody)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-BAPI-API-KEY", c.apiKey)
	req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
	req.Header.Set("X-BAPI-SIGN", signature)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// doWithRetryFn выполняет запрос с повтором при rate-limit.
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

		c.log.Debug("bybit rest request",
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
			c.log.Warn("bybit: rate limit hit, retrying",
				zap.Duration("wait", wait),
				zap.Int("attempt", attempt+1),
			)
			time.Sleep(wait)
			wait *= 2
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("bybit: max retries exceeded")
}

