package bybit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/shared/stats"
	"github.com/osman/bot-traider/internal/shared/ticker"
)

// EventHandler обрабатывает входящие события от Bybit.
type EventHandler interface {
	OnTicker(t ticker.Ticker)
}

// Connection управляет одним WebSocket-соединением с Bybit.
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

	// Отправляем подписку сразу после установки соединения.
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
// Bybit шлёт JSON-ping как текстовое сообщение — обрабатываем здесь, не через SetPingHandler.
func (c *Connection) handleMessage(conn *websocket.Conn, raw []byte) error {
	// Сначала проверяем служебные op-сообщения (ping/pong).
	var op opMessage
	if err := json.Unmarshal(raw, &op); err == nil && op.Op == "ping" {
		return conn.WriteJSON(opMessage{Op: "pong"})
	}

	var msg topicMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return fmt.Errorf("unmarshal topic message: %w", err)
	}

	// Пропускаем служебные ответы на subscribe и прочие без топика.
	if msg.Topic == "" {
		return nil
	}

	// Обрабатываем только тикеры.
	if len(msg.Topic) < 8 || msg.Topic[:8] != "tickers." {
		return nil
	}

	var data TickerData
	if err := json.Unmarshal(msg.Data, &data); err != nil {
		return fmt.Errorf("unmarshal ticker data: %w", err)
	}

	if c.lastPrice[data.Symbol] == data.LastPrice {
		return nil
	}

	c.lastPrice[data.Symbol] = data.LastPrice
	c.stats.Record("bybit", len(raw))
	c.handler.OnTicker(ticker.Ticker{
		Exchange:  "bybit",
		Symbol:    data.Symbol,
		Quote:     quoteFromSymbol(data.Symbol),
		Price:     data.LastPrice,
		Open24h:   data.OpenPrice,
		High24h:   data.HighPrice24h,
		Low24h:    data.LowPrice24h,
		Volume24h: data.Volume24h,
		ChangePct: calcChangePct(data.OpenPrice, data.LastPrice),
	})
	return nil
}

// quoteFromSymbol определяет котируемую валюту по суффиксу символа.
func quoteFromSymbol(symbol string) string {
	if strings.HasSuffix(symbol, "BTC") {
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
