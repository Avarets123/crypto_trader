package config

import (
	"log"
	"os"
	"strconv"
	"time"
)

// Base — общие настройки для всех бирж.
type Base struct {
	LogLevel            string
	MaxWait             time.Duration
	SymbolRefreshMin    int
	DevMode             bool
	PostgresDSN         string
	SpreadThresholdPct  float64
}

// LoadBase читает общий конфиг из переменных окружения.
func LoadBase() Base {
	maxWaitSec := GetEnvInt("RECONNECT_MAX_WAIT", 60)
	postgresDSN := os.Getenv("POSTGRES_DSN")
	if postgresDSN == "" {
		log.Fatal("POSTGRES_DSN not passed!")
	}
	return Base{
		LogLevel:           GetEnv("LOG_LEVEL", "info"),
		MaxWait:            time.Duration(maxWaitSec) * time.Second,
		SymbolRefreshMin:   GetEnvInt("SYMBOL_REFRESH_MIN", 30),
		DevMode:            GetEnv("DEV_MODE", "false") == "true",
		PostgresDSN:        postgresDSN,
		SpreadThresholdPct: GetEnvFloat("SPREAD_THRESHOLD_PCT", 5.0),
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

// GetEnvFloat возвращает float64-значение переменной окружения или fallback.
func GetEnvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}
