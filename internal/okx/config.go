package okx

import (
	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
)

// Config содержит настройки okx ws-клиента.
type Config struct {
	sharedconfig.Base
	WSURL         string
	RestURL       string
	Enabled       bool
	APIKey        string
	APISecret     string
	APIPassphrase string
}

// LoadConfig читает конфиг из переменных окружения с fallback на дефолты.
func LoadConfig() *Config {
	base := sharedconfig.LoadBase()
	wsURL := "wss://ws.okx.com:8443/ws/v5/public"
	restURL := "https://www.okx.com/api/v5/public/instruments?instType=SPOT"
	if base.DevMode {
		wsURL = "wss://wspap.okx.com:8443/ws/v5/public?brokerId=9999"
	}
	return &Config{
		Base:          base,
		WSURL:         wsURL,
		RestURL:       restURL,
		Enabled:       sharedconfig.GetEnvBool("OKX_ENABLED", true),
		APIKey:        sharedconfig.GetEnv("OKX_API_KEY", ""),
		APISecret:     sharedconfig.GetEnv("OKX_API_SECRET", ""),
		APIPassphrase: sharedconfig.GetEnv("OKX_API_PASSPHRASE", ""),
	}
}
