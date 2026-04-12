package kucoin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
	"github.com/osman/bot-traider/internal/shared/exchange"
)

// Убеждаемся, что RestClient реализует exchange.RestClient.
var _ exchange.RestClient = (*RestClient)(nil)

// AccountBalance — баланс одного актива на счёте KuCoin.
type AccountBalance struct {
	Currency  string
	Type      string
	Available float64
	Balance   float64
	Holds     float64
}

// RestClient — клиент KuCoin REST API.
type RestClient struct {
	baseURL    string
	apiKey     string
	secret     string
	passphrase string
	http       *http.Client
	log        *zap.Logger
}

// NewRestClient создаёт RestClient и проверяет API-ключи если они заданы.
func NewRestClient(ctx context.Context, log *zap.Logger) *RestClient {
	baseURL := sharedconfig.GetEnv("KUCOIN_REST_URL", "https://api.kucoin.com")
	apiKey := sharedconfig.GetEnv("KUCOIN_API_KEY", "")
	apiSecret := sharedconfig.GetEnv("KUCOIN_API_SECRET", "")
	passphrase := sharedconfig.GetEnv("KUCOIN_PASSPHRASE", "")

	c := &RestClient{
		baseURL:    baseURL,
		apiKey:     apiKey,
		secret:     apiSecret,
		passphrase: passphrase,
		http:       &http.Client{Timeout: 10 * time.Second},
		log:        log,
	}

	log.Info("kucoin rest client created", zap.String("base_url", baseURL))

	if apiKey != "" {
		log.Info("checking kucoin api keys...")
		balances, err := c.GetAccounts(ctx)
		if err != nil {
			log.Fatal("kucoin api key invalid", zap.Error(err))
		}
		log.Info("kucoin api keys valid", zap.Int("accounts_count", len(balances)))
	} else {
		log.Info("kucoin: no API keys configured, trading disabled")
	}

	return c
}

// HasAPIKeys возвращает true если API-ключи заданы.
func (c *RestClient) HasAPIKeys() bool {
	return c.apiKey != ""
}

