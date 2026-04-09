package news

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// Repository сохраняет новости в таблицу news.
type Repository struct {
	pool *pgxpool.Pool
	log  *zap.Logger
}

// NewRepository создаёт Repository.
func NewRepository(pool *pgxpool.Pool, log *zap.Logger) *Repository {
	return &Repository{pool: pool, log: log}
}

// SaveBatch вставляет статьи, дубли молча игнорируются (ON CONFLICT DO NOTHING).
// Возвращает количество реально сохранённых строк.
func (r *Repository) SaveBatch(ctx context.Context, articles []Article) (int64, error) {
	if len(articles) == 0 {
		return 0, nil
	}

	var saved int64
	for _, a := range articles {
		tag, err := r.pool.Exec(ctx,
			`INSERT INTO news (source, guid, title, link, description, summary, published_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)
			 ON CONFLICT (guid) DO NOTHING`,
			a.Source, a.GUID, a.Title, a.Link, a.Description, a.Summary, a.PublishedAt,
		)
		if err != nil {
			r.log.Warn("news: failed to insert article",
				zap.String("guid", a.GUID),
				zap.String("source", a.Source),
				zap.Error(err),
			)
			continue
		}
		saved += tag.RowsAffected()
	}
	return saved, nil
}
