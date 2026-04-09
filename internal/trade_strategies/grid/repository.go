package grid

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GridOrderRecord — запись об исполненном grid-ордере для сохранения в БД.
type GridOrderRecord struct {
	SessionID    uuid.UUID
	Symbol       string
	Exchange     string
	Side         string
	LevelIndex   int
	Price        float64
	Qty          float64
	OrderID      string
	CyclePnlUSDT *float64 // nil для buy-ордеров
}

// GridRepository сохраняет исполненные grid-ордера в таблицу grid_orders.
type GridRepository struct {
	db *pgxpool.Pool
}

// NewGridRepository создаёт GridRepository.
func NewGridRepository(db *pgxpool.Pool) *GridRepository {
	return &GridRepository{db: db}
}

// SaveFilledOrder вставляет запись об исполненном ордере.
func (r *GridRepository) SaveFilledOrder(ctx context.Context, order GridOrderRecord) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO grid_orders
		 (session_id, symbol, exchange, side, level_index, price, qty, order_id, cycle_pnl_usdt)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		order.SessionID,
		order.Symbol,
		order.Exchange,
		order.Side,
		order.LevelIndex,
		order.Price,
		order.Qty,
		order.OrderID,
		order.CyclePnlUSDT,
	)
	return err
}
