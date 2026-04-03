package gateio

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

// EventHandler обрабатывает входящие события от Gate.io.
type EventHandler interface {
	OnTicker(t ticker.Ticker)
}

// Connection управляет одним WebSocket-соединением с Gate.io.
type Connection struct {
	id        int
	wsURL     string
	symbols   []string
	logger    *zap.Logger
	lastPrice map[string]string
	handler   EventHandler
	maxWait   time.Duration
	stats     *stats.Stats
}

// NewConnection создаёт новое Connection.
func NewConnection(id int, symbols []string, wsURL string, log *zap.Logger, h EventHandler, maxWait time.Duration, st *stats.Stats) *Connection {
	return &Connection{
		id:        id,
		wsURL:     wsURL,
		symbols:   symbols,
		logger:    log,
		lastPrice: make(map[string]string),
		handler:   h,
		maxWait:   maxWait,
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
	if err := conn.WriteMessage(websocket.TextMessage, BuildSubscribeMsg(c.symbols)); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	c.logger.Info("connection started",
		zap.Int("conn_id", c.id),
		zap.Int("symbols", len(c.symbols)),
	)

	conn.SetPingHandler(func(data string) error {
		return conn.WriteControl(
			websocket.PongMessage,
			[]byte(data),
			time.Now().Add(5*time.Second),
		)
	})

	for {
		if ctx.Err() != nil {
			return nil
		}

		conn.SetReadDeadline(time.Now().Add(60 * time.Second)) //nolint:errcheck

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		c.handleMessage(msg)
	}
}

// handleMessage разбирает входящее сообщение и вызывает нужный обработчик.
func (c *Connection) handleMessage(raw []byte) {
	var msg WsMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		c.logger.Error("unmarshal ws message", zap.Error(err))
		return
	}

	if msg.Channel != "spot.tickers" || msg.Event != "update" {
		return
	}

	var result TickerResult
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		c.logger.Error("unmarshal ticker", zap.Error(err))
		return
	}

	if c.lastPrice[result.CurrencyPair] == result.Last {
		return
	}

	changePct := calcChangePct(result.OpenPrice, result.Last)
	c.lastPrice[result.CurrencyPair] = result.Last
	c.stats.Record("gateio", len(raw))
	c.handler.OnTicker(ticker.Ticker{
		Exchange:  "gateio",
		Symbol:    result.CurrencyPair,
		Quote:     quoteFromSymbol(result.CurrencyPair),
		Price:     result.Last,
		Open24h:   result.OpenPrice,
		High24h:   result.HighPrice,
		Low24h:    result.LowPrice,
		Volume24h: result.BaseVolume,
		ChangePct: changePct,
		CreatedAt: time.Now(),
	})
}

// quoteFromSymbol определяет котируемую валюту из пары формата BASE_QUOTE.
func quoteFromSymbol(symbol string) string {
	if i := strings.LastIndex(symbol, "_"); i >= 0 {
		return symbol[i+1:]
	}
	return symbol
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
