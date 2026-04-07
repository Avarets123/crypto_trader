package bybit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// WsTradeClient реализует exchange.RestClient через Bybit Private WebSocket API.
// Соединение аутентифицируется один раз при подключении.
type WsTradeClient struct {
	base    wsConn
	apiKey  string
	secret  string
	log     *zap.Logger

	connMu  sync.RWMutex
	conn    *websocket.Conn
	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[string]chan wsTradeResponse
}

type wsTradeRequest struct {
	ReqID  string        `json:"reqId"`
	Header wsTradeHeader `json:"header"`
	Op     string        `json:"op"`
	Args   []interface{} `json:"args"`
}

type wsTradeHeader struct {
	Timestamp  string `json:"X-BAPI-TIMESTAMP"`
	RecvWindow string `json:"X-BAPI-RECV-WINDOW"`
}

type wsTradeResponse struct {
	ReqID   string          `json:"reqId"`
	RetCode int             `json:"retCode"`
	RetMsg  string          `json:"retMsg"`
	Op      string          `json:"op"`
	Data    json.RawMessage `json:"data"`
}

// NewWsTradeClient создаёт WsTradeClient.
// При DEV_MODE=true использует testnet.
func NewWsTradeClient(log *zap.Logger) *WsTradeClient {
	devMode := sharedconfig.GetEnv("DEV_MODE", "false") == "true"
	wsURL := "wss://stream.bybit.com/v5/private"
	if devMode {
		wsURL = "wss://stream-testnet.bybit.com/v5/private"
	}
	maxWait := time.Duration(sharedconfig.GetEnvInt("RECONNECT_MAX_WAIT", 60)) * time.Second

	log.Info("bybit ws trade client created",
		zap.String("ws_url", wsURL),
		zap.Bool("dev_mode", devMode),
	)

	return &WsTradeClient{
		base:    wsConn{url: wsURL, log: log, maxWait: maxWait},
		apiKey:  sharedconfig.GetEnv("BYBIT_API_KEY", ""),
		secret:  sharedconfig.GetEnv("BYBIT_API_SECRET", ""),
		log:     log,
		pending: make(map[string]chan wsTradeResponse),
	}
}

// Run запускает WS-соединение с reconnect. Должен вызываться в горутине.
func (c *WsTradeClient) Run(ctx context.Context) {
	c.base.Run(ctx, c.onConnect)
}

