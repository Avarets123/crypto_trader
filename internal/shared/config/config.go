package config

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// Base — общие настройки для всех бирж.
type Base struct {
	LogLevel           string
	MaxWait            time.Duration
	SymbolRefreshMin   int
	DevMode            bool
	PostgresDSN        string
	SpreadThresholdPct float64
	// WatchSymbols — зафиксированный список символов (Binance/Bybit формат, напр. BTCUSDT).
	// Если пустой — символы берутся с биржи. Формат OKX (BTC-USDT) конвертируется автоматически.
	WatchSymbols []string
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
		SpreadThresholdPct: GetEnvFloat("SPREAD_THRESHOLD_PCT", 1.0),
		WatchSymbols:       GetEnvStringSlice("WATCH_SYMBOLS"),
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

// GetEnvBool возвращает bool-значение переменной окружения или fallback.
func GetEnvBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

// GetEnvStringSlice парсит переменную окружения как список строк через запятую.
// Возвращает nil если переменная не задана или пуста.
func GetEnvStringSlice(key string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	var result []string
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			result = append(result, s)
		}
	}
	return result
}
