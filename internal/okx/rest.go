package okx

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

// AccountBalance — баланс одного актива OKX.
type AccountBalance struct {
	Ccy      string
	AvailBal float64
}

// RestClient — клиент OKX REST API v5 (signed endpoints).
type RestClient struct {
	baseURL    string
	apiKey     string
	secret     string
	passphrase string
	devMode    bool
	http       *http.Client
	log        *zap.Logger
}

// NewRestClient создаёт RestClient, читая конфиг из env.
// При DEV_MODE=true добавляет заголовок x-simulated-trading: 1.
func NewRestClient(ctx context.Context,log *zap.Logger) *RestClient {
	devMode := sharedconfig.GetEnv("DEV_MODE", "false") == "true"
	baseURL := "https://www.okx.com"

	log.Info("okx rest client created",
		zap.String("base_url", baseURL),
		zap.Bool("dev_mode", devMode),
	)

	okx := &RestClient{
		baseURL:    baseURL,
		apiKey:     sharedconfig.GetEnv("OKX_API_KEY", ""),
		secret:     sharedconfig.GetEnv("OKX_API_SECRET", ""),
		passphrase: sharedconfig.GetEnv("OKX_API_PASSPHRASE", ""),
		devMode:    devMode,
		http:       &http.Client{Timeout: 10 * time.Second},
		log:        log,
	}


		okxAPIKey := sharedconfig.GetEnv("OKX_API_KEY", "")
	if okxAPIKey != "" {
		if err := okx.GetAccountInfo(ctx); err != nil {
			log.Fatal("okx api key invalid", zap.Error(err))
		}
	} else {
		log.Info("okx api key not set, skipping okx trading")
	}

	return okx
}

// GetAccountInfo проверяет ключи и логирует балансы (GET /api/v5/account/balance).
func (c *RestClient) GetAccountInfo(ctx context.Context) error {
	c.log.Info("okx: getting account info")

	body, err := c.doRequest(ctx, http.MethodGet, "/api/v5/account/balance", "")
	if err != nil {
		return fmt.Errorf("okx get account info: %w", err)
	}

	var raw struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			Details []struct {
				Ccy      string `json:"ccy"`
				AvailBal string `json:"availBal"`
			} `json:"details"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("okx get account info unmarshal: %w", err)
	}
	if raw.Code != "0" {
		return fmt.Errorf("okx get account info api error: code=%s msg=%s", raw.Code, raw.Msg)
	}

	var balances []AccountBalance
	if len(raw.Data) > 0 {
		for _, d := range raw.Data[0].Details {
			bal, _ := strconv.ParseFloat(d.AvailBal, 64)
			if bal > 0 {
				balances = append(balances, AccountBalance{Ccy: d.Ccy, AvailBal: bal})
			}
		}
	}

	fields := []zap.Field{zap.Int("balances_count", len(balances))}
	for _, b := range balances {
		fields = append(fields, zap.Float64(b.Ccy, b.AvailBal))
	}
	c.log.Info("okx api key valid", fields...)
	return nil
}

// PlaceMarketOrder размещает рыночный ордер (POST /api/v5/trade/order).
// Для BUY — запрашивает точный netQty через fills endpoint.
func (c *RestClient) PlaceMarketOrder(ctx context.Context, symbol, side string, qty float64) (exchange.OrderResult, error) {
	c.log.Info("okx: placing market order",
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.Float64("qty", qty),
	)

	fmtQty := formatQty(qty)

	reqMap := map[string]string{
		"instId":  symbol,
		"tdMode":  "cash",
		"side":    strings.ToLower(side),
		"ordType": "market",
		"sz":      fmtQty,
		"tgtCcy":  "base_ccy",
	}
	bodyBytes, _ := json.Marshal(reqMap)

	c.log.Info("okx: order request body", zap.ByteString("body", bodyBytes))

	respBody, err := c.doRequest(ctx, http.MethodPost, "/api/v5/trade/order", string(bodyBytes))
	if err != nil {
		return exchange.OrderResult{}, fmt.Errorf("okx place market order: %w", err)
	}

	var raw struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			OrdID string `json:"ordId"`
			SCode string `json:"sCode"`
			SMsg  string `json:"sMsg"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return exchange.OrderResult{}, fmt.Errorf("okx place market order unmarshal: %w", err)
	}
	if raw.Code != "0" {
		return exchange.OrderResult{}, fmt.Errorf("okx place market order api error: code=%s msg=%s", raw.Code, raw.Msg)
	}
	if len(raw.Data) == 0 {
		return exchange.OrderResult{}, fmt.Errorf("okx place market order: empty data in response")
	}
	if raw.Data[0].SCode != "0" {
		return exchange.OrderResult{}, fmt.Errorf("okx place market order order error: sCode=%s sMsg=%s", raw.Data[0].SCode, raw.Data[0].SMsg)
	}

	orderID := raw.Data[0].OrdID
	execQty, _ := strconv.ParseFloat(fmtQty, 64)
	netQty := execQty

	if strings.EqualFold(side, "buy") {
		fallback := execQty * 0.999
		netQty = c.fetchNetQty(ctx, symbol, orderID, fallback)
	}

	c.log.Info("okx: market order placed",
		zap.String("order_id", orderID),
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.Float64("requested_qty", qty),
		zap.Float64("net_qty", netQty),
	)

	return exchange.OrderResult{
		OrderID: orderID,
		Qty:     netQty,
	}, nil
}

