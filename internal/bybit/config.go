package bybit

import (
	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
)

// Config содержит настройки bybit ws-клиента.
type Config struct {
	sharedconfig.Base
	WSURL   string
	RestURL string
}

// LoadConfig читает конфиг из переменных окружения с fallback на дефолты.
func LoadConfig() *Config {
	base := sharedconfig.LoadBase()
	wsURL := "wss://stream.bybit.com/v5/public/spot"
	restURL := "https://api.bybit.com/v5/market/instruments-info?category=spot"
	if base.DevMode {
		wsURL = "wss://stream-testnet.bybit.com/v5/public/spot"
		restURL = "https://api-testnet.bybit.com/v5/market/instruments-info?category=spot"
	}
	return &Config{
		Base:    base,
		WSURL:   wsURL,
		RestURL: restURL,
	}
}
