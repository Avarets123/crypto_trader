package okx

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/shared/stats"
	"github.com/osman/bot-traider/internal/ticker"
)

// EventHandler обрабатывает входящие события от OKX.
type EventHandler interface {
	OnTicker(t ticker.Ticker)
}

// Connection управляет одним WebSocket-соединением с OKX.
type Connection struct {
	id        int
	wsURL     string
	symbols   []string
	logger    *zap.Logger
	handler   EventHandler
	maxWait   time.Duration
	lastPrice map[string]string
	stats     *stats.Stats
}

// NewConnection создаёт новое Connection.
func NewConnection(id int, symbols []string, wsURL string, log *zap.Logger, h EventHandler, maxWait time.Duration, st *stats.Stats) *Connection {
	return &Connection{
		id:        id,
		wsURL:     wsURL,
		symbols:   symbols,
		logger:    log,
		handler:   h,
		maxWait:   maxWait,
		lastPrice: make(map[string]string),
		stats:     st,
	}
}

// Run запускает соединение с экспоненциальным backoff до ctx.Done().
func (c *Connection) Run(ctx context.Context) {
	wait := time.Second
	attempt := 0

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := c.connect(ctx)
		if err == nil || ctx.Err() != nil {
			return
		}

		attempt++
		c.logger.Warn("reconnecting",
			zap.Int("conn_id", c.id),
			zap.Int("attempt", attempt),
			zap.Duration("wait", wait),
			zap.Error(err),
		)

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		wait *= 2
		if wait > c.maxWait {
			wait = c.maxWait
		}
	}
}

// connect устанавливает одно WS-соединение и читает сообщения до ошибки или ctx.Done().
func (c *Connection) connect(ctx context.Context) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	sub := subscribeMsg{Op: "subscribe", Args: BuildArgs(c.symbols)}
	if err := conn.WriteJSON(sub); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	c.logger.Info("connection started",
		zap.Int("conn_id", c.id),
		zap.Int("symbols", len(c.symbols)),
	)

	for {
		if ctx.Err() != nil {
			return nil
		}

		conn.SetReadDeadline(time.Now().Add(60 * time.Second)) //nolint:errcheck

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		if err := c.handleMessage(conn, msg); err != nil {
			c.logger.Error("handle message", zap.Error(err))
		}
	}
}

// handleMessage разбирает входящее сообщение и вызывает нужный обработчик.
// OKX шлёт ping как текстовое сообщение "ping" — обрабатываем здесь, не через SetPingHandler.
func (c *Connection) handleMessage(conn *websocket.Conn, raw []byte) error {
	if string(raw) == "ping" {
		return conn.WriteMessage(websocket.TextMessage, []byte("pong"))
	}

	var msg tickerMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return fmt.Errorf("unmarshal message: %w", err)
	}

	// Пропускаем служебные ответы (subscribe ack и прочие без data).
	if len(msg.Data) == 0 {
		return nil
	}

	data := msg.Data[0]

	if c.lastPrice[data.InstID] == data.Last {
		return nil
	}

	c.lastPrice[data.InstID] = data.Last
	c.stats.Record("okx", len(raw))
	c.handler.OnTicker(ticker.Ticker{
		Exchange:  "okx",
		Symbol:    data.InstID,
		Quote:     quoteFromSymbol(data.InstID),
		Price:     data.Last,
		Open24h:   data.Open24h,
		High24h:   data.High24h,
		Low24h:    data.Low24h,
		Volume24h: data.Vol24h,
		ChangePct: calcChangePct(data.Open24h, data.Last),
		CreatedAt: time.Now(),
	})
	return nil
}

// quoteFromSymbol определяет котируемую валюту по суффиксу символа (формат BTC-USDT).
func quoteFromSymbol(symbol string) string {
	if strings.HasSuffix(symbol, "-BTC") {
		return "BTC"
	}
	return "USDT"
}

// calcChangePct вычисляет процентное изменение цены относительно открытия.
func calcChangePct(open, last string) string {
	var o, l float64
	fmt.Sscanf(open, "%f", &o)
	fmt.Sscanf(last, "%f", &l)
	if o == 0 {
		return "0.00%"
	}
	pct := (l - o) / o * 100
	if pct >= 0 {
		return fmt.Sprintf("+%.2f%%", pct)
	}
	return fmt.Sprintf("%.2f%%", pct)
}