// CancelOrder отменяет ордер (POST /api/v5/trade/cancel-order).
func (c *RestClient) CancelOrder(ctx context.Context, symbol, orderID string) error {
	c.log.Info("okx: cancelling order", zap.String("symbol", symbol), zap.String("order_id", orderID))

	bodyMap := map[string]string{
		"instId": symbol,
		"ordId":  orderID,
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	respBody, err := c.doRequest(ctx, http.MethodPost, "/api/v5/trade/cancel-order", string(bodyBytes))
	if err != nil {
		return fmt.Errorf("okx cancel order: %w", err)
	}

	var raw struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return fmt.Errorf("okx cancel order unmarshal: %w", err)
	}
	if raw.Code != "0" {
		return fmt.Errorf("okx cancel order api error: code=%s msg=%s", raw.Code, raw.Msg)
	}

	c.log.Info("okx: order cancelled", zap.String("order_id", orderID))
	return nil
}

// fetchNetQty запрашивает /api/v5/trade/fills для точного qty после BUY с учётом комиссии.
// Повторяет до 3 раз с паузой 300 мс. При неудаче возвращает fallback.
func (c *RestClient) fetchNetQty(ctx context.Context, instID, ordID string, fallback float64) float64 {
	const maxAttempts = 3

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(300 * time.Millisecond):
			case <-ctx.Done():
				c.log.Warn("okx: fetchNetQty: context cancelled", zap.String("order_id", ordID))
				return fallback
			}
		}

		path := fmt.Sprintf("/api/v5/trade/fills?instId=%s&ordId=%s", instID, ordID)

		// Для GET с query params подписываем полный path включая query.
		ts := okxTimestamp()
		sig := signOKX(c.secret, ts, http.MethodGet, path, "")

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
		if err != nil {
			c.log.Warn("okx: fetchNetQty: failed to create request", zap.Error(err))
			return fallback
		}
		c.setAuthHeaders(req, ts, sig)

		start := time.Now()
		resp, err := c.http.Do(req)
		if err != nil {
			c.log.Warn("okx: fetchNetQty: request failed",
				zap.Error(err),
				zap.Int("attempt", attempt+1),
			)
			return fallback
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		c.log.Debug("okx: fetchNetQty response",
			zap.String("order_id", ordID),
			zap.Int("attempt", attempt+1),
			zap.Duration("latency", time.Since(start)),
			zap.ByteString("body", body),
		)

		var raw struct {
			Code string `json:"code"`
			Msg  string `json:"msg"`
			Data []struct {
				FillSz string `json:"fillSz"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			c.log.Warn("okx: fetchNetQty: unmarshal failed", zap.Error(err))
			return fallback
		}
		if raw.Code != "0" {
			c.log.Warn("okx: fetchNetQty: api error",
				zap.String("code", raw.Code),
				zap.String("msg", raw.Msg),
			)
			return fallback
		}
		if len(raw.Data) == 0 {
			c.log.Debug("okx: fetchNetQty: fills empty, retrying",
				zap.String("order_id", ordID),
				zap.Int("attempt", attempt+1),
			)
			continue
		}

		totalFillSz := 0.0
		for _, fill := range raw.Data {
			sz, _ := strconv.ParseFloat(fill.FillSz, 64)
			totalFillSz += sz
		}

		c.log.Info("okx: fetchNetQty: computed from fills",
			zap.String("order_id", ordID),
			zap.String("inst_id", instID),
			zap.Float64("net_qty", totalFillSz),
			zap.Int("fills_count", len(raw.Data)),
		)
		return totalFillSz
	}

	c.log.Warn("okx: fetchNetQty: exhausted retries, using fallback",
		zap.String("order_id", ordID),
		zap.Float64("fallback", fallback),
	)
	return fallback
}

// doRequest выполняет подписанный HTTP-запрос и возвращает тело ответа.
// При rate-limit (429 или code "50011") повторяет до 3 раз.
func (c *RestClient) doRequest(ctx context.Context, method, path, body string) ([]byte, error) {
	wait := time.Second
	for attempt := 0; attempt < 3; attempt++ {
		ts := okxTimestamp()
		sig := signOKX(c.secret, ts, method, path, body)

		var reqBody io.Reader
		if body != "" {
			reqBody = bytes.NewReader([]byte(body))
		}

		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
		if err != nil {
			return nil, err
		}
		c.setAuthHeaders(req, ts, sig)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}

		start := time.Now()
		resp, err := c.http.Do(req)
		latency := time.Since(start)

		c.log.Debug("okx rest request",
			zap.String("method", method),
			zap.String("path", path),
			zap.Duration("latency", latency),
			zap.Int("attempt", attempt+1),
		)

		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			c.log.Warn("okx: rate limit hit, retrying",
				zap.Duration("wait", wait),
				zap.Int("attempt", attempt+1),
			)
			time.Sleep(wait)
			wait *= 2
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		c.log.Debug("okx rest response",
			zap.String("path", path),
			zap.Int("status", resp.StatusCode),
			zap.ByteString("body", respBody),
		)

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("okx http error: status %d body: %s", resp.StatusCode, respBody)
		}

		// Проверяем rate-limit на уровне API (code "50011").
		var check struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(respBody, &check) == nil && check.Code == "50011" {
			c.log.Warn("okx: api rate limit, retrying",
				zap.Duration("wait", wait),
				zap.Int("attempt", attempt+1),
			)
			time.Sleep(wait)
			wait *= 2
			continue
		}

		return respBody, nil
	}
	return nil, fmt.Errorf("okx: max retries exceeded")
}

// setAuthHeaders добавляет заголовки аутентификации OKX.
func (c *RestClient) setAuthHeaders(req *http.Request, timestamp, signature string) {
	req.Header.Set("OK-ACCESS-KEY", c.apiKey)
	req.Header.Set("OK-ACCESS-SIGN", signature)
	req.Header.Set("OK-ACCESS-TIMESTAMP", timestamp)
	req.Header.Set("OK-ACCESS-PASSPHRASE", c.passphrase)
	if c.devMode {
		req.Header.Set("x-simulated-trading", "1")
	}
}
