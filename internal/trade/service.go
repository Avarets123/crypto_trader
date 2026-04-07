package trade

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/shared/exchange"
)

// service управляет позициями.
// Открытые позиции хранятся в памяти.
// В БД пишется только при закрытии (единственный INSERT с полными данными).
type Service struct {
	mu           sync.RWMutex
	devMode      bool
	mode         string
	idCounter    int64
	trades       map[int64]*Trade
	clients      map[string]exchange.RestClient
	repo         *TradeRepository
	log          *zap.Logger
	onTradeOpen  []func(*Trade)
	onTradeClose []func(*Trade)
}

// WithOnTradeOpen регистрирует хук, вызываемый при открытии сделки.
func (m *Service) WithOnTradeOpen(fn func(*Trade)) {
	m.onTradeOpen = append(m.onTradeOpen, fn)
}

// WithOnTradeClose регистрирует хук, вызываемый при закрытии сделки.
func (m *Service) WithOnTradeClose(fn func(*Trade)) {
	m.onTradeClose = append(m.onTradeClose, fn)
}

// New создаёт service.
func NewService(devMode bool, repo *TradeRepository, clients map[string]exchange.RestClient, log *zap.Logger) *Service {
	mode := "prod"
	if devMode {
		mode = "test"
	}
	log.Info("order manager created",
		zap.String("mode", mode),
		zap.Int("exchanges", len(clients)),
	)
	return &Service{
		devMode:   devMode,
		mode:      mode,
		trades: make(map[int64]*Trade),
		clients:   clients,
		repo:      repo,
		log:       log,
	}
}

// OpenTrade сохраняет позицию в памяти и возвращает её in-memory id.
// В БД ничего не пишется.
func (m *Service) OpenTrade(ctx context.Context, newTrade Trade) (int64, error) {
	m.log.Warn("new trade: ",
		zap.String("strategy", newTrade.Strategy),
		zap.String("symbol", newTrade.Symbol),
		zap.String("trade_exchange", newTrade.TradeExchange),
		zap.Float64("entry_price", newTrade.EntryPrice),
		zap.Float64("qty", newTrade.Qty),
	)

	client, ok := m.clients[newTrade.TradeExchange]
	if !ok {
		return 0, fmt.Errorf("trade service: no rest client for exchange %q", newTrade.TradeExchange)
	}

	result, err := client.PlaceMarketOrder(ctx, newTrade.Symbol, "buy", newTrade.Qty)
	if err != nil {
		m.log.Error("trade service: error in create order",
			zap.String("symbol", newTrade.Symbol),
			zap.String("exchange", newTrade.TradeExchange),
			zap.String("mode", m.mode),
			zap.Error(err),
		)
		return 0, fmt.Errorf("new order: %w", err)
	}
	entryOrderID := result.OrderID
	if result.Price > 0 {
		newTrade.EntryPrice = result.Price
	}
	m.log.Info("exchange: order created",
		zap.String("order_id", entryOrderID),
		zap.String("exchange", newTrade.TradeExchange),
		zap.String("mode", m.mode),
		zap.Float64("filled_price", newTrade.EntryPrice),
	)

	id := atomic.AddInt64(&m.idCounter, 1)
	pos := &Trade{
		ID:             id,
		Strategy:       newTrade.Strategy,
		Mode:           m.mode,
		SignalExchange: newTrade.SignalExchange,
		TradeExchange:  newTrade.TradeExchange,
		Symbol:         newTrade.Symbol,
		Qty:            newTrade.Qty,
		EntryPrice:     newTrade.EntryPrice,
		TargetPrice:    newTrade.TargetPrice,
		StopLossPrice:  newTrade.StopLossPrice,
		EntryOrderID:   entryOrderID,
		SpreadID:       newTrade.SpreadID,
		OpenedAt:       time.Now(),
	}

	m.mu.Lock()
	m.trades[id] = pos
	m.mu.Unlock()

	m.log.Info("trade created: ",
		zap.Int64("id", id),
		zap.String("symbol", newTrade.Symbol),
		zap.String("mode", m.mode),
		zap.Float64("entry_price", newTrade.EntryPrice),
		zap.Float64("qty", newTrade.Qty),
		zap.Int("open_positions", m.CountOpenPositions()),
	)

	for _, fn := range m.onTradeOpen {
		fn := fn
		go fn(pos)
	}

	return id, nil
}

