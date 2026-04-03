package gateio

import (
	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
)

// Config содержит настройки gateio ws-клиента.
type Config struct {
	sharedconfig.Base
	WSURL   string
	RestURL string
}

// LoadConfig читает конфиг из переменных окружения с fallback на дефолты.
// Gate.io не имеет публичного testnet, поэтому DevMode не меняет URL.
func LoadConfig() *Config {
	base := sharedconfig.LoadBase()
	return &Config{
		Base:    base,
		WSURL:   "wss://api.gateio.ws/ws/v4/",
		RestURL: "https://api.gateio.ws/api/v4/spot/currency_pairs",
	}
}