// onConnect аутентифицирует соединение и читает ответы до разрыва.
func (c *WsTradeClient) onConnect(ctx context.Context, conn *websocket.Conn) error {
	// Аутентифицируем соединение.
	if err := c.sendAuth(conn); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	c.log.Info("bybit ws trade: authenticated")

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()

	defer func() {
		c.connMu.Lock()
		c.conn = nil
		c.connMu.Unlock()

		c.pendingMu.Lock()
		dropped := len(c.pending)
		for id, ch := range c.pending {
			ch <- wsTradeResponse{ReqID: id, RetCode: -1, RetMsg: "connection lost"}
			delete(c.pending, id)
		}
		c.pendingMu.Unlock()

		if dropped > 0 {
			c.log.Warn("bybit ws trade: dropped pending requests on disconnect",
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

		// JSON-пинг Bybit.
		var op opMessage
		if jsonErr := json.Unmarshal(msg, &op); jsonErr == nil && op.Op == "ping" {
			if writeErr := conn.WriteJSON(opMessage{Op: "pong"}); writeErr != nil {
				return fmt.Errorf("pong: %w", writeErr)
			}
			continue
		}

		var resp wsTradeResponse
		if err := json.Unmarshal(msg, &resp); err != nil {
			c.log.Error("bybit ws trade: unmarshal response failed",
				zap.Error(err),
				zap.ByteString("raw", msg),
			)
			continue
		}

		if resp.ReqID == "" {
			c.log.Debug("bybit ws trade: received message without reqId",
				zap.String("op", resp.Op),
				zap.Int("ret_code", resp.RetCode),
			)
			continue
		}

		c.pendingMu.Lock()
		ch, ok := c.pending[resp.ReqID]
		if ok {
			delete(c.pending, resp.ReqID)
		}
		c.pendingMu.Unlock()

		if ok {
			ch <- resp
		} else {
			c.log.Warn("bybit ws trade: received response for unknown reqId",
				zap.String("req_id", resp.ReqID),
			)
		}
	}
}

// sendAuth отправляет auth-сообщение для аутентификации соединения.
func (c *WsTradeClient) sendAuth(conn *websocket.Conn) error {
	expires := strconv.FormatInt(time.Now().Add(time.Second).UnixMilli(), 10)
	sig := signBybit(c.secret, "GET/realtime"+expires)

	msg, _ := json.Marshal(map[string]interface{}{
		"op":   "auth",
		"args": []interface{}{c.apiKey, expires, sig},
	})

	c.log.Debug("bybit ws trade: sending auth")
	return conn.WriteMessage(websocket.TextMessage, msg)
}

// PlaceMarketOrder размещает рыночный ордер через Bybit Private WS API.
func (c *WsTradeClient) PlaceMarketOrder(ctx context.Context, symbol, side string, qty float64) (exchange.OrderResult, error) {
	c.log.Warn("bybit ws trade: placing market order",
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.Float64("qty", qty),
	)

	
	bySide := strings.Title(strings.ToLower(side)) 

	args := map[string]interface{}{
		"category":  "spot",
		"symbol":    symbol,
		"side":      bySide,
		"orderType": "Market",
		"qty":       formatQty(qty),
	}

	resp, err := c.sendRequest(ctx, "order.create", args)
	if err != nil {
		return exchange.OrderResult{}, fmt.Errorf("bybit ws place order: %w", err)
	}
	if resp.RetCode != 0 {
		return exchange.OrderResult{}, fmt.Errorf("bybit ws place order api error: %d %s", resp.RetCode, resp.RetMsg)
	}

	var raw struct {
		OrderID string `json:"orderId"`
	}
	if err := json.Unmarshal(resp.Data, &raw); err != nil {
		return exchange.OrderResult{}, fmt.Errorf("bybit ws place order unmarshal: %w", err)
	}

	// Bybit WS не возвращает fills в ответе на создание ордера.
	// Применяем приближение: комиссия 0.1% списывается из полученного базового актива при BUY.
	netQty := qty
	if strings.EqualFold(side, "buy") {
		netQty = qty * 0.999
	}

	c.log.Info("bybit ws trade: order placed",
		zap.String("order_id", raw.OrderID),
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.Float64("requested_qty", qty),
		zap.Float64("net_qty", netQty),
	)

	result := exchange.OrderResult{
		OrderID: raw.OrderID,
		Qty:     netQty,
	}
	return result, nil
}

// CancelOrder отменяет ордер через Bybit Private WS API.
func (c *WsTradeClient) CancelOrder(ctx context.Context, symbol, orderID string) error {
	c.log.Info("bybit ws trade: cancelling order",
		zap.String("symbol", symbol),
		zap.String("order_id", orderID),
	)

	args := map[string]interface{}{
		"category": "spot",
		"symbol":   symbol,
		"orderId":  orderID,
	}

	resp, err := c.sendRequest(ctx, "order.cancel", args)
	if err != nil {
		return fmt.Errorf("bybit ws cancel order: %w", err)
	}
	if resp.RetCode != 0 {
		return fmt.Errorf("bybit ws cancel order api error: %d %s", resp.RetCode, resp.RetMsg)
	}

	c.log.Info("bybit ws trade: order cancelled", zap.String("order_id", orderID))
	return nil
}

// sendRequest отправляет WS-запрос и блокируется до получения ответа или ctx.Done().
func (c *WsTradeClient) sendRequest(ctx context.Context, op string, args interface{}) (wsTradeResponse, error) {
	reqID := newReqID()
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)

	req := wsTradeRequest{
		ReqID:  reqID,
		Header: wsTradeHeader{Timestamp: timestamp, RecvWindow: "5000"},
		Op:     op,
		Args:   []interface{}{args},
	}

	data, err := json.Marshal(req)
	if err != nil {
		return wsTradeResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	ch := make(chan wsTradeResponse, 1)
	c.pendingMu.Lock()
	c.pending[reqID] = ch
	c.pendingMu.Unlock()

	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()

	if conn == nil {
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return wsTradeResponse{}, fmt.Errorf("bybit ws trade: not connected")
	}

	start := time.Now()
	c.writeMu.Lock()
	err = conn.WriteMessage(websocket.TextMessage, data)
	c.writeMu.Unlock()

	if err != nil {
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return wsTradeResponse{}, fmt.Errorf("write request: %w", err)
	}

	c.log.Debug("bybit ws trade: request sent",
		zap.String("req_id", reqID),
		zap.String("op", op),
	)

	select {
	case resp := <-ch:
		latency := time.Since(start)
		c.log.Debug("bybit ws trade: response received",
			zap.String("req_id", reqID),
			zap.Int("ret_code", resp.RetCode),
			zap.Duration("latency", latency),
		)
		if resp.RetCode == -1 {
			return wsTradeResponse{}, fmt.Errorf("bybit ws trade: connection lost while waiting for response")
		}
		return resp, nil
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		c.log.Warn("bybit ws trade: request timed out",
			zap.String("req_id", reqID),
			zap.String("op", op),
		)
		return wsTradeResponse{}, ctx.Err()
	}
}

// newReqID генерирует уникальный ID для WS-запроса.
func newReqID() string {
	b := make([]byte, 8)
	rand.Read(b) //nolint:errcheck
	return hex.EncodeToString(b)
}
