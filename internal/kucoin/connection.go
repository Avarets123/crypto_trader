package kucoin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/shared/stats"
	"github.com/osman/bot-traider/internal/ticker"
)

// EventHandler обрабатывает входящие события от KuCoin.
type EventHandler interface {
	OnTicker(t ticker.Ticker)
}

// Connection управляет одним WebSocket-соединением с KuCoin market data.
type Connection struct {
	id        int
	restURL   string
	symbols   []string // внутренний формат (BTCUSDT)
	logger    *zap.Logger
	lastPrice map[string]string
	handler   EventHandler
	stats     *stats.Stats
	maxWait   time.Duration
}

// NewConnection создаёт новое Connection.
func NewConnection(id int, symbols []string, restURL string, log *zap.Logger, h EventHandler, maxWait time.Duration, st *stats.Stats) *Connection {
	return &Connection{
		id:        id,
		restURL:   restURL,
		symbols:   symbols,
		logger:    log,
		lastPrice: make(map[string]string),
		handler:   h,
		stats:     st,
		maxWait:   maxWait,
	}
}

// Run запускает соединение с exponential backoff до ctx.Done().
func (c *Connection) Run(ctx context.Context) {
	wait := time.Second
	attempt := 0

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := c.connect(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			attempt++
			c.logger.Warn("kucoin: connection failed, reconnecting",
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
			wait = nextWait(wait, c.maxWait)
		} else {
			attempt = 0
			wait = time.Second
		}
	}
}

// connect выполняет одну попытку подключения: получает токен, коннектится, читает сообщения.
func (c *Connection) connect(ctx context.Context) error {
	// 1. Получаем WS токен
	token, wsBaseURL, pingMs, err := c.getWsTokenFull(ctx)
	if err != nil {
		return fmt.Errorf("get ws token: %w", err)
	}

	// 2. Подключаемся
	cid := newConnectID()
	fullURL := wsBaseURL + "?token=" + token + "&connectId=" + cid

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, fullURL, nil)
	if err != nil {
		return fmt.Errorf("dial %s: %w", wsBaseURL, err)
	}
	defer conn.Close()

	c.logger.Info("kucoin: ws connected",
		zap.Int("conn_id", c.id),
		zap.Int("symbols", len(c.symbols)),
	)

	// 3. Ждём welcome-сообщение
	conn.SetReadDeadline(time.Now().Add(15 * time.Second)) //nolint:errcheck
	_, rawMsg, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read welcome: %w", err)
	}
	var welcome WsMessage
	if err := json.Unmarshal(rawMsg, &welcome); err != nil || welcome.Type != "welcome" {
		return fmt.Errorf("expected welcome, got: %s", rawMsg)
	}
	c.logger.Info("kucoin: received welcome", zap.Int("conn_id", c.id))

	// 4. Подписываемся на снимки рынка
	subID := newConnectID()
	topic := BuildSnapshotTopic(c.symbols)
	subMsg := map[string]interface{}{
		"id":             subID,
		"type":           "subscribe",
		"topic":          topic,
		"privateChannel": false,
		"response":       true,
	}
	data, _ := json.Marshal(subMsg)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("send subscribe: %w", err)
	}

	// Ждём ack
	conn.SetReadDeadline(time.Now().Add(15 * time.Second)) //nolint:errcheck
	_, rawAck, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read ack: %w", err)
	}
	var ack WsMessage
	if err := json.Unmarshal(rawAck, &ack); err != nil {
		return fmt.Errorf("unmarshal ack: %w", err)
	}
	if ack.Type != "ack" {
		c.logger.Warn("kucoin: unexpected response after subscribe",
			zap.String("type", ack.Type),
			zap.Int("conn_id", c.id),
		)
	}
	c.logger.Info("kucoin: subscribed", zap.Int("conn_id", c.id), zap.String("topic", topic[:min(50, len(topic))]))

	// 5. Запускаем пинг-горутину (клиентские пинги для поддержания соединения)
	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()

	go func() {
		pingInterval := time.Duration(pingMs) * time.Millisecond
		if pingInterval <= 0 {
			pingInterval = 18 * time.Second
		}
		// Шлём пинг каждые pingInterval/2 чтобы не дождаться таймаута сервера
		t := time.NewTicker(pingInterval / 2)
		defer t.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-t.C:
				pid := newConnectID()
				ping := map[string]string{"id": pid, "type": "ping"}
				d, _ := json.Marshal(ping)
				conn.WriteMessage(websocket.TextMessage, d) //nolint:errcheck
			}
		}
	}()

	// 6. Основной цикл чтения сообщений
	return c.readLoop(ctx, conn)
}

