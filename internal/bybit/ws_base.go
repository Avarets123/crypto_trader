package bybit

import (
	"context"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// wsConn — базовая WS-структура с exponential backoff reconnect.
// Используется и Connection (market data), и WsTradeClient (торговля).
// В отличие от Binance, ping у Bybit — JSON-сообщение, обрабатывается в onConnect.
type wsConn struct {
	url     string
	log     *zap.Logger
	maxWait time.Duration
}

// Run запускает цикл подключения с exponential backoff до ctx.Done().
// При каждом успешном подключении вызывает onConnect(ctx, conn).
// onConnect блокируется до разрыва соединения или ctx.Done().
func (w *wsConn) Run(ctx context.Context, onConnect func(ctx context.Context, conn *websocket.Conn) error) {
	wait := time.Second
	attempt := 0

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, err := w.dial(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			attempt++
			w.log.Warn("ws: dial failed, reconnecting",
				zap.String("url", w.url),
				zap.Int("attempt", attempt),
				zap.Duration("wait", wait),
				zap.Error(err),
			)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = nextWait(wait, w.maxWait)
			continue
		}

		w.log.Info("ws: connected", zap.String("url", w.url))
		attempt = 0
		wait = time.Second

		err = onConnect(ctx, conn)
		conn.Close()

		if ctx.Err() != nil {
			return
		}

		attempt++
		w.log.Warn("ws: disconnected, reconnecting",
			zap.String("url", w.url),
			zap.Int("attempt", attempt),
			zap.Duration("wait", wait),
			zap.Error(err),
		)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = nextWait(wait, w.maxWait)
	}
}

// dial устанавливает WS-соединение без ping handler (Bybit использует JSON-пинги).
func (w *wsConn) dial(ctx context.Context) (*websocket.Conn, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, w.url, nil)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", w.url, err)
	}
	return conn, nil
}

func nextWait(current, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}
	return next
}
