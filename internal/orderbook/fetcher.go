package orderbook

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/binance"
)

// DepthClient — интерфейс для получения снимка стакана через REST.
type DepthClient interface {
	GetDepth(ctx context.Context, symbol string, limit int) (*binance.DepthSnapshot, error)
}

// Fetcher загружает стакан через REST и подписывается на WS-обновления.
type Fetcher struct {
	rest    DepthClient
	wsBase  string
	repo    *Repository
	log     *zap.Logger
	maxWait time.Duration
}

// NewFetcher создаёт Fetcher.
// wsBase — базовый WS URL (напр. "wss://stream.binance.com:9443").
func NewFetcher(rest DepthClient, wsBase string, repo *Repository, log *zap.Logger) *Fetcher {
	return &Fetcher{
		rest:    rest,
		wsBase:  wsBase,
		repo:    repo,
		log:     log,
		maxWait: 60 * time.Second,
	}
}

// FetchSnapshot загружает снимок стакана через REST и сохраняет в Redis.
func (f *Fetcher) FetchSnapshot(ctx context.Context, symbol string, depth int) (*OrderBook, error) {
	snap, err := f.rest.GetDepth(ctx, symbol, depth)
	if err != nil {
		return nil, fmt.Errorf("fetch snapshot %s: %w", symbol, err)
	}

	ob := &OrderBook{
		Symbol:    symbol,
		Bids:      entriesToModel(snap.Bids),
		Asks:      entriesToModel(snap.Asks),
		UpdatedAt: time.Now(),
	}
	return ob, nil
}

// Subscribe открывает WS-стрим <symbol>@depth20 и при каждом сообщении обновляет Redis.
// Reconnect с exponential backoff. Блокирует до ctx.Done().
func (f *Fetcher) Subscribe(ctx context.Context, symbol string) {
	wsURL := fmt.Sprintf("%s/ws/%s@depth20@300ms", f.wsBase, strings.ToLower(symbol))
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
			f.log.Warn("orderbook ws: dial failed, reconnecting",
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
			continue
		}

		f.log.Info("orderbook ws: connected", zap.String("symbol", symbol))
		wait = time.Second

		f.readLoop(ctx, conn, symbol)
		conn.Close()

		if ctx.Err() != nil {
			return
		}

		f.log.Warn("orderbook ws: disconnected, reconnecting",
			zap.String("symbol", symbol),
			zap.Duration("wait", wait),
		)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = nextWait(wait, f.maxWait)
	}
}

// readLoop читает сообщения стрима depth20 и сохраняет обновлённый стакан в Redis.
func (f *Fetcher) readLoop(ctx context.Context, conn *websocket.Conn, symbol string) {
	for {
		if ctx.Err() != nil {
			return
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			f.log.Debug("orderbook ws: read error", zap.String("symbol", symbol), zap.Error(err))
			return
		}

		var raw struct {
			Bids [][]json.RawMessage `json:"bids"`
			Asks [][]json.RawMessage `json:"asks"`
		}
		if err := json.Unmarshal(msg, &raw); err != nil {
			f.log.Warn("orderbook ws: unmarshal failed", zap.String("symbol", symbol), zap.Error(err))
			continue
		}

		ob := OrderBook{
			Symbol:    symbol,
			Bids:      parsePairs(raw.Bids),
			Asks:      parsePairs(raw.Asks),
			UpdatedAt: time.Now(),
		}

		if err := f.repo.Save(ctx, ob); err != nil {
			f.log.Warn("orderbook ws: save failed", zap.String("symbol", symbol), zap.Error(err))
			continue
		}

	}
}

func entriesToModel(pairs [][2]string) []Entry {
	entries := make([]Entry, len(pairs))
	for i, p := range pairs {
		entries[i] = Entry{Price: p[0], Qty: p[1]}
	}
	return entries
}

func parsePairs(rows [][]json.RawMessage) []Entry {
	entries := make([]Entry, 0, len(rows))
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		var price, qty string
		json.Unmarshal(row[0], &price) //nolint:errcheck
		json.Unmarshal(row[1], &qty)   //nolint:errcheck
		entries = append(entries, Entry{Price: price, Qty: qty})
	}
	return entries
}

func nextWait(current, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}
	return next
}
