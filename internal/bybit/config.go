package bybit

import (
	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
)

// Config содержит настройки bybit ws-клиента.
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
	// Testnet используется только REST-клиентом для размещения ордеров (bybit/rest.go).
	wsURL := "wss://stream.bybit.com/v5/public/spot"
	restURL := "https://api.bybit.com/v5/market/instruments-info?category=spot"
	return &Config{
		Base:    base,
		WSURL:   wsURL,
		RestURL: restURL,
		Enabled: sharedconfig.GetEnvBool("BYBIT_ENABLED", true),
	}
}
