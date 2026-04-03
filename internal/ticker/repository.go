package ticker

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

type TickerRepository struct {
	pool *pgxpool.Pool
	log  *zap.Logger
}

func NewRepository(pool *pgxpool.Pool, log *zap.Logger) *TickerRepository {
	return &TickerRepository{pool: pool, log: log}
}

func (r *TickerRepository) SaveBatch(ctx context.Context, batch []Ticker) error {
	if len(batch) == 0 {
		return nil
	}

	rows := make([][]any, 0, len(batch))

	for _, t := range batch {
		price, err := parseFloat(t.Price)
		if err != nil {
			r.log.Warn("skip ticker: invalid price", zap.String("symbol", t.Symbol), zap.String("price", t.Price))
			continue
		}
		rows = append(rows, []any{
			t.Exchange,
			t.Symbol,
			t.Quote,
			price,
			parseFloatNull(t.Open24h),
			parseFloatNull(t.High24h),
			parseFloatNull(t.Low24h),
			parseFloatNull(t.Volume24h),
			parseFloatNull(cleanPct(t.ChangePct)),
			t.CreatedAt,
		})
	}

	insCount, err := r.pool.CopyFrom(
		ctx,
		pgx.Identifier{"tickers"},
		[]string{"exchange", "symbol", "quote", "price", "open_24h", "high_24h", "low_24h", "volume_24h", "change_pct", "received_at"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		r.log.Error("repository: CopyFrom failed", zap.Error(err), zap.Int("batch_size", len(rows)))
		return err
	}

	r.log.Info(fmt.Sprintf("Inserset tickers count is: %d", insCount))

	return nil
}

func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

func parseFloatNull(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

func cleanPct(s string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s), "+"), "%")
}
