package exchange_orders

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

const (
	flushSize     = 100
	flushInterval = 500 * time.Millisecond
	maxWait       = 60 * time.Second
)

// Fetcher подписывается на WS-стрим @trade и батч-сохраняет сделки в БД.
type Fetcher struct {
	exchange string
	wsBase   string
	repo     *Repository
	log      *zap.Logger
}

// NewFetcher создаёт Fetcher.
// exchange — название биржи (напр. "binance").
// wsBase — базовый WS URL (напр. "wss://stream.binance.com:9443").
func NewFetcher(exchange, wsBase string, repo *Repository, log *zap.Logger) *Fetcher {
	return &Fetcher{
		exchange: exchange,
		wsBase:   wsBase,
		repo:     repo,
		log:      log,
	}
}

// Subscribe открывает WS-стрим <symbol>@trade и сохраняет каждую сделку в БД батчами.
// Reconnect с exponential backoff. Блокирует до ctx.Done().
func (f *Fetcher) Subscribe(ctx context.Context, symbol string) {
	wsURL := fmt.Sprintf("%s/ws/%s@trade", f.wsBase, strings.ToLower(symbol))
	wait := time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			f.log.Warn("exchange_orders ws: dial failed, reconnecting",
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
			continue
		}

		f.log.Info("exchange_orders ws: connected", zap.String("symbol", symbol))
		wait = time.Second

		f.readLoop(ctx, conn, symbol)
		conn.Close()

		if ctx.Err() != nil {
			return
		}

		f.log.Warn("exchange_orders ws: disconnected, reconnecting",
			zap.String("symbol", symbol),
			zap.Duration("wait", wait),
		)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = nextWait(wait, maxWait)
	}
}

// readLoop читает сообщения и флашит батч каждые 500мс или при 100 записях.
func (f *Fetcher) readLoop(ctx context.Context, conn *websocket.Conn, symbol string) {
	buf := make([]ExchangeOrder, 0, flushSize)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	flush := func() {
		if len(buf) == 0 {
			return
		}
		if err := f.repo.SaveBatch(ctx, buf); err != nil {
			f.log.Warn("exchange_orders: flush failed",
				zap.String("symbol", symbol),
				zap.Int("count", len(buf)),
				zap.Error(err),
			)
		}
		buf = buf[:0]
	}

	msgs := make(chan []byte, 256)

	// Читаем в отдельной горутине чтобы не блокировать ticker
	go func() {
		defer close(msgs)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			select {
			case msgs <- msg:
			default:
				f.log.Warn("exchange_orders ws: buffer full, dropping message", zap.String("symbol", symbol))
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			flush()
			return

		case msg, ok := <-msgs:
			if !ok {
				flush()
				return
			}
			order, err := f.parseMessage(msg, symbol)
			if err != nil {
				f.log.Warn("exchange_orders ws: parse failed", zap.String("symbol", symbol), zap.Error(err))
				continue
			}
			buf = append(buf, *order)
			if len(buf) >= flushSize {
				flush()
			}

		case <-ticker.C:
			flush()
		}
	}
}

func (f *Fetcher) parseMessage(msg []byte, symbol string) (*ExchangeOrder, error) {
	var raw struct {
		Symbol    string `json:"s"`
		TradeID   int64  `json:"t"`
		Price     string `json:"p"`
		Quantity  string `json:"q"`
		TradeTime int64  `json:"T"`
		IsMaker   bool   `json:"m"`
		Ignore    bool   `json:"M"` // всегда true, нужен чтобы не перезаписывал IsMaker через case-insensitive matching
	}
	if err := json.Unmarshal(msg, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	side := "buy"
	if raw.IsMaker {
		side = "sell"
	}

	return &ExchangeOrder{
		Exchange:  f.exchange,
		Symbol:    symbol,
		TradeID:   raw.TradeID,
		Price:     raw.Price,
		Quantity:  raw.Quantity,
		Side:      side,
		TradeTime: time.UnixMilli(raw.TradeTime).UTC(),
	}, nil
}

func nextWait(current, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}
	return next
}
