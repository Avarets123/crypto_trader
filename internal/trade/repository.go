package trade

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// TradeRepository сохраняет закрытые сделки в таблицу positions.
// Открытые позиции хранятся только в памяти (OrderManager) и пишутся в БД при закрытии.
type TradeRepository struct {
	pool *pgxpool.Pool
	log  *zap.Logger
}

func NewRepo(pool *pgxpool.Pool, log *zap.Logger) *TradeRepository {
	return &TradeRepository{pool: pool, log: log}
}

// SaveClosedTrade выполняет единственный INSERT с полными данными закрытой сделки.
func (r *TradeRepository) SaveClosedTrade(ctx context.Context, t *Trade) error {
	r.log.Debug("trade: saving closed trade",
		zap.String("strategy", t.Strategy),
		zap.String("symbol", t.Symbol),
		zap.String("mode", t.Mode),
		zap.Float64("entry_price", t.EntryPrice),
		zap.Float64("qty", t.Qty),
		zap.String("exit_reason", t.ExitReason),
	)

	_, err := r.pool.Exec(ctx,
		`INSERT INTO trades
		 (strategy, mode, signal_exchange, trade_exchange, symbol, qty,
		  entry_price, target_price, stop_loss_price,
		  exit_price, exit_reason, pnl_usdt,
		  entry_order_id, exit_order_id, signal_data,
		  opened_at, closed_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		t.Strategy, t.Mode, t.SignalExchange, t.TradeExchange, t.Symbol, t.Qty,
		t.EntryPrice, t.TargetPrice, t.StopLossPrice,
		t.ExitPrice, t.ExitReason, t.PnlUSDT,
		nilIfEmpty(t.EntryOrderID), nilIfEmpty(t.ExitOrderID), t.SignalData,
		t.OpenedAt, t.ClosedAt,
	)
	if err != nil {
		r.log.Error("trade: save closed trade failed",
			zap.String("strategy", t.Strategy),
			zap.String("symbol", t.Symbol),
			zap.Error(err),
		)
		return err
	}

	r.log.Info("closed trade saved",
		zap.String("strategy", t.Strategy),
		zap.String("symbol", t.Symbol),
		zap.Float64("entry_price", t.EntryPrice),
		zap.Stringp("exit_reason", &t.ExitReason),
		zap.String("mode", t.Mode),
	)
	return nil
}

func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
