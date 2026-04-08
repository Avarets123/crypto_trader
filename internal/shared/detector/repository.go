package detector

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// DetectorRepository сохраняет события детектора в PostgreSQL.
type DetectorRepository struct {
	pool *pgxpool.Pool
	log  *zap.Logger
}

func NewDetectorRepository(pool *pgxpool.Pool, log *zap.Logger) *DetectorRepository {
	return &DetectorRepository{pool: pool, log: log}
}

// SaveVolumeSpike вставляет событие аномального объёма в таблицу volume_spikes.
func (r *DetectorRepository) SaveVolumeSpike(ctx context.Context, e VolumeEvent) {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO volume_spikes (symbol, exchange, type, spike_ratio, volume_usdt, avg_usdt, price, change_pct)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		e.Symbol, e.Exchange, string(e.Type), e.SpikeRatio, e.Volume, e.AvgVolume, e.Price, e.ChangePct,
	)
	if err != nil {
		r.log.Error("volume spike: save failed",
			zap.String("symbol", e.Symbol),
			zap.String("exchange", e.Exchange),
			zap.String("type", string(e.Type)),
			zap.Error(err),
		)
	}
}

// Save вставляет событие в таблицу price_change_detections и записывает полученный id.
func (r *DetectorRepository) Save(ctx context.Context, e *DetectorEvent) {
	err := r.pool.QueryRow(ctx,
		`INSERT INTO price_change_detections
		 (type, symbol, exchange, detected_at, window_sec, price_before, price_now, change_pct)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		e.Type, e.Symbol, e.Exchange, e.DetectedAt,
		e.WindowSec, e.PriceBefore, e.PriceNow, e.ChangePct,
	).Scan(&e.ID)
	if err != nil {
		r.log.Error("detector: save failed", zap.String("type", e.Type), zap.Error(err))
	}
}