// GetAccounts возвращает список торговых счетов (GET /api/v1/accounts).
func (c *RestClient) GetAccounts(ctx context.Context) ([]AccountBalance, error) {
	resp, err := c.doSigned(ctx, http.MethodGet, "/api/v1/accounts?type=trade", nil)
	if err != nil {
		return nil, fmt.Errorf("kucoin get accounts: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kucoin get accounts: status %d body: %s", resp.StatusCode, body)
	}

	var raw struct {
		Code string `json:"code"`
		Data []struct {
			Currency  string `json:"currency"`
			Type      string `json:"type"`
			Available string `json:"available"`
			Balance   string `json:"balance"`
			Holds     string `json:"holds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("kucoin get accounts unmarshal: %w", err)
	}
	if raw.Code != "200000" {
		return nil, fmt.Errorf("kucoin get accounts: api error code %s", raw.Code)
	}

	result := make([]AccountBalance, 0, len(raw.Data))
	for _, a := range raw.Data {
		avail, _ := strconv.ParseFloat(a.Available, 64)
		bal, _ := strconv.ParseFloat(a.Balance, 64)
		holds, _ := strconv.ParseFloat(a.Holds, 64)
		result = append(result, AccountBalance{
			Currency:  a.Currency,
			Type:      a.Type,
			Available: avail,
			Balance:   bal,
			Holds:     holds,
		})
	}
	return result, nil
}

// PlaceMarketOrder размещает рыночный ордер.
// Для BUY: qty — количество базовой валюты.
// Для SELL: qty — количество базовой валюты.
func (c *RestClient) PlaceMarketOrder(ctx context.Context, symbol, side string, qty float64) (exchange.OrderResult, error) {
	kuSymbol := toKucoinSymbol(symbol)
	formattedQty := formatQty(qty)

	c.log.Info("kucoin: placing market order",
		zap.String("symbol", kuSymbol),
		zap.String("side", side),
		zap.String("qty", formattedQty),
	)

	body := map[string]string{
		"clientOid": uuid.New().String(),
		"side":      strings.ToLower(side),
		"symbol":    kuSymbol,
		"type":      "market",
		"size":      formattedQty,
	}
	bodyJSON, _ := json.Marshal(body)

	resp, err := c.doSigned(ctx, http.MethodPost, "/api/v1/orders", bodyJSON)
	if err != nil {
		return exchange.OrderResult{}, fmt.Errorf("kucoin place market order: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return exchange.OrderResult{}, fmt.Errorf("kucoin place market order: status %d body: %s", resp.StatusCode, respBody)
	}

	var raw struct {
		Code string `json:"code"`
		Data struct {
			OrderID string `json:"orderId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return exchange.OrderResult{}, fmt.Errorf("kucoin place market order unmarshal: %w", err)
	}
	if raw.Code != "200000" {
		return exchange.OrderResult{}, fmt.Errorf("kucoin place market order: api error code %s body: %s", raw.Code, respBody)
	}

	orderID := raw.Data.OrderID
	c.log.Info("kucoin: market order placed, fetching fill details",
		zap.String("order_id", orderID),
		zap.String("symbol", kuSymbol),
	)

	// Ждём исполнения ордера и получаем детали сделки
	result, err := c.getOrderFill(ctx, orderID, symbol)
	if err != nil {
		c.log.Warn("kucoin: failed to get fill details, using qty estimate",
			zap.String("order_id", orderID),
			zap.Error(err),
		)
		return exchange.OrderResult{OrderID: orderID, Qty: qty}, nil
	}

	c.log.Info("kucoin: market order filled",
		zap.String("order_id", orderID),
		zap.String("symbol", kuSymbol),
		zap.String("side", side),
		zap.Float64("qty", result.Qty),
		zap.Float64("price", result.Price),
	)

	return result, nil
}

// getOrderFill получает детали исполненного ордера.
func (c *RestClient) getOrderFill(ctx context.Context, orderID, symbol string) (exchange.OrderResult, error) {
	// Небольшая задержка для исполнения рыночного ордера
	select {
	case <-ctx.Done():
		return exchange.OrderResult{}, ctx.Err()
	case <-time.After(300 * time.Millisecond):
	}

	for attempt := 0; attempt < 5; attempt++ {
		resp, err := c.doSigned(ctx, http.MethodGet, "/api/v1/orders/"+orderID, nil)
		if err != nil {
			return exchange.OrderResult{}, fmt.Errorf("get order: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var raw struct {
			Code string `json:"code"`
			Data struct {
				DealSize  string `json:"dealSize"`
				DealFunds string `json:"dealFunds"`
				Fee       string `json:"fee"`
				IsActive  bool   `json:"isActive"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return exchange.OrderResult{}, fmt.Errorf("unmarshal order: %w", err)
		}
		if raw.Code != "200000" {
			return exchange.OrderResult{}, fmt.Errorf("get order api error: %s", raw.Code)
		}

		dealSize, _ := strconv.ParseFloat(raw.Data.DealSize, 64)
		dealFunds, _ := strconv.ParseFloat(raw.Data.DealFunds, 64)

		if !raw.Data.IsActive && dealSize > 0 {
			avgPrice := 0.0
			if dealSize > 0 {
				avgPrice = dealFunds / dealSize
			}
			return exchange.OrderResult{
				OrderID: orderID,
				Qty:     dealSize,
				Price:   avgPrice,
			}, nil
		}

		// Ордер ещё активен — ждём
		select {
		case <-ctx.Done():
			return exchange.OrderResult{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}

	return exchange.OrderResult{}, fmt.Errorf("order %s not filled after retries", orderID)
}

// CancelOrder отменяет ордер (DELETE /api/v1/orders/{orderId}).
func (c *RestClient) CancelOrder(ctx context.Context, symbol, orderID string) error {
	c.log.Info("kucoin: cancelling order",
		zap.String("symbol", toKucoinSymbol(symbol)),
		zap.String("order_id", orderID),
	)

	resp, err := c.doSigned(ctx, http.MethodDelete, "/api/v1/orders/"+orderID, nil)
	if err != nil {
		return fmt.Errorf("kucoin cancel order: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("kucoin cancel order: status %d body: %s", resp.StatusCode, body)
	}

	var raw struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &raw); err == nil && raw.Code != "200000" {
		return fmt.Errorf("kucoin cancel order: api error code %s", raw.Code)
	}

	c.log.Info("kucoin: order cancelled", zap.String("order_id", orderID))
	return nil
}

// PlaceLimitOrder размещает лимитный ордер.
// При postOnly=true устанавливает postOnly: true (аналог LIMIT_MAKER на Binance).
func (c *RestClient) PlaceLimitOrder(ctx context.Context, symbol, side string, qty, price float64, postOnly bool) (exchange.OrderResult, error) {
	kuSymbol := toKucoinSymbol(symbol)
	formattedQty := formatQty(qty)
	priceStr := strconv.FormatFloat(price, 'f', -1, 64)

	body := map[string]interface{}{
		"clientOid":   uuid.New().String(),
		"side":        strings.ToLower(side),
		"symbol":      kuSymbol,
		"type":        "limit",
		"price":       priceStr,
		"size":        formattedQty,
		"timeInForce": "GTC",
		"postOnly":    postOnly,
	}
	bodyJSON, _ := json.Marshal(body)

	resp, err := c.doSigned(ctx, http.MethodPost, "/api/v1/orders", bodyJSON)
	if err != nil {
		return exchange.OrderResult{}, fmt.Errorf("kucoin place limit order: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return exchange.OrderResult{}, fmt.Errorf("kucoin place limit order: status %d body: %s", resp.StatusCode, respBody)
	}

	var raw struct {
		Code string `json:"code"`
		Data struct {
			OrderID string `json:"orderId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return exchange.OrderResult{}, fmt.Errorf("kucoin place limit order unmarshal: %w", err)
	}
	if raw.Code != "200000" {
		return exchange.OrderResult{}, fmt.Errorf("kucoin place limit order: api error code %s body: %s", raw.Code, respBody)
	}

	c.log.Info("kucoin: limit order placed",
		zap.String("order_id", raw.Data.OrderID),
		zap.String("symbol", kuSymbol),
		zap.String("side", side),
		zap.Float64("price", price),
		zap.String("qty", formattedQty),
		zap.Bool("post_only", postOnly),
	)

	return exchange.OrderResult{
		OrderID: raw.Data.OrderID,
		Price:   price,
		Qty:     qty,
	}, nil
}

// GetOpenOrders возвращает список активных ордеров по символу.
func (c *RestClient) GetOpenOrders(ctx context.Context, symbol string) ([]exchange.OpenOrder, error) {
	kuSymbol := toKucoinSymbol(symbol)
	path := "/api/v1/orders?symbol=" + kuSymbol + "&status=active"

	resp, err := c.doSigned(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("kucoin get open orders: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kucoin get open orders: status %d body: %s", resp.StatusCode, body)
	}

	var raw struct {
		Code string `json:"code"`
		Data struct {
			Items []struct {
				ID    string `json:"id"`
				Side  string `json:"side"`
				Price string `json:"price"`
				Size  string `json:"size"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("kucoin get open orders unmarshal: %w", err)
	}
	if raw.Code != "200000" {
		return nil, fmt.Errorf("kucoin get open orders: api error code %s", raw.Code)
	}

	orders := make([]exchange.OpenOrder, 0, len(raw.Data.Items))
	for _, o := range raw.Data.Items {
		price, _ := strconv.ParseFloat(o.Price, 64)
		qty, _ := strconv.ParseFloat(o.Size, 64)
		orders = append(orders, exchange.OpenOrder{
			OrderID: o.ID,
			Symbol:  symbol,
			Side:    strings.ToUpper(o.Side),
			Price:   price,
			Qty:     qty,
		})
	}
	return orders, nil
}

// GetFreeUSDT возвращает свободный баланс USDT на торговом счёте.
func (c *RestClient) GetFreeUSDT(ctx context.Context) (float64, error) {
	path := "/api/v1/accounts?currency=USDT&type=trade"

	resp, err := c.doSigned(ctx, http.MethodGet, path, nil)
	if err != nil {
		return 0, fmt.Errorf("kucoin get free usdt: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("kucoin get free usdt: status %d body: %s", resp.StatusCode, body)
	}

	var raw struct {
		Code string `json:"code"`
		Data []struct {
			Currency  string `json:"currency"`
			Available string `json:"available"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0, fmt.Errorf("kucoin get free usdt unmarshal: %w", err)
	}
	if raw.Code != "200000" {
		return 0, fmt.Errorf("kucoin get free usdt: api error code %s", raw.Code)
	}

	for _, a := range raw.Data {
		if a.Currency == "USDT" {
			avail, _ := strconv.ParseFloat(a.Available, 64)
			return avail, nil
		}
	}
	return 0, nil
}

// VolatileTicker — тикер с изменением цены за 24ч (аналог binance.VolatileTicker).
type VolatileTicker struct {
	Symbol             string
	PriceChangePercent float64
	LastPrice          float64
}

// GetTopVolatile возвращает топ-N самых волатильных USDT-пар по абсолютному изменению цены за 24ч.
// Использует публичный эндпоинт GET /api/v1/market/allTickers — авторизация не требуется.
// Символы возвращаются во внутреннем формате (BTCUSDT).
func (c *RestClient) GetTopVolatile(ctx context.Context, limit int) ([]VolatileTicker, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.baseURL+"/api/v1/market/allTickers", nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kucoin get top volatile: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kucoin get top volatile: status %d body: %s", resp.StatusCode, body)
	}

	var raw struct {
		Code string `json:"code"`
		Data struct {
			Ticker []struct {
				Symbol     string `json:"symbol"`
				ChangeRate string `json:"changeRate"`
				Last       string `json:"last"`
			} `json:"ticker"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("kucoin get top volatile unmarshal: %w", err)
	}
	if raw.Code != "200000" {
		return nil, fmt.Errorf("kucoin get top volatile: api error code %s", raw.Code)
	}

	tickers := make([]VolatileTicker, 0, len(raw.Data.Ticker))
	for _, t := range raw.Data.Ticker {
		// Только USDT-пары
		if !strings.HasSuffix(t.Symbol, "-USDT") {
			continue
		}
		internalSymbol := fromKucoinSymbol(t.Symbol)

		// changeRate у KuCoin десятичный (0.0273 = 2.73%)
		var rate float64
		fmt.Sscanf(t.ChangeRate, "%f", &rate)
		pct := rate * 100

		last, _ := strconv.ParseFloat(t.Last, 64)

		tickers = append(tickers, VolatileTicker{
			Symbol:             internalSymbol,
			PriceChangePercent: pct,
			LastPrice:          last,
		})
	}

	// Сортировка по убыванию абсолютного изменения цены
	sort.Slice(tickers, func(i, j int) bool {
		return abs(tickers[i].PriceChangePercent) > abs(tickers[j].PriceChangePercent)
	})

	if limit > len(tickers) {
		limit = len(tickers)
	}
	return tickers[:limit], nil
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// doSigned выполняет подписанный HTTP-запрос к KuCoin REST API.
func (c *RestClient) doSigned(ctx context.Context, method, path string, bodyJSON []byte) (*http.Response, error) {
	ts := nowTimestamp()
	bodyStr := ""
	if len(bodyJSON) > 0 {
		bodyStr = string(bodyJSON)
	}

	payload := buildSignPayload(ts, method, path, bodyStr)
	apiSign := sign(c.secret, payload)

	// KC-API-PASSPHRASE для версии 2 тоже подписывается
	apiPassphrase := sign(c.secret, c.passphrase)

	var bodyReader io.Reader
	if len(bodyJSON) > 0 {
		bodyReader = bytes.NewReader(bodyJSON)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("KC-API-KEY", c.apiKey)
	req.Header.Set("KC-API-SIGN", apiSign)
	req.Header.Set("KC-API-TIMESTAMP", ts)
	req.Header.Set("KC-API-PASSPHRASE", apiPassphrase)
	req.Header.Set("KC-API-KEY-VERSION", "2")
	if len(bodyJSON) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}

	c.log.Debug("kucoin rest request",
		zap.String("method", method),
		zap.String("path", path),
		zap.Duration("latency", time.Since(start)),
		zap.Int("status", resp.StatusCode),
	)

	return resp, nil
}
