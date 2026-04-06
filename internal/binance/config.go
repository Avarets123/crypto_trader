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
	// Рыночные данные всегда с mainnet — testnet не имеет живых стримов.
	// Testnet используется только REST-клиентом для размещения ордеров (binance/rest.go).
	wsURL := "wss://stream.binance.com:9443"
	restURL := "https://api.binance.com"
	return &Config{
		Base:    base,
		WSURL:   wsURL,
		RestURL: restURL,
		Enabled: sharedconfig.GetEnvBool("BINANCE_ENABLED", true),
	}
}
