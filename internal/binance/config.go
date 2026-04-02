package binance

import (
	"os"
	"strconv"
	"time"
)

// Config содержит настройки binance-ws клиента.
type Config struct {
	LogLevel         string
	MaxWait          time.Duration
	SymbolRefreshMin int
}

// LoadConfig читает конфиг из переменных окружения с fallback на дефолты.
func LoadConfig() *Config {
	maxWaitSec := getEnvInt("RECONNECT_MAX_WAIT", 60)
	return &Config{
		LogLevel:         getEnv("LOG_LEVEL", "info"),
		MaxWait:          time.Duration(maxWaitSec) * time.Second,
		SymbolRefreshMin: getEnvInt("SYMBOL_REFRESH_MIN", 30),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
