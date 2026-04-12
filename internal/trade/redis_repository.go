package trade

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const redisKeyPrefix = "open_trade:"

// TradeRedisRepository хранит открытые сделки в Redis.
type TradeRedisRepository struct {
	rdb *redis.Client
	log *zap.Logger
}

// NewTradeRedisRepository создаёт TradeRedisRepository.
func NewTradeRedisRepository(rdb *redis.Client, log *zap.Logger) *TradeRedisRepository {
	return &TradeRedisRepository{rdb: rdb, log: log}
}

func redisTradeKey(id int64) string {
	return fmt.Sprintf("%s%d", redisKeyPrefix, id)
}

// Save сохраняет открытую сделку в Redis (без TTL).
func (r *TradeRedisRepository) Save(ctx context.Context, t *Trade) error {
	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("trade redis save: marshal: %w", err)
	}
	if err := r.rdb.Set(ctx, redisTradeKey(t.ID), data, 0).Err(); err != nil {
		return fmt.Errorf("trade redis save: set: %w", err)
	}
	r.log.Debug("trade redis: saved open trade", zap.Int64("id", t.ID), zap.String("symbol", t.Symbol))
	return nil
}

// Delete удаляет сделку из Redis.
func (r *TradeRedisRepository) Delete(ctx context.Context, id int64) error {
	if err := r.rdb.Del(ctx, redisTradeKey(id)).Err(); err != nil {
		return fmt.Errorf("trade redis delete: %w", err)
	}
	r.log.Debug("trade redis: deleted trade", zap.Int64("id", id))
	return nil
}

// LoadAll загружает все открытые сделки из Redis.
func (r *TradeRedisRepository) LoadAll(ctx context.Context) ([]*Trade, error) {
	var cursor uint64
	var trades []*Trade

	for {
		keys, next, err := r.rdb.Scan(ctx, cursor, redisKeyPrefix+"*", 100).Result()
		if err != nil {
			return nil, fmt.Errorf("trade redis load: scan: %w", err)
		}
		for _, key := range keys {
			data, err := r.rdb.Get(ctx, key).Bytes()
			if err != nil {
				r.log.Warn("trade redis load: get failed", zap.String("key", key), zap.Error(err))
				continue
			}
			var t Trade
			if err := json.Unmarshal(data, &t); err != nil {
				r.log.Warn("trade redis load: unmarshal failed", zap.String("key", key), zap.Error(err))
				continue
			}
			trades = append(trades, &t)
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}

	r.log.Info("trade redis: loaded open trades", zap.Int("count", len(trades)))
	return trades, nil
}
