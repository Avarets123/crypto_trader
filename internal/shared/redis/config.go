package redisclient

import sharedconfig "github.com/osman/bot-traider/internal/shared/config"

// Config — настройки подключения к Redis.
type Config struct {
	Addr     string
	Password string
	DB       int
}

// LoadConfig читает конфиг Redis из переменных окружения.
func LoadConfig() Config {
	return Config{
		Addr:     sharedconfig.GetEnv("REDIS_ADDR", "localhost:6379"),
		Password: sharedconfig.GetEnv("REDIS_PASSWORD", ""),
		DB:       sharedconfig.GetEnvInt("REDIS_DB", 0),
	}
}
