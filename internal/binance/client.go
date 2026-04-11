package binance

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/shared/stats"
	"github.com/osman/bot-traider/internal/ticker"
)

// Client управляет соединениями к Binance WebSocket.
type Client struct {
	config       *Config
	logger       *zap.Logger
	stats        *stats.Stats
	svc          *ticker.TickerService
	fetchSymbols func(ctx context.Context) ([]string, error)

	mu          sync.Mutex
	cancelConns context.CancelFunc
	connsWg     *sync.WaitGroup
	connCount   int
}

// NewClient создаёт новый Client.
func NewClient(cfg *Config, log *zap.Logger, st *stats.Stats, w *ticker.TickerService) *Client {
	return &Client{
		config:  cfg,
		logger:  log,
		stats:   st,
		svc:     w,
		connsWg: &sync.WaitGroup{},
	}
}

// WithSymbolFetcher задаёт функцию получения списка символов для подписки.
func (c *Client) WithSymbolFetcher(fn func(ctx context.Context) ([]string, error)) {
	c.fetchSymbols = fn
}

// Run запускает SymbolWatcher и управляет соединениями до ctx.Done().
func (c *Client) Run(ctx context.Context) error {
	interval := time.Duration(c.config.SymbolRefreshMin) * time.Minute
	watcher := NewSymbolWatcher(interval, c.fetchSymbols, c.logger, c.onSymbolsChanged)
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
		conn := NewConnection(i, chunk, c.config.WSURL, c.logger, c, c.config.MaxWait, c.stats)
		wg.Add(1)
		go func(conn *Connection) {
			defer wg.Done()
			conn.Run(ctx)
		}(conn)
	}

	return cancel, wg
}

// NotifySymbolsChanged немедленно перезапускает WS-соединения для нового списка символов.
// Вызывается при обновлении топ-листа, минуя таймер SymbolWatcher.
func (c *Client) NotifySymbolsChanged(all []string) {
	c.onSymbolsChanged(nil, nil, all)
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

// OnTicker реализует EventHandler.
func (c *Client) OnTicker(t ticker.Ticker) {
	if c.svc != nil {
		c.svc.Send(t)
	}
}