// CloseTrade закрывает позицию: исполняет продажу и сохраняет полную запись в БД.
func (m *Service) CloseTrade(ctx context.Context, id int64, exitPrice float64, exitReason string) error {
	m.mu.RLock()
	trade, ok := m.trades[id]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("trade service: trade %d not found in memory", id)
	}

	m.log.Info(trade.TradeExchange +": closing position",
		zap.Int64("id", id),
		zap.String("symbol", trade.Symbol),
		zap.String("reason", exitReason),
		zap.Float64("exit_price", exitPrice),
	)

	client, ok := m.clients[trade.TradeExchange]
	if !ok {
		return fmt.Errorf(trade.TradeExchange +" : no rest client for exchange %q", trade.TradeExchange)
	}
	result, err := client.PlaceMarketOrder(ctx, trade.Symbol, "sell", trade.Qty)
	if err != nil {
		m.log.Error("exchange: close order failed",
			zap.Int64("id", id),
			zap.String("symbol", trade.Symbol),
			zap.String("exchange", trade.TradeExchange),
			zap.String("mode", m.mode),
			zap.Error(err),
		)
		return fmt.Errorf("order close: %w", err)
	}
	exitOrderID := result.OrderID
	if result.Price > 0 {
		exitPrice = result.Price
	}
	m.log.Info(trade.TradeExchange +" : close order placed",
		zap.String("order_id", exitOrderID),
		zap.String("exchange", trade.TradeExchange),
		zap.String("mode", m.mode),
		zap.Float64("exit_price", exitPrice),
	)

	// комиссия: 0.1% за вход + 0.1% за выход
	const commissionRate = 0.001
	commission := (trade.EntryPrice + exitPrice) * trade.Qty * commissionRate
	pnl := (exitPrice-trade.EntryPrice)*trade.Qty - commission
	closedAt := time.Now()

	m.log.Info(trade.TradeExchange+": pnl calculated",
		zap.Int64("id", id),
		zap.Float64("entry_price", trade.EntryPrice),
		zap.Float64("exit_price", exitPrice),
		zap.Float64("qty", trade.Qty),
		zap.Float64("commission_usdt", commission),
		zap.Float64("pnl_usdt", pnl),
	)

	trade.ExitPrice = &exitPrice
	trade.ExitReason = exitReason
	trade.CommissionUSDT = &commission
	trade.PnlUSDT = &pnl
	trade.ExitOrderID = exitOrderID
	trade.ClosedAt = &closedAt

	if err := m.repo.SaveClosedTrade(ctx, trade); err != nil {
		m.log.Error("order manager: save closed trade failed",
			zap.Int64("id", id),
			zap.Error(err),
		)
		// позицию всё равно удаляем из памяти, чтобы не заблокировать следующие сделки
	}

	m.mu.Lock()
	delete(m.trades, id)
	m.mu.Unlock()

	m.log.Info("trade ended",
		zap.Int64("id", id),
		zap.String("symbol", trade.Symbol),
		zap.String("reason", exitReason),
		zap.Float64("exit_price", exitPrice),
		zap.Float64("pnl_usdt", pnl),
		zap.Int("open_positions", m.CountOpenPositions()),
	)

	for _, fn := range m.onTradeClose {
		fn := fn
		go fn(trade)
	}

	return nil
}

// HasOpenPosition возвращает true если по символу есть открытая позиция в памяти.
func (m *Service) HasOpenPosition(symbol string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, pos := range m.trades {
		if pos.Symbol == symbol {
			return true
		}
	}
	return false
}

// CountOpenPositions возвращает количество открытых позиций в памяти.
func (m *Service) CountOpenPositions() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.trades)
}
