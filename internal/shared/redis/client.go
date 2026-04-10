package redisclient

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// New создаёт Redis-клиент, проверяет подключение через Ping и возвращает ошибку при неудаче.
func New(ctx context.Context, cfg Config, log *zap.Logger) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}

	log.Info("redis connected", zap.String("addr", cfg.Addr), zap.Int("db", cfg.DB))
	return client, nil
}
