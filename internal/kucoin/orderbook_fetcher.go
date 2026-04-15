package kucoin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"github.com/osman/bot-traider/internal/orderbook"
	"go.uber.org/zap"
)

// level2Update — инкрементальное обновление стакана KuCoin из WS /market/level2.
type level2Update struct {
	SequenceStart int64  `json:"sequenceStart"`
	SequenceEnd   int64  `json:"sequenceEnd"`
	Symbol        string `json:"symbol"`
	Changes       struct {
		Bids [][3]string `json:"bids"` // [price, qty, sequence]
		Asks [][3]string `json:"asks"`
	} `json:"changes"`
}

// OrderBookFetcher загружает стакан KuCoin через REST-снимок и держит его актуальным
// через WS-стрим /market/level2:{symbol}. Пишет в общий orderbook.Store.
type OrderBookFetcher struct {
	rest    *RestClient
	log     *zap.Logger
	maxWait time.Duration
}

// NewOrderBookFetcher создаёт OrderBookFetcher.
func NewOrderBookFetcher(rest *RestClient, log *zap.Logger) *OrderBookFetcher {
	return &OrderBookFetcher{rest: rest, log: log, maxWait: 60 * time.Second}
}

// SubscribeDiff запускает полный цикл: REST-снимок → WS-дельты.
// Reconnect с exponential backoff. Блокирует до ctx.Done().
func (f *OrderBookFetcher) SubscribeDiff(ctx context.Context, symbol string, store *orderbook.Store) {
	wait := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := f.runDiffStream(ctx, symbol, store)
		if ctx.Err() != nil {
			return
		}

		f.log.Warn("kucoin orderbook: reconnecting",
			zap.String("symbol", symbol),
			zap.Duration("wait", wait),
			zap.Error(err),
		)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = nextWait(wait, f.maxWait)
	}
}

// runDiffStream выполняет один цикл: WS-подключение → REST-снимок → дельты.
func (f *OrderBookFetcher) runDiffStream(ctx context.Context, symbol string, store *orderbook.Store) error {
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

	// Подписываемся на /market/level2:{symbol}
	kcSymbol := toKucoinSymbol(symbol)
	subMsg := map[string]interface{}{
		"id":             newConnectID(),
		"type":           "subscribe",
		"topic":          "/market/level2:" + kcSymbol,
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
	json.Unmarshal(rawAck, &ack) //nolint:errcheck

	f.log.Info("kucoin orderbook: subscribed", zap.String("symbol", symbol))

	// Буфер на 2000 событий — достаточно для времени REST-запроса
	type wsResult struct {
		upd *level2Update
		err error
	}
	buf := make(chan wsResult, 2000)

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

	// Горутина чтения WS-сообщений в буфер
	go func() {
		for {
			conn.SetReadDeadline(time.Now().Add(60 * time.Second)) //nolint:errcheck
			_, rawMsg, err := conn.ReadMessage()
			if err != nil {
				buf <- wsResult{err: err}
				return
			}
			var msg WsMessage
			if err := json.Unmarshal(rawMsg, &msg); err != nil {
				continue
			}
			if msg.Type != "message" || msg.Subject != "trade.l2update" {
				continue
			}
			var upd level2Update
			if err := json.Unmarshal(msg.Data, &upd); err != nil {
				continue
			}
			select {
			case buf <- wsResult{upd: &upd}:
			default:
				buf <- wsResult{err: fmt.Errorf("event buffer overflow")}
				return
			}
		}
	}()

	// Загружаем REST-снимок (100 уровней)
	bids, asks, sequence, err := f.rest.GetOrderBookSnapshot(ctx, symbol)
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}

	book := store.GetOrCreate(symbol)
	book.Init(bids, asks, sequence, f.log)
	f.log.Info("kucoin orderbook: initialized from snapshot",
		zap.String("symbol", symbol),
		zap.Int64("sequence", sequence),
		zap.Int("bids", len(bids)),
		zap.Int("asks", len(asks)),
	)

	// Дренируем буфер: применяем события с sequence > snapshot
draining:
	for {
		select {
		case <-ctx.Done():
			return nil
		case r := <-buf:
			if r.err != nil {
				return r.err
			}
			if r.upd.SequenceEnd <= sequence {
				continue // устаревшее
			}
			book.ApplyPairs(r.upd.SequenceEnd, toEntryPairs(r.upd.Changes.Bids), toEntryPairs(r.upd.Changes.Asks))
		default:
			break draining
		}
	}

	f.log.Info("kucoin orderbook: synced, streaming diffs", zap.String("symbol", symbol))

	// Основной цикл — реальное время
	for {
		select {
		case <-ctx.Done():
			return nil
		case r := <-buf:
			if r.err != nil {
				return r.err
			}
			book.ApplyPairs(r.upd.SequenceEnd, toEntryPairs(r.upd.Changes.Bids), toEntryPairs(r.upd.Changes.Asks))
		}
	}
}

// toEntryPairs конвертирует KuCoin [price, qty, seq] в [price, qty] пары для LocalBook.
func toEntryPairs(rows [][3]string) [][2]string {
	pairs := make([][2]string, len(rows))
	for i, row := range rows {
		pairs[i] = [2]string{row[0], row[1]}
	}
	return pairs
}
