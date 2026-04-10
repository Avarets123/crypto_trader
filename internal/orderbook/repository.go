package orderbook

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const ttl = 30 * 24 * time.Hour

// Repository хранит стаканы в Redis.
type Repository struct {
	rdb *redis.Client
}

// NewRepository создаёт Repository.
func NewRepository(rdb *redis.Client) *Repository {
	return &Repository{rdb: rdb}
}

func key(symbol string) string {
	return "orderbook:" + symbol
}

// Save сериализует OrderBook в JSON и сохраняет в Redis с TTL 30 дней.
func (r *Repository) Save(ctx context.Context, ob OrderBook) error {
	data, err := json.Marshal(ob)
	if err != nil {
		return fmt.Errorf("orderbook save marshal: %w", err)
	}
	return r.rdb.Set(ctx, key(ob.Symbol), data, ttl).Err()
}

// Get загружает OrderBook из Redis. Возвращает redis.Nil если ключ не найден.
func (r *Repository) Get(ctx context.Context, symbol string) (*OrderBook, error) {
	data, err := r.rdb.Get(ctx, key(symbol)).Bytes()
	if err != nil {
		return nil, err
	}
	var ob OrderBook
	if err := json.Unmarshal(data, &ob); err != nil {
		return nil, fmt.Errorf("orderbook get unmarshal: %w", err)
	}
	return &ob, nil
}
