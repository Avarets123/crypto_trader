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

// TradeFetcher подписывается на WS-стрим /market/match KuCoin для одного символа.
// Аналог exchange_orders.Fetcher, но для KuCoin aggTrade-сделок.
// Используется TradeService для получения CVD/whale сигналов в microscalping.
type TradeFetcher struct {
	rest    *RestClient
	onTrade func(exchange_orders.ExchangeOrder)
	log     *zap.Logger
}

// NewTradeFetcher создаёт TradeFetcher.
func NewTradeFetcher(rest *RestClient, log *zap.Logger) *TradeFetcher {
	return &TradeFetcher{rest: rest, log: log}
}

// WithOnTrade устанавливает хук, вызываемый на каждой входящей сделке.
func (f *TradeFetcher) WithOnTrade(fn func(exchange_orders.ExchangeOrder)) {
	f.onTrade = fn
}

// Subscribe открывает WS-подписку /market/match:{symbol} с reconnect до ctx.Done().
// Совместим по сигнатуре с exchange_orders.Fetcher.Subscribe.
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
	for {
		if ctx.Err() != nil {
			return nil
		}
		conn.SetReadDeadline(time.Now().Add(60 * time.Second)) //nolint:errcheck
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		f.handleMessage(raw, symbol)
	}
}

func (f *TradeFetcher) handleMessage(raw []byte, symbol string) {
	var msg WsMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}

	switch msg.Type {
	case "message":
		// обрабатываем ниже
	case "pong", "welcome", "ack":
		return
	default:
		return
	}

	if msg.Subject != "trade.l3match" {
		return
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
		return
	}

	tradeTime := time.Now()
	if ns, err := strconv.ParseInt(data.Time, 10, 64); err == nil {
		tradeTime = time.Unix(0, ns).UTC()
	}

	if f.onTrade != nil {
		f.onTrade(exchange_orders.ExchangeOrder{
			Exchange:  "kucoin",
			Symbol:    symbol,
			Price:     data.Price,
			Quantity:  data.Size,
			Side:      data.Side,
			TradeTime: tradeTime,
		})
	}
}
