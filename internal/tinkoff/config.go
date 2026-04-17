package tinkoff

import (
	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
)

// Config содержит настройки клиента Т-Инвестиции.
type Config struct {
	Token          string
	Enabled        bool
	AccountID      string   // идентификатор торгового счёта (TINKOFF_ACCOUNT_ID)
	ExtraSymbols   []string // тикеры, всегда включённые в список (TINKOFF_EXTRA_SYMBOLS)
	Sandbox        bool
	OrderBookDepth int32
	TopLimit       int    // кол-во топ-волатильных акций (TINKOFF_TOP_LIMIT)
	ExchangeFilter string // класс-код биржи для фильтрации (TINKOFF_EXCHANGE_FILTER), напр. TQBR
	TradeEnabled   bool    // разрешить выставление ордеров (TINKOFF_TRADE_ENABLED)
	TradeAmountRUB float64 // макс. стоимость одной сделки в рублях (TINKOFF_TRADE_AMOUNT_RUB); 0 = использовать qty от стратегии
}

// LoadConfig читает конфиг из переменных окружения.
func LoadConfig() *Config {
	return &Config{
		Token:          sharedconfig.GetEnv("TINKOFF_TOKEN", ""),
		Enabled:        sharedconfig.GetEnvBool("TINKOFF_ENABLED", false),
		AccountID:      sharedconfig.GetEnv("TINKOFF_ACCOUNT_ID", ""),
		ExtraSymbols:   sharedconfig.GetEnvStringSlice("TINKOFF_EXTRA_SYMBOLS"),
		Sandbox:        sharedconfig.GetEnvBool("TINKOFF_SANDBOX", false),
		OrderBookDepth: int32(sharedconfig.GetEnvInt("TINKOFF_ORDERBOOK_DEPTH", 20)),
		TopLimit:       sharedconfig.GetEnvInt("TINKOFF_TOP_LIMIT", 10),
		ExchangeFilter: sharedconfig.GetEnv("TINKOFF_EXCHANGE_FILTER", "TQBR"),
		TradeEnabled:   sharedconfig.GetEnvBool("TINKOFF_TRADE_ENABLED", false),
		TradeAmountRUB: sharedconfig.GetEnvFloat("TINKOFF_TRADE_AMOUNT_RUB", 0),
	}
}
