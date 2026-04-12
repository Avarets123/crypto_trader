package kucoin

import (
	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
)

// Config содержит настройки KuCoin клиента.
type Config struct {
	sharedconfig.Base
	RestURL    string
	APIKey     string
	APISecret  string
	Passphrase string
	Enabled    bool
}

// LoadConfig читает конфиг из переменных окружения с fallback на дефолты.
func LoadConfig() *Config {
	base := sharedconfig.LoadBase()
	return &Config{
		Base:       base,
		RestURL:    sharedconfig.GetEnv("KUCOIN_REST_URL", "https://api.kucoin.com"),
		APIKey:     sharedconfig.GetEnv("KUCOIN_API_KEY", ""),
		APISecret:  sharedconfig.GetEnv("KUCOIN_API_SECRET", ""),
		Passphrase: sharedconfig.GetEnv("KUCOIN_PASSPHRASE", ""),
		Enabled:    sharedconfig.GetEnvBool("KUCOIN_ENABLED", false),
	}
}
