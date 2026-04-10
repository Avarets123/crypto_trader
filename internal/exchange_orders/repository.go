package exchange_orders

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// Repository сохраняет сделки в PostgreSQL.
type Repository struct {
	pool      *pgxpool.Pool
	log       *zap.Logger
	onSaved   func(n int)
}

// NewRepository создаёт Repository.
func NewRepository(pool *pgxpool.Pool, log *zap.Logger) *Repository {
	return &Repository{pool: pool, log: log}
}

// WithOnSaved устанавливает колбэк, вызываемый после успешного сохранения батча.
func (r *Repository) WithOnSaved(fn func(n int)) {
	r.onSaved = fn
}

// SaveBatch вставляет пачку сделок через unnest + ON CONFLICT DO NOTHING.
func (r *Repository) SaveBatch(ctx context.Context, orders []ExchangeOrder) error {
	if len(orders) == 0 {
		return nil
	}

	exchanges := make([]string, len(orders))
	symbols := make([]string, len(orders))
	tradeIDs := make([]int64, len(orders))
	prices := make([]string, len(orders))
	quantities := make([]string, len(orders))
	sides := make([]string, len(orders))
	tradeTimes := make([]interface{}, len(orders))

	for i, o := range orders {
		exchanges[i] = o.Exchange
		symbols[i] = o.Symbol
		tradeIDs[i] = o.TradeID
		prices[i] = o.Price
		quantities[i] = o.Quantity
		sides[i] = o.Side
		tradeTimes[i] = o.TradeTime
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO exchange_orders (exchange, symbol, trade_id, price, quantity, side, trade_time)
		SELECT * FROM unnest(
			$1::varchar[],
			$2::varchar[],
			$3::bigint[],
			$4::numeric[],
			$5::numeric[],
			$6::varchar[],
			$7::timestamptz[]
		)
		ON CONFLICT (exchange, symbol, trade_id) DO NOTHING
	`, exchanges, symbols, tradeIDs, prices, quantities, sides, tradeTimes)
	if err != nil {
		r.log.Warn("exchange_orders: save batch failed",
			zap.Int("count", len(orders)),
			zap.Error(err),
		)
		return err
	}

	if r.onSaved != nil {
		r.onSaved(len(orders))
	}
	return nil
}
