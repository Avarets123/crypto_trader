package kucoin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	exchange_orders "github.com/osman/bot-traider/internal/exchange_orders"
	"go.uber.org/zap"
)

const (
	tradeFlushSize     = 100
	tradeFlushInterval = 500 * time.Millisecond
)

// TradeFetcher подписывается на WS-стрим /market/match KuCoin для одного символа.
// Аналог exchange_orders.Fetcher, но для KuCoin aggTrade-сделок.
// Поддерживает батч-сохранение в БД через WithOnSave и realtime-хук через WithOnTrade.
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
// Вызывается каждые 500мс или при накоплении 100 сделок.
func (f *TradeFetcher) WithOnSave(fn func(ctx context.Context, orders []exchange_orders.ExchangeOrder) error) {
	f.onSave = fn
}

// Subscribe открывает WS-подписку /market/match:{symbol} с reconnect до ctx.Done().
func (f *TradeFetcher) Subscribe(ctx context.Context, symbol string) {
	kcSymbol := toKucoinSymbol(symbol)
	maxWait := 60 * time.Second
	wait := time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := f.connect(ctx, symbol, kcSymbol); err != nil {
			if ctx.Err() != nil {
				return
			}
			f.log.Warn("kucoin trade: reconnecting",
				zap.String("symbol", symbol),
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

func (f *TradeFetcher) connect(ctx context.Context, symbol, kcSymbol string) error {
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

	// Подписываемся на /market/match:{symbol}
	subMsg := map[string]interface{}{
		"id":             newConnectID(),
		"type":           "subscribe",
		"topic":          "/market/match:" + kcSymbol,
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
		f.log.Warn("kucoin trade: unexpected ack type", zap.String("type", ack.Type), zap.String("symbol", symbol))
	}

	f.log.Info("kucoin trade: subscribed", zap.String("symbol", symbol))

	// Пинг-горутина
	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go func() {
		pingInterval := time.Duration(pingMs) * time.Millisecond
		if pingInterval <= 0 {
			pingInterval = 18 * time.Second
		}
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

	return f.readLoop(ctx, conn, symbol)
}

func (f *TradeFetcher) readLoop(ctx context.Context, conn *websocket.Conn, symbol string) error {
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
				zap.String("symbol", symbol),
				zap.Int("count", len(buf)),
				zap.Error(err),
			)
		}
		buf = buf[:0]
	}

	// Читаем в отдельной горутине чтобы не блокировать ticker
	msgs := make(chan []byte, 256)
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
				f.log.Warn("kucoin trade: msg buffer full, dropping", zap.String("symbol", symbol))
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
			order := f.parseOrder(raw, symbol)
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

// parseOrder парсит WS-сообщение KuCoin /market/match и возвращает ExchangeOrder.
// Возвращает nil если сообщение не является торговым событием.
func (f *TradeFetcher) parseOrder(raw []byte, symbol string) *exchange_orders.ExchangeOrder {
	var msg WsMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil
	}
	if msg.Type != "message" || msg.Subject != "trade.l3match" {
		return nil
	}

	var data struct {
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
		Symbol:    symbol,
		Price:     data.Price,
		Quantity:  data.Size,
		Side:      data.Side,
		TradeTime: tradeTime,
	}
}
