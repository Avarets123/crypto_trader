package binance

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/shared/ticker"
)

// Client управляет соединениями к Binance WebSocket.
type Client struct {
	config *Config
	logger *zap.Logger

	mu          sync.Mutex
	cancelConns context.CancelFunc
	connsWg     *sync.WaitGroup
	connCount   int
}

// NewClient создаёт новый Client.
func NewClient(cfg *Config, log *zap.Logger) *Client {
	return &Client{
		config:  cfg,
		logger:  log,
		connsWg: &sync.WaitGroup{},
	}
}

// Run запускает SymbolWatcher и управляет соединениями до ctx.Done().
func (c *Client) Run(ctx context.Context) error {
	interval := time.Duration(c.config.SymbolRefreshMin) * time.Minute
	watcher := NewSymbolWatcher(interval, c.logger, c.onSymbolsChanged)
	return watcher.Run(ctx)
}

// onSymbolsChanged вызывается при каждом изменении списка символов.
// Останавливает старые соединения и запускает новые.
func (c *Client) onSymbolsChanged(added, removed, all []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	oldCancel := c.cancelConns
	oldWg := c.connsWg
	oldCount := c.connCount

	if oldCancel != nil {
		oldCancel()
		oldWg.Wait()
	}

	chunks := ChunkSymbols(all, MaxSymbolsPerConn)
	newCount := len(chunks)

	c.logger.Info("restarting connections",
		zap.Int("old_count", oldCount),
		zap.Int("new_count", newCount),
		zap.Int("total_symbols", len(all)),
	)

	cancel, wg := c.startConnections(all)
	c.cancelConns = cancel
	c.connsWg = wg
	c.connCount = newCount
}

// startConnections создаёт и запускает Connection для каждой группы символов.
func (c *Client) startConnections(symbols []string) (context.CancelFunc, *sync.WaitGroup) {
	ctx, cancel := context.WithCancel(context.Background())
	wg := &sync.WaitGroup{}

	chunks := ChunkSymbols(symbols, MaxSymbolsPerConn)

	c.logger.Info("starting connections",
		zap.Int("total_symbols", len(symbols)),
		zap.Int("connections", len(chunks)),
	)

	for i, chunk := range chunks {
		conn := NewConnection(i, chunk, c.logger, c, c.config.MaxWait)
		wg.Add(1)
		go func(conn *Connection) {
			defer wg.Done()
			conn.Run(ctx)
		}(conn)
	}

	return cancel, wg
}

// OnTrade реализует EventHandler — логирует совершённую сделку.
func (c *Client) OnTrade(event TradeEvent) {
	side := "BUY"
	if event.IsMaker {
		side = "SELL"
	}
	c.logger.Info("trade",
		zap.String("symbol", event.Symbol),
		zap.String("price", event.Price),
		zap.String("qty", event.Quantity),
		zap.String("side", side),
		zap.Time("time", time.UnixMilli(event.TradeTime)),
	)
}

// OnTicker реализует EventHandler — логирует обновление тикера.
func (c *Client) OnTicker(t ticker.Ticker) {
	c.logger.Info("ticker",
		zap.String("symbol", t.Symbol),
		zap.String("quote", t.Quote),
		zap.String("price", t.Price),
		zap.String("open_24h", t.Open24h),
		zap.String("high_24h", t.High24h),
		zap.String("low_24h", t.Low24h),
		zap.String("vol_24h", t.Volume24h),
		zap.String("change_pct", t.ChangePct),
	)
}
