package binance

import (
	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
)

// Config содержит настройки binance ws-клиента.
type Config struct {
	sharedconfig.Base
	WSURL   string
	RestURL string
	Enabled bool
}

// LoadConfig читает конфиг из переменных окружения с fallback на дефолты.
func LoadConfig() *Config {
	base := sharedconfig.LoadBase()
	wsURL := "wss://stream.binance.com:9443"
	restURL := "https://api.binance.com"
	if base.DevMode {
		wsURL = "wss://testnet.binance.vision"
		restURL = "https://testnet.binance.vision"
	}
	return &Config{
		Base:    base,
		WSURL:   wsURL,
		RestURL: restURL,
		Enabled: sharedconfig.GetEnvBool("BINANCE_ENABLED", true),
	}
}
