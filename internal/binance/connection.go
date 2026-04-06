package binance

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

// EventHandler обрабатывает входящие события от Binance.
type EventHandler interface {
	OnTrade(event TradeEvent)
	OnTicker(t ticker.Ticker)
}

// Connection управляет одним WebSocket-соединением с Binance market data.
type Connection struct {
	id        int
	base      wsConn
	symbols   []string
	logger    *zap.Logger
	lastPrice map[string]string
	handler   EventHandler
	stats     *stats.Stats
}

// NewConnection создаёт новое Connection.
func NewConnection(id int, symbols []string, wsBaseURL string, log *zap.Logger, h EventHandler, maxWait time.Duration, st *stats.Stats) *Connection {
	return &Connection{
		id:        id,
		base:      wsConn{url: BuildStreamURL(wsBaseURL, symbols), log: log, maxWait: maxWait},
		symbols:   symbols,
		logger:    log,
		lastPrice: make(map[string]string),
		handler:   h,
		stats:     st,
	}
}

// Run запускает соединение с exponential backoff до ctx.Done().
func (c *Connection) Run(ctx context.Context) {
	c.base.Run(ctx, c.onConnect)
}

// onConnect читает сообщения из активного WS-соединения до ошибки или ctx.Done().
func (c *Connection) onConnect(ctx context.Context, conn *websocket.Conn) error {
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
	case len(sm.Stream) > 11 && sm.Stream[len(sm.Stream)-11:] == "@miniTicker":
		var event MiniTickerEvent
		if err := json.Unmarshal(sm.Data, &event); err != nil {
			c.logger.Error("unmarshal miniTicker", zap.Error(err))
			return
		}

		if c.lastPrice[event.Symbol] == event.LastPrice {
			return
		}

		changePct := calcChangePct(event.OpenPrice, event.LastPrice)
		c.lastPrice[event.Symbol] = event.LastPrice
		c.stats.Record("binance", len(raw))
		c.handler.OnTicker(ticker.Ticker{
			Exchange:  "binance",
			Symbol:    event.Symbol,
			Quote:     quoteFromSymbol(event.Symbol),
			Price:     event.LastPrice,
			Open24h:   event.OpenPrice,
			High24h:   event.HighPrice,
			Low24h:    event.LowPrice,
			Volume24h: event.BaseVolume,
			ChangePct: changePct,
			CreatedAt: time.Now(),
		})
	}
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
