package config

import (
	"os"
	"strconv"
	"time"
)

// Base — общие настройки для всех бирж.
type Base struct {
	LogLevel         string
	MaxWait          time.Duration
	SymbolRefreshMin int
	DevMode          bool
}

// LoadBase читает общий конфиг из переменных окружения.
func LoadBase() Base {
	maxWaitSec := GetEnvInt("RECONNECT_MAX_WAIT", 60)
	return Base{
		LogLevel:         GetEnv("LOG_LEVEL", "info"),
		MaxWait:          time.Duration(maxWaitSec) * time.Second,
		SymbolRefreshMin: GetEnvInt("SYMBOL_REFRESH_MIN", 30),
		DevMode:          GetEnv("DEV_MODE", "false") == "true",
	}
}

// GetEnv возвращает значение переменной окружения или fallback.
func GetEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// GetEnvInt возвращает целочисленное значение переменной окружения или fallback.
func GetEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
