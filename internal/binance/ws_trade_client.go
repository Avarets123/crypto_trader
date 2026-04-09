package binance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
	"github.com/osman/bot-traider/internal/shared/exchange"
)

// Убеждаемся, что WsTradeClient реализует exchange.RestClient.
var _ exchange.RestClient = (*WsTradeClient)(nil)

// WsTradeClient реализует exchange.RestClient через Binance WebSocket API.
// Ордера размещаются по одному персистентному WS-соединению без REST-вызовов.
type WsTradeClient struct {
	base        wsConn
	apiKey      string
	secret      string
	restBaseURL string
	http        *http.Client
	log         *zap.Logger

	connMu  sync.RWMutex
	conn    *websocket.Conn
	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[string]chan wsTradeResponse
}

type wsTradeRequest struct {
	ID     string                 `json:"id"`
	Method string                 `json:"method"`
	Params map[string]interface{} `json:"params"`
}

type wsTradeResponse struct {
	ID     string          `json:"id"`
	Status int             `json:"status"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	} `json:"error"`
}

// NewWsTradeClient создаёт WsTradeClient.
// При DEV_MODE=true использует testnet.
func NewWsTradeClient(log *zap.Logger) *WsTradeClient {
	devMode := sharedconfig.GetEnv("DEV_MODE", "false") == "true"
	wsURL := "wss://ws-api.binance.com/ws-api/v3"
	restBaseURL := "https://api.binance.com"
	if devMode {
		wsURL = "wss://ws-api.testnet.binance.vision/ws-api/v3"
		restBaseURL = "https://testnet.binance.vision"
	}
	maxWait := time.Duration(sharedconfig.GetEnvInt("RECONNECT_MAX_WAIT", 60)) * time.Second

	log.Info("binance ws trade client created",
		zap.String("ws_url", wsURL),
		zap.Bool("dev_mode", devMode),
	)

	return &WsTradeClient{
		base:        wsConn{url: wsURL, log: log, maxWait: maxWait},
		apiKey:      sharedconfig.GetEnv("BINANCE_API_KEY", ""),
		secret:      sharedconfig.GetEnv("BINANCE_API_SECRET", ""),
		restBaseURL: restBaseURL,
		http:        &http.Client{Timeout: 10 * time.Second},
		log:         log,
		pending:     make(map[string]chan wsTradeResponse),
	}
}

// Run запускает WS-соединение с reconnect. Должен вызываться в горутине.
func (c *WsTradeClient) Run(ctx context.Context) {
	c.base.Run(ctx, c.onConnect)
}

// onConnect читает ответы из активного соединения и роутит по pending-каналам.
func (c *WsTradeClient) onConnect(ctx context.Context, conn *websocket.Conn) error {
	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()

	c.log.Info("binance ws trade: connected")

	defer func() {
		c.connMu.Lock()
		c.conn = nil
		c.connMu.Unlock()

		// Сбрасываем все ожидающие запросы с ошибкой.
		c.pendingMu.Lock()
		dropped := len(c.pending)
		for id, ch := range c.pending {
			ch <- wsTradeResponse{ID: id, Status: 0}
			delete(c.pending, id)
		}
		c.pendingMu.Unlock()

		if dropped > 0 {
			c.log.Warn("binance ws trade: dropped pending requests on disconnect",
				zap.Int("count", dropped),
			)
		}
	}()

	for {
		if ctx.Err() != nil {
			return nil
		}

		conn.SetReadDeadline(time.Now().Add(60 * time.Second)) //nolint:errcheck

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		var resp wsTradeResponse
		if err := json.Unmarshal(msg, &resp); err != nil {
			c.log.Error("binance ws trade: unmarshal response failed",
				zap.Error(err),
				zap.ByteString("raw", msg),
			)
			continue
		}

		c.pendingMu.Lock()
		ch, ok := c.pending[resp.ID]
		if ok {
			delete(c.pending, resp.ID)
		}
		c.pendingMu.Unlock()

		if ok {
			ch <- resp
		} else {
			c.log.Warn("binance ws trade: received response for unknown id",
				zap.String("id", resp.ID),
			)
		}
	}
}

// PlaceMarketOrder размещает рыночный ордер через WS API.
func (c *WsTradeClient) PlaceMarketOrder(ctx context.Context, symbol, side string, qty float64) (exchange.OrderResult, error) {
	c.log.Warn("binance ws trade: placing market order",
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.Float64("qty", qty),
	)

	stepSize := getStepSize(ctx, symbol, c.log)
	formattedQty := formatQtyWithStep(qty, stepSize)
	c.log.Info("binance ws trade: formatted quantity for order",
		zap.String("symbol", symbol),
		zap.Float64("raw_qty", qty),
		zap.Float64("step_size", stepSize),
		zap.String("formatted_qty", formattedQty),
	)

	params := map[string]interface{}{
		"symbol":   symbol,
		"side":     strings.ToUpper(side),
		"type":     "MARKET",
		"quantity": formattedQty,
	}
	signWS(c.apiKey, c.secret, params)

	resp, err := c.sendRequest(ctx, "order.place", params)
	if err != nil {
		return exchange.OrderResult{}, fmt.Errorf("binance ws place order: %w", err)
	}
	if resp.Status != 200 {
		msg := "unknown error"
		if resp.Error != nil {
			msg = resp.Error.Msg
		}
		return exchange.OrderResult{}, fmt.Errorf("binance ws place order: status %d: %s", resp.Status, msg)
	}

	var raw struct {
		OrderID             int64  `json:"orderId"`
		ExecutedQty         string `json:"executedQty"`
		CummulativeQuoteQty string `json:"cummulativeQuoteQty"`
		Fills               []struct {
			Commission      string `json:"commission"`
			CommissionAsset string `json:"commissionAsset"`
		} `json:"fills"`
	}
	if err := json.Unmarshal(resp.Result, &raw); err != nil {
		return exchange.OrderResult{}, fmt.Errorf("binance ws place order unmarshal: %w", err)
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

	c.log.Info("binance ws trade: order placed",
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

// CancelOrder отменяет ордер через WS API.
func (c *WsTradeClient) CancelOrder(ctx context.Context, symbol, orderID string) error {
	c.log.Info("binance ws trade: cancelling order",
		zap.String("symbol", symbol),
		zap.String("order_id", orderID),
	)

	body := map[string]interface{}{
		"symbol":  symbol,
		"orderId": orderID,
	}
	signWS(c.apiKey, c.secret, body)

	resp, err := c.sendRequest(ctx, "order.cancel", body)
	if err != nil {
		return fmt.Errorf("binance ws cancel order: %w", err)
	}
	if resp.Status != 200 {
		msg := "unknown error"
		if resp.Error != nil {
			msg = resp.Error.Msg
		}
		return fmt.Errorf("binance ws cancel order: status %d: %s", resp.Status, msg)
	}

	c.log.Info("binance ws trade: order cancelled", zap.String("order_id", orderID))
	return nil
}

// sendRequest отправляет запрос и блокируется до получения ответа или ctx.Done().
func (c *WsTradeClient) sendRequest(ctx context.Context, method string, params map[string]interface{}) (wsTradeResponse, error) {
	id := newRequestID()
	req := wsTradeRequest{ID: id, Method: method, Params: params}

	data, err := json.Marshal(req)
	if err != nil {
		return wsTradeResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	ch := make(chan wsTradeResponse, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()

	if conn == nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return wsTradeResponse{}, fmt.Errorf("binance ws trade: not connected")
	}

	start := time.Now()
	c.writeMu.Lock()
	err = conn.WriteMessage(websocket.TextMessage, data)
	c.writeMu.Unlock()

	if err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return wsTradeResponse{}, fmt.Errorf("write request: %w", err)
	}

	c.log.Info("binance ws trade: request sent",
		zap.String("id", id),
		zap.String("method", method),
		zap.ByteString("body", data),
	)

	select {
	case resp := <-ch:
		latency := time.Since(start)
		c.log.Debug("binance ws trade: response received",
			zap.String("id", id),
			zap.Int("status", resp.Status),
			zap.Duration("latency", latency),
		)
		if resp.Status == 0 {
			return wsTradeResponse{}, fmt.Errorf("binance ws trade: connection lost while waiting for response")
		}
		return resp, nil
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		c.log.Warn("binance ws trade: request timed out", zap.String("id", id), zap.String("method", method))
		return wsTradeResponse{}, ctx.Err()
	}
}

// PlaceLimitOrder размещает лимитный ордер через Binance WS API.
// При postOnly=true использует LIMIT_MAKER (отклоняется если было бы taker).
func (c *WsTradeClient) PlaceLimitOrder(ctx context.Context, symbol, side string, qty, price float64, postOnly bool) (exchange.OrderResult, error) {
	stepSize := getStepSize(ctx, symbol, c.log)
	formattedQty := formatQtyWithStep(qty, stepSize)

	params := map[string]interface{}{
		"symbol":   symbol,
		"side":     strings.ToUpper(side),
		"quantity": formattedQty,
		"price":    strconv.FormatFloat(price, 'f', -1, 64),
	}
	if postOnly {
		params["type"] = "LIMIT_MAKER"
	} else {
		params["type"] = "LIMIT"
		params["timeInForce"] = "GTC"
	}
	signWS(c.apiKey, c.secret, params)

	resp, err := c.sendRequest(ctx, "order.place", params)
	if err != nil {
		return exchange.OrderResult{}, fmt.Errorf("binance ws place limit order: %w", err)
	}
	if resp.Status != 200 {
		msg := "unknown error"
		if resp.Error != nil {
			msg = resp.Error.Msg
		}
		return exchange.OrderResult{}, fmt.Errorf("binance ws place limit order: status %d: %s", resp.Status, msg)
	}

	var raw struct {
		OrderID int64  `json:"orderId"`
		Price   string `json:"price"`
		OrigQty string `json:"origQty"`
	}
	if err := json.Unmarshal(resp.Result, &raw); err != nil {
		return exchange.OrderResult{}, fmt.Errorf("binance ws place limit order unmarshal: %w", err)
	}

	parsedPrice, _ := strconv.ParseFloat(raw.Price, 64)
	parsedQty, _ := strconv.ParseFloat(raw.OrigQty, 64)

	c.log.Info("binance ws trade: limit order placed",
		zap.String("order_id", strconv.FormatInt(raw.OrderID, 10)),
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.Float64("price", parsedPrice),
		zap.Bool("post_only", postOnly),
	)

	return exchange.OrderResult{
		OrderID: strconv.FormatInt(raw.OrderID, 10),
		Price:   parsedPrice,
		Qty:     parsedQty,
	}, nil
}

// GetOpenOrders возвращает активные ордера через REST API.
func (c *WsTradeClient) GetOpenOrders(ctx context.Context, symbol string) ([]exchange.OpenOrder, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	signREST(c.secret, params)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.restBaseURL+"/api/v3/openOrders?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-MBX-APIKEY", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("binance ws get open orders: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("binance ws get open orders: status %d body: %s", resp.StatusCode, body)
	}

	var raw []struct {
		OrderID int64  `json:"orderId"`
		Symbol  string `json:"symbol"`
		Side    string `json:"side"`
		Price   string `json:"price"`
		OrigQty string `json:"origQty"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("binance ws get open orders unmarshal: %w", err)
	}

	orders := make([]exchange.OpenOrder, 0, len(raw))
	for _, o := range raw {
		p, _ := strconv.ParseFloat(o.Price, 64)
		q, _ := strconv.ParseFloat(o.OrigQty, 64)
		orders = append(orders, exchange.OpenOrder{
			OrderID: strconv.FormatInt(o.OrderID, 10),
			Symbol:  o.Symbol,
			Side:    o.Side,
			Price:   p,
			Qty:     q,
		})
	}
	return orders, nil
}

// GetFreeUSDT возвращает свободный баланс USDT через REST API.
func (c *WsTradeClient) GetFreeUSDT(ctx context.Context) (float64, error) {
	params := url.Values{}
	signREST(c.secret, params)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.restBaseURL+"/api/v3/account?"+params.Encode(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-MBX-APIKEY", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("binance ws get free usdt: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("binance ws get free usdt: status %d", resp.StatusCode)
	}

	var raw struct {
		Balances []struct {
			Asset string `json:"asset"`
			Free  string `json:"free"`
		} `json:"balances"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0, fmt.Errorf("binance ws get free usdt unmarshal: %w", err)
	}

	for _, b := range raw.Balances {
		if b.Asset == "USDT" {
			free, _ := strconv.ParseFloat(b.Free, 64)
			return free, nil
		}
	}
	return 0, nil
}

// newRequestID генерирует уникальный ID для WS-запроса.
func newRequestID() string {
	b := make([]byte, 8)
	rand.Read(b) //nolint:errcheck
	return hex.EncodeToString(b)
}
