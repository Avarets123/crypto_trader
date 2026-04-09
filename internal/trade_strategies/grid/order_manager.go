package grid

import (
	"context"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/shared/exchange"
)

// PlaceAllBuyOrders размещает лимитные buy-ордера на всех уровнях ниже текущей цены.
func PlaceAllBuyOrders(ctx context.Context, state *GridState, client exchange.RestClient, log *zap.Logger) {
	for _, level := range state.Levels {
		if level.Price >= state.CurrentPrice {
			continue // размещаем только ниже текущей цены
		}
		if level.OrderID != "" {
			continue // уже есть ордер
		}

		price := RoundPrice(level.Price)
		result, err := client.PlaceLimitOrder(ctx, state.Symbol, "Buy", state.QtyPerLevel, price, true)
		if err != nil {
			log.Error("grid: failed to place buy order",
				zap.String("symbol", state.Symbol),
				zap.Int("level", level.Index),
				zap.Float64("price", price),
				zap.Error(err),
			)
			continue
		}

		level.OrderID = result.OrderID
		level.Side = "buy"
		level.Filled = false
		log.Debug("grid: buy order placed",
			zap.String("symbol", state.Symbol),
			zap.Int("level", level.Index),
			zap.Float64("price", price),
			zap.String("order_id", result.OrderID),
		)
	}
}

// PlaceSellOrder ставит sell-ордер на уровне X+1 после исполнения buy на уровне X.
func PlaceSellOrder(ctx context.Context, state *GridState, level *GridLevel, client exchange.RestClient, log *zap.Logger) {
	if level.Index+1 >= len(state.Levels) {
		log.Debug("grid: no upper level for sell order",
			zap.String("symbol", state.Symbol),
			zap.Int("level", level.Index),
		)
		return
	}

	sellLevel := state.Levels[level.Index+1]
	sellPrice := RoundPrice(sellLevel.Price)

	result, err := client.PlaceLimitOrder(ctx, state.Symbol, "Sell", state.QtyPerLevel, sellPrice, true)
	if err != nil {
		log.Error("grid: failed to place sell order",
			zap.String("symbol", state.Symbol),
			zap.Int("buy_level", level.Index),
			zap.Int("sell_level", level.Index+1),
			zap.Float64("price", sellPrice),
			zap.Error(err),
		)
		return
	}

	sellLevel.OrderID = result.OrderID
	sellLevel.Side = "sell"
	sellLevel.Filled = false
	log.Info("grid: sell order placed",
		zap.String("symbol", state.Symbol),
		zap.Int("level", level.Index+1),
		zap.Float64("price", sellPrice),
		zap.String("order_id", result.OrderID),
	)
}

// PlaceBuyOrder ставит buy-ордер на уровне Y-1 после исполнения sell на уровне Y.
func PlaceBuyOrder(ctx context.Context, state *GridState, level *GridLevel, client exchange.RestClient, log *zap.Logger) {
	if level.Index-1 < 0 {
		log.Debug("grid: no lower level for buy order",
			zap.String("symbol", state.Symbol),
			zap.Int("level", level.Index),
		)
		return
	}

	buyLevel := state.Levels[level.Index-1]
	buyPrice := RoundPrice(buyLevel.Price)

	result, err := client.PlaceLimitOrder(ctx, state.Symbol, "Buy", state.QtyPerLevel, buyPrice, true)
	if err != nil {
		log.Error("grid: failed to place buy order",
			zap.String("symbol", state.Symbol),
			zap.Int("sell_level", level.Index),
			zap.Int("buy_level", level.Index-1),
			zap.Float64("price", buyPrice),
			zap.Error(err),
		)
		return
	}

	buyLevel.OrderID = result.OrderID
	buyLevel.Side = "buy"
	buyLevel.Filled = false
	log.Info("grid: buy order placed",
		zap.String("symbol", state.Symbol),
		zap.Int("level", level.Index-1),
		zap.Float64("price", buyPrice),
		zap.String("order_id", result.OrderID),
	)
}

// CancelAllOrders отменяет все активные ордера сетки.
func CancelAllOrders(ctx context.Context, state *GridState, client exchange.RestClient, log *zap.Logger) {
	for _, level := range state.Levels {
		if level.OrderID == "" {
			continue
		}
		if err := client.CancelOrder(ctx, state.Symbol, level.OrderID); err != nil {
			log.Error("grid: failed to cancel order",
				zap.String("symbol", state.Symbol),
				zap.Int("level", level.Index),
				zap.String("order_id", level.OrderID),
				zap.Error(err),
			)
			// Не паникуем — продолжаем отмену остальных
		} else {
			log.Debug("grid: order cancelled",
				zap.String("symbol", state.Symbol),
				zap.Int("level", level.Index),
				zap.String("order_id", level.OrderID),
			)
		}
		level.OrderID = ""
	}
}

// SyncOrders синхронизирует локальное состояние с биржей.
// Возвращает список уровней, ордера которых были исполнены (исчезли из открытых).
func SyncOrders(ctx context.Context, state *GridState, client exchange.RestClient, log *zap.Logger) []*GridLevel {
	openOrders, err := client.GetOpenOrders(ctx, state.Symbol)
	if err != nil {
		log.Error("grid: SyncOrders failed",
			zap.String("symbol", state.Symbol),
			zap.Error(err),
		)
		return nil
	}

	// Собираем множество активных order ID с биржи
	activeIDs := make(map[string]struct{}, len(openOrders))
	for _, o := range openOrders {
		activeIDs[o.OrderID] = struct{}{}
	}

	log.Debug("grid: SyncOrders",
		zap.String("symbol", state.Symbol),
		zap.Int("open_orders_on_exchange", len(openOrders)),
	)

	var filled []*GridLevel
	for _, level := range state.Levels {
		if level.OrderID == "" || level.Filled {
			continue
		}
		if _, exists := activeIDs[level.OrderID]; !exists {
			// Ордер исчез с биржи — считаем исполненным
			filled = append(filled, level)
		}
	}
	return filled
}
