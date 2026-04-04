package comparator

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// SpreadRepository сохраняет события спреда в PostgreSQL.
type SpreadRepository struct {
	pool *pgxpool.Pool
	log  *zap.Logger
}

func NewSpreadRepository(pool *pgxpool.Pool, log *zap.Logger) *SpreadRepository {
	return &SpreadRepository{pool: pool, log: log}
}

// OpenSpread вставляет новый активный спред и записывает полученный id в event.
func (r *SpreadRepository) OpenSpread(ctx context.Context, e *SpreadEvent) {
	err := r.pool.QueryRow(ctx,
		`INSERT INTO spreads (symbol, exchange_high, exchange_low, opened_at, max_spread_pct, price_high, price_low)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		e.Symbol, e.ExchangeHigh, e.ExchangeLow, e.OpenedAt,
		e.MaxSpreadPct, e.PriceHigh, e.PriceLow,
	).Scan(&e.ID)
	if err != nil {
		r.log.Error("spread: open failed", zap.Error(err))
	}
}

// CloseSpread закрывает спред: проставляет closed_at и duration_ms.
func (r *SpreadRepository) CloseSpread(ctx context.Context, id int64, closedAt time.Time, durationMs int64) {
	_, err := r.pool.Exec(ctx,
		`UPDATE spreads SET closed_at=$1, duration_ms=$2 WHERE id=$3`,
		closedAt, durationMs, id,
	)
	if err != nil {
		r.log.Error("spread: close failed", zap.Error(err))
	}
}

// UpdateMaxSpread обновляет максимальный спред если новое значение больше.
func (r *SpreadRepository) UpdateMaxSpread(ctx context.Context, id int64, pct float64) {
	_, err := r.pool.Exec(ctx,
		`UPDATE spreads SET max_spread_pct=$1 WHERE id=$2 AND max_spread_pct < $1`,
		pct, id,
	)
	if err != nil {
		r.log.Error("spread: update max failed", zap.Error(err))
	}
}
