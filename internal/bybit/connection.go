package bybit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/shared/stats"
	"github.com/osman/bot-traider/internal/ticker"
)

// EventHandler обрабатывает входящие события от Bybit.
type EventHandler interface {
	OnTicker(t ticker.Ticker)
}

// Connection управляет одним WebSocket-соединением с Bybit market data.
type Connection struct {
	id        int
	base      wsConn
	symbols   []string
	logger    *zap.Logger
	handler   EventHandler
	lastPrice map[string]string
	openPrice map[string]string // кешируем openPrice из snapshot для дельта-обновлений
	stats     *stats.Stats
}

// NewConnection создаёт новое Connection.
func NewConnection(id int, symbols []string, wsURL string, log *zap.Logger, h EventHandler, maxWait time.Duration, st *stats.Stats) *Connection {
	return &Connection{
		id:        id,
		base:      wsConn{url: wsURL, log: log, maxWait: maxWait},
		symbols:   symbols,
		logger:    log,
		handler:   h,
		lastPrice: make(map[string]string),
		openPrice: make(map[string]string),
		stats:     st,
	}
}

// Run запускает соединение с exponential backoff до ctx.Done().
func (c *Connection) Run(ctx context.Context) {
	c.base.Run(ctx, c.onConnect)
}

// onConnect отправляет подписку и читает сообщения до ошибки или ctx.Done().
func (c *Connection) onConnect(ctx context.Context, conn *websocket.Conn) error {
	sub := subscribeMsg{Op: "subscribe", Args: BuildTopics(c.symbols)}
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
// Bybit шлёт JSON-ping как текстовое сообщение — обрабатываем здесь.
func (c *Connection) handleMessage(conn *websocket.Conn, raw []byte) error {
	var op opMessage
	if err := json.Unmarshal(raw, &op); err == nil && op.Op == "ping" {
		return conn.WriteJSON(opMessage{Op: "pong"})
	}

	var msg topicMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return fmt.Errorf("unmarshal topic message: %w", err)
	}

	if msg.Topic == "" {
		return nil
	}

	if len(msg.Topic) < 8 || msg.Topic[:8] != "tickers." {
		return nil
	}

	var data TickerData
	if err := json.Unmarshal(msg.Data, &data); err != nil {
		return fmt.Errorf("unmarshal ticker data: %w", err)
	}

	if data.OpenPrice != "" {
		c.openPrice[data.Symbol] = data.OpenPrice
	}

	if c.lastPrice[data.Symbol] == data.LastPrice {
		return nil
	}

	c.lastPrice[data.Symbol] = data.LastPrice

	openPrice := data.OpenPrice
	if openPrice == "" {
		openPrice = c.openPrice[data.Symbol]
	}

	c.stats.Record("bybit", len(raw))
	c.handler.OnTicker(ticker.Ticker{
		Exchange:  "bybit",
		Symbol:    data.Symbol,
		Quote:     quoteFromSymbol(data.Symbol),
		Price:     data.LastPrice,
		Open24h:   openPrice,
		High24h:   data.HighPrice24h,
		Low24h:    data.LowPrice24h,
		Volume24h: data.Volume24h,
		ChangePct: calcChangePct(openPrice, data.LastPrice),
		CreatedAt: time.Now(),
	})
	return nil
}
