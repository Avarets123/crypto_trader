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
	"github.com/osman/bot-traider/internal/shared/utils"
)

// DepthClient — интерфейс для получения снимка стакана через REST.
type DepthClient interface {
	GetDepth(ctx context.Context, symbol string, limit int) (*binance.DepthSnapshot, error)
}

// Fetcher загружает стакан через REST и подписывается на WS diff-стрим.
type Fetcher struct {
	rest    DepthClient
	wsBase  string
	log     *zap.Logger
	maxWait time.Duration
}

// NewFetcher создаёт Fetcher.
// wsBase — базовый WS URL (напр. "wss://stream.binance.com:9443").
func NewFetcher(rest DepthClient, wsBase string, log *zap.Logger) *Fetcher {
	return &Fetcher{
		rest:    rest,
		wsBase:  wsBase,
		log:     log,
		maxWait: 60 * time.Second,
	}
}

// diffMsg — инкрементальное обновление стакана из WS @depth стрима.
type diffMsg struct {
	FirstUpdateID int64               `json:"U"`
	FinalUpdateID int64               `json:"u"`
	Bids          [][]json.RawMessage `json:"b"`
	Asks          [][]json.RawMessage `json:"a"`
}

// SubscribeDiff запускает полный diff-стрим для символа:
// 1. Подключается к <symbol>@depth@100ms
// 2. Буферизует события
// 3. Загружает REST-снимок (5000 уровней) → инициализирует LocalBook
// 4. Применяет буферизованные события для синхронизации
// 5. Применяет последующие события в реальном времени
// Reconnect с exponential backoff. Блокирует до ctx.Done().
func (f *Fetcher) SubscribeDiff(ctx context.Context, symbol string, store *Store) {
	defer utils.TimeTracker(f.log, "SubscribeDiff")()

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

		f.log.Warn("orderbook diff: reconnecting",
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

// runDiffStream выполняет один цикл подключения diff-стрима.
func (f *Fetcher) runDiffStream(ctx context.Context, symbol string, store *Store) error {
	wsURL := fmt.Sprintf("%s/ws/%s@depth@100ms", f.wsBase, strings.ToLower(symbol))

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// Буфер на 1000 событий — достаточно для времени REST-запроса
	buf := make(chan diffMsg, 1000)
	readErr := make(chan error, 1)

	// Горутина чтения WS-сообщений
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			var d diffMsg
			if json.Unmarshal(msg, &d) == nil && d.FinalUpdateID > 0 {
				select {
				case buf <- d:
				default:
					readErr <- fmt.Errorf("event buffer overflow")
					return
				}
			}
		}
	}()

	// Загружаем REST-снимок (500 уровней, вес=25 у Binance)
	snap, err := f.rest.GetDepth(ctx, symbol, snapshotDepth)
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}

	// Инициализируем локальный стакан
	book := store.GetOrCreate(symbol)
	book.Init(snap.Bids, snap.Asks, snap.LastUpdateID, f.log)

	f.log.Info("orderbook: initialized from snapshot",
		zap.String("symbol", symbol),
		zap.Int64("lastUpdateID", snap.LastUpdateID),
		zap.Int("bids", len(snap.Bids)),
		zap.Int("asks", len(snap.Asks)),
	)

	// Применяем буферизованные события (синхронизация)
draining:
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-readErr:
			return err
		case d := <-buf:
			if d.FinalUpdateID <= snap.LastUpdateID {
				continue // устаревшее событие
			}
			// Обнаружен gap: WS опередил снимок.
			// Принимаем gap без REST-запроса — стакан восстановится из дельт за несколько секунд.
			if d.FirstUpdateID > snap.LastUpdateID+1 {
				f.log.Debug("orderbook diff: sync gap, accepting and continuing",
					zap.String("symbol", symbol),
					zap.Int64("U", d.FirstUpdateID),
					zap.Int64("lastUpdateID", snap.LastUpdateID),
				)
				snap.LastUpdateID = d.FirstUpdateID - 1
				book.Init(nil, nil, snap.LastUpdateID, f.log)
			}
			book.Apply(d.FinalUpdateID, d.Bids, d.Asks)
		default:
			break draining
		}
	}

	f.log.Info("orderbook: synced, streaming diffs", zap.String("symbol", symbol))

	// Основной цикл — применяем события в реальном времени
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-readErr:
			return err
		case d := <-buf:
			book.Apply(d.FinalUpdateID, d.Bids, d.Asks)
		}
	}
}

func nextWait(current, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}
	return next
}
