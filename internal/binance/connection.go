package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// EventHandler обрабатывает входящие события от Binance.
type EventHandler interface {
	OnTrade(event TradeEvent)
	OnTicker(event MiniTickerEvent, changePct string)
}

// Connection управляет одним WebSocket-соединением с Binance.
type Connection struct {
	id        int
	url       string
	symbols   []string
	logger    *zap.Logger
	lastPrice map[string]string
	handler   EventHandler
	maxWait   time.Duration
}

// NewConnection создаёт новое Connection.
func NewConnection(id int, symbols []string, log *zap.Logger, h EventHandler, maxWait time.Duration) *Connection {
	return &Connection{
		id:        id,
		url:       BuildStreamURL(symbols),
		symbols:   symbols,
		logger:    log,
		lastPrice: make(map[string]string),
		handler:   h,
		maxWait:   maxWait,
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
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.url, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	c.logger.Info("connection started",
		zap.Int("conn_id", c.id),
		zap.Int("symbols", len(c.symbols)),
	)

	// Обработка Ping — отвечаем Pong.
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

		// Сбрасываем таймаут при каждом успешном ReadMessage.
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
	var sm StreamMessage
	if err := json.Unmarshal(raw, &sm); err != nil {
		c.logger.Error("unmarshal stream message", zap.Error(err))
		return
	}

	if len(sm.Stream) == 0 {
		return
	}

	switch {
	// case len(sm.Stream) > 6 && sm.Stream[len(sm.Stream)-6:] == "@trade":
	// 	var event TradeEvent
	// 	if err := json.Unmarshal(sm.Data, &event); err != nil {
	// 		c.logger.Error("unmarshal trade", zap.Error(err))
	// 		return
	// 	}
	// 	c.handler.OnTrade(event)

	case len(sm.Stream) > 11 && sm.Stream[len(sm.Stream)-11:] == "@miniTicker":
		var event MiniTickerEvent
		if err := json.Unmarshal(sm.Data, &event); err != nil {
			c.logger.Error("unmarshal miniTicker", zap.Error(err))
			return
		}

		// Логируем только при изменении цены.
		if c.lastPrice[event.Symbol] == event.LastPrice {
			return
		}

		changePct := calcChangePct(event.OpenPrice, event.LastPrice)
		c.lastPrice[event.Symbol] = event.LastPrice
		c.handler.OnTicker(event, changePct)
	}
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
