package kucoin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	exchange_orders "github.com/osman/bot-traider/internal/exchange_orders"
	"go.uber.org/zap"
)

const (
	tradeFlushSize     = 100
	tradeFlushInterval = 500 * time.Millisecond
)

// TradeFetcher подписывается на WS-стрим /market/match KuCoin для группы символов
// на одном WS-соединении. Поддерживает батч-сохранение в БД и realtime-хук.
type TradeFetcher struct {
	rest    *RestClient
	onTrade func(exchange_orders.ExchangeOrder)
	onSave  func(ctx context.Context, orders []exchange_orders.ExchangeOrder) error
	log     *zap.Logger
}

// NewTradeFetcher создаёт TradeFetcher.
func NewTradeFetcher(rest *RestClient, log *zap.Logger) *TradeFetcher {
	return &TradeFetcher{rest: rest, log: log}
}

// WithOnTrade устанавливает realtime-хук — вызывается на каждой сделке (для microscalping).
func (f *TradeFetcher) WithOnTrade(fn func(exchange_orders.ExchangeOrder)) {
	f.onTrade = fn
}

// WithOnSave устанавливает хук батч-сохранения в БД.
func (f *TradeFetcher) WithOnSave(fn func(ctx context.Context, orders []exchange_orders.ExchangeOrder) error) {
	f.onSave = fn
}

// Subscribe открывает WS-подписку /market/match:{sym1,sym2,...} с reconnect до ctx.Done().
// id используется только для логирования.
func (f *TradeFetcher) Subscribe(ctx context.Context, id int, symbols []string) {
	maxWait := 60 * time.Second
	wait := time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := f.connect(ctx, id, symbols); err != nil {
			if ctx.Err() != nil {
				return
			}
			f.log.Warn("kucoin trade: reconnecting",
				zap.Int("conn_id", id),
				zap.Duration("wait", wait),
				zap.Error(err),
			)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = nextWait(wait, maxWait)
		} else {
			wait = time.Second
		}
	}
}

func (f *TradeFetcher) connect(ctx context.Context, id int, symbols []string) error {
	token, wsBaseURL, pingMs, err := f.rest.GetWsToken(ctx)
	if err != nil {
		return fmt.Errorf("get ws token: %w", err)
	}

	cid := newConnectID()
	fullURL := wsBaseURL + "?token=" + token + "&connectId=" + cid

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, fullURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// Ждём welcome
	conn.SetReadDeadline(time.Now().Add(15 * time.Second)) //nolint:errcheck
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read welcome: %w", err)
	}
	var welcome WsMessage
	if err := json.Unmarshal(raw, &welcome); err != nil || welcome.Type != "welcome" {
		return fmt.Errorf("expected welcome, got: %s", raw)
	}

	// Подписываемся на /market/match:SYM1,SYM2,... (один запрос на все символы чанка)
	kcSymbols := make([]string, len(symbols))
	for i, s := range symbols {
		kcSymbols[i] = toKucoinSymbol(s)
	}
	topic := "/market/match:" + strings.Join(kcSymbols, ",")

	subMsg := map[string]interface{}{
		"id":             newConnectID(),
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
	if ack.Type == "error" {
		return fmt.Errorf("kucoin trade: subscribe rejected by server: %s", string(rawAck))
	}
	if ack.Type != "ack" {
		f.log.Warn("kucoin trade: unexpected ack type",
			zap.String("type", ack.Type),
			zap.String("raw", string(rawAck)),
			zap.Int("conn_id", id),
		)
	}

	f.log.Info("kucoin trade: subscribed",
		zap.Int("conn_id", id),
		zap.Int("symbols", len(symbols)),
		zap.Strings("list", symbols),
	)

	// Единый writer-канал — gorilla/websocket требует "один reader + один writer".
	// Все записи (ping-клиент + pong-ответы) идут через этот канал.
	writes := make(chan []byte, 16)

	writeCtx, writeCancel := context.WithCancel(ctx)
	defer writeCancel()

	// Writer-горутина — единственный горутин, пишущий в conn.
	go func() {
		for {
			select {
			case <-writeCtx.Done():
				return
			case msg, ok := <-writes:
				if !ok {
					return
				}
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					f.log.Warn("kucoin trade: write failed", zap.Int("conn_id", id), zap.Error(err))
					return
				}
			}
		}
	}()

	// Пинг-горутина — шлёт ping через writes-канал.
	go func() {
		pingInterval := time.Duration(pingMs) * time.Millisecond
		if pingInterval <= 0 {
			pingInterval = 18 * time.Second
		}
		t := time.NewTicker(pingInterval / 2)
		defer t.Stop()
		for {
			select {
			case <-writeCtx.Done():
				return
			case <-t.C:
				pid := newConnectID()
				ping := map[string]string{"id": pid, "type": "ping"}
				d, _ := json.Marshal(ping)
				select {
				case writes <- d:
				case <-writeCtx.Done():
					return
				}
			}
		}
	}()

	return f.readLoop(ctx, conn, id, writes)
}