// getWsTokenFull получает WS токен, URL сервера и интервал пинга.
func (c *Connection) getWsTokenFull(ctx context.Context) (token, wsURL string, pingIntervalMs int, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.restURL+"/api/v1/bullet-public", nil)
	if err != nil {
		return "", "", 0, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", 0, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	var result WsTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", 0, fmt.Errorf("decode token response: %w", err)
	}
	if result.Code != "200000" {
		return "", "", 0, fmt.Errorf("kucoin token api error: code %s", result.Code)
	}
	if len(result.Data.InstanceServers) == 0 {
		return "", "", 0, fmt.Errorf("no ws instance servers in token response")
	}

	srv := result.Data.InstanceServers[0]
	return result.Data.Token, srv.Endpoint, srv.PingInterval, nil
}

// readLoop читает и обрабатывает WS-сообщения до ошибки или ctx.Done().
func (c *Connection) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		conn.SetReadDeadline(time.Now().Add(60 * time.Second)) //nolint:errcheck

		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		c.handleMessage(conn, raw)
	}
}

// handleMessage разбирает входящее сообщение и вызывает нужный обработчик.
func (c *Connection) handleMessage(conn *websocket.Conn, raw []byte) {
	var msg WsMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		c.logger.Error("kucoin: unmarshal ws message", zap.Error(err))
		return
	}

	switch msg.Type {
	case "ping":
		// Отвечаем на серверный пинг
		pong := map[string]string{"id": msg.ID, "type": "pong"}
		d, _ := json.Marshal(pong)
		conn.WriteMessage(websocket.TextMessage, d) //nolint:errcheck

	case "message":
		c.handleMarketMessage(msg, raw)

	case "pong", "welcome", "ack":
		// Игнорируем

	default:
		c.logger.Debug("kucoin: unknown message type", zap.String("type", msg.Type))
	}
}

// handleMarketMessage обрабатывает рыночные данные из снимка рынка.
func (c *Connection) handleMarketMessage(msg WsMessage, raw []byte) {
	if msg.Subject != "trade.snapshot" {
		return
	}

	var wrapper SnapshotWrapper
	if err := json.Unmarshal(msg.Data, &wrapper); err != nil {
		c.logger.Error("kucoin: unmarshal snapshot wrapper", zap.Error(err))
		return
	}

	snap := wrapper.Data
	if !snap.Trading {
		return
	}

	price := string(snap.LastTradedPrice)
	if price == "" {
		price = string(snap.Close)
	}

	// Дедупликация — пропускаем если цена не изменилась
	internalSymbol := fromKucoinSymbol(snap.Symbol)
	if c.lastPrice[internalSymbol] == price {
		return
	}
	c.lastPrice[internalSymbol] = price

	open := calcOpen(price, string(snap.ChangePrice))
	changePct := calcChangePctFromRate(string(snap.ChangeRate))
	quote := quoteFromKucoinSymbol(snap.Symbol)

	c.stats.Record("kucoin", len(raw))
	c.handler.OnTicker(ticker.Ticker{
		Exchange:  "kucoin",
		Symbol:    internalSymbol,
		Quote:     quote,
		Price:     price,
		Open24h:   open,
		High24h:   string(snap.High),
		Low24h:    string(snap.Low),
		Volume24h: string(snap.Vol),
		ChangePct: changePct,
		CreatedAt: time.Now(),
	})
}

func nextWait(current, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}
	return next
}
