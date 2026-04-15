package exchange

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// EmulatedClient реализует RestClient без реальных запросов к бирже.
// Используется для тестирования стратегий: ордера логируются, баланс симулируется.
type EmulatedClient struct {
	mu          sync.Mutex
	balanceUSDT float64
	orderSeq    int64
	log         *zap.Logger
}

// NewEmulatedClient создаёт эмулированный клиент с начальным балансом balanceUSDT.
func NewEmulatedClient(balanceUSDT float64, log *zap.Logger) *EmulatedClient {
	log.Info("emulated exchange client created",
		zap.Float64("initial_balance_usdt", balanceUSDT),
	)
	return &EmulatedClient{
		balanceUSDT: balanceUSDT,
		log:         log,
	}
}

// PlaceMarketOrder эмулирует исполнение маркет-ордера.
// Возвращает Price=0 и Qty=0, чтобы trade.Service использовал значения стратегии.
func (c *EmulatedClient) PlaceMarketOrder(ctx context.Context, symbol, side string, qty float64) (OrderResult, error) {
	orderID := c.nextOrderID()

	c.log.Info("[EMULATION] market order placed",
		zap.String("order_id", orderID),
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.Float64("qty", qty),
	)

	return OrderResult{
		OrderID: orderID,
		Price:   0, // trade.Service возьмёт цену из стратегии
		Qty:     0, // trade.Service возьмёт qty из запроса
	}, nil
}

// PlaceLimitOrder эмулирует лимитный ордер.
func (c *EmulatedClient) PlaceLimitOrder(ctx context.Context, symbol, side string, qty, price float64, postOnly bool) (OrderResult, error) {
	orderID := c.nextOrderID()

	c.log.Info("[EMULATION] limit order placed",
		zap.String("order_id", orderID),
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.Float64("qty", qty),
		zap.Float64("price", price),
		zap.Bool("post_only", postOnly),
	)

	return OrderResult{
		OrderID: orderID,
		Price:   price,
		Qty:     qty,
	}, nil
}

// CancelOrder эмулирует отмену ордера (no-op).
func (c *EmulatedClient) CancelOrder(ctx context.Context, symbol, orderID string) error {
	c.log.Info("[EMULATION] order cancelled",
		zap.String("order_id", orderID),
		zap.String("symbol", symbol),
	)
	return nil
}

// GetOpenOrders возвращает пустой список (эмуляция).
func (c *EmulatedClient) GetOpenOrders(ctx context.Context, symbol string) ([]OpenOrder, error) {
	return nil, nil
}

// GetFreeUSDT возвращает симулированный баланс.
func (c *EmulatedClient) GetFreeUSDT(ctx context.Context) (float64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.balanceUSDT, nil
}

func (c *EmulatedClient) nextOrderID() string {
	seq := atomic.AddInt64(&c.orderSeq, 1)
	return fmt.Sprintf("EMU-%d-%d", time.Now().UnixMilli(), seq)
}