func (f *TradeFetcher) readLoop(ctx context.Context, conn *websocket.Conn, id int, writes chan<- []byte) error {
	buf := make([]exchange_orders.ExchangeOrder, 0, tradeFlushSize)
	flushTicker := time.NewTicker(tradeFlushInterval)
	defer flushTicker.Stop()

	flush := func() {
		if len(buf) == 0 || f.onSave == nil {
			buf = buf[:0]
			return
		}
		if err := f.onSave(ctx, buf); err != nil {
			f.log.Warn("kucoin trade: save batch failed",
				zap.Int("conn_id", id),
				zap.Int("count", len(buf)),
				zap.Error(err),
			)
		}
		buf = buf[:0]
	}

	msgs := make(chan []byte, 512)
	go func() {
		defer close(msgs)
		for {
			conn.SetReadDeadline(time.Now().Add(60 * time.Second)) //nolint:errcheck
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			select {
			case msgs <- raw:
			default:
				f.log.Warn("kucoin trade: msg buffer full, dropping", zap.Int("conn_id", id))
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			flush()
			return nil

		case raw, ok := <-msgs:
			if !ok {
				flush()
				return fmt.Errorf("ws connection closed")
			}

			// Быстрый разбор типа для обработки серверных ping и диагностики
			var hdr WsMessage
			if err := json.Unmarshal(raw, &hdr); err == nil {
				switch hdr.Type {
				case "ping":
					// Серверный ping — отвечаем pong через writes-канал (единственный writer-горутина).
					pong := map[string]string{"id": hdr.ID, "type": "pong"}
					d, _ := json.Marshal(pong)
					select {
					case writes <- d:
					default:
						f.log.Warn("kucoin trade: pong dropped (writes full)", zap.Int("conn_id", id))
					}
					continue
				case "pong", "ack", "welcome":
					continue
				case "message":
					// нормальный путь — парсим ниже
				default:
					f.log.Debug("kucoin trade: unknown msg type",
						zap.String("type", hdr.Type),
						zap.String("subject", hdr.Subject),
						zap.Int("conn_id", id),
					)
					continue
				}
			}

			order := f.parseOrder(raw)
			if order == nil {
				continue
			}
			if f.onTrade != nil {
				f.onTrade(*order)
			}
			if f.onSave != nil {
				buf = append(buf, *order)
				if len(buf) >= tradeFlushSize {
					flush()
				}
			}

		case <-flushTicker.C:
			flush()
		}
	}
}

// parseOrder парсит WS-сообщение KuCoin /market/match.
// Символ извлекается из поля data.symbol (KuCoin-формат → внутренний).
func (f *TradeFetcher) parseOrder(raw []byte) *exchange_orders.ExchangeOrder {
	var msg WsMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil
	}
	if msg.Type != "message" {
		return nil
	}
	if msg.Subject != "trade.l3match" {
		// Логируем неожиданный subject — помогает диагностировать изменения в API KuCoin.
		f.log.Warn("kucoin trade: unexpected subject, message skipped",
			zap.String("subject", msg.Subject),
			zap.String("topic", msg.Topic),
		)
		return nil
	}

	var data struct {
		Symbol  string `json:"symbol"`  // KuCoin-формат: "BTC-USDT"
		Side    string `json:"side"`    // "buy" | "sell" — taker side
		Price   string `json:"price"`
		Size    string `json:"size"`
		TradeID string `json:"tradeId"`
		Time    string `json:"time"` // наносекунды
	}
	if err := json.Unmarshal(msg.Data, &data); err != nil {
		f.log.Warn("kucoin trade: unmarshal data failed", zap.Error(err))
		return nil
	}

	tradeTime := time.Now()
	if ns, err := strconv.ParseInt(data.Time, 10, 64); err == nil {
		tradeTime = time.Unix(0, ns).UTC()
	}

	return &exchange_orders.ExchangeOrder{
		Exchange:  "kucoin",
		Symbol:    fromKucoinSymbol(data.Symbol),
		Price:     data.Price,
		Quantity:  data.Size,
		Side:      data.Side,
		TradeTime: tradeTime,
	}
}
