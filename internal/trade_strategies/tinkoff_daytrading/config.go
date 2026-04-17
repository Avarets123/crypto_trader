package tinkoff_daytrading

import sharedconfig "github.com/osman/bot-traider/internal/shared/config"

// Config — параметры стратегии дейтрейдинга Тинькофф.
type Config struct {
	Enabled    bool
	LotLimit   float64
	CapitalPct float64
}

// LoadConfig читает конфигурацию из переменных окружения.
func LoadConfig() Config {
	return Config{
		Enabled:    sharedconfig.GetEnvBool("TINKOFF_DAYTRADING_ENABLED", false),
		LotLimit:   sharedconfig.GetEnvFloat("TINKOFF_DAYTRADING_LOT_LIMIT", 1),
		CapitalPct: sharedconfig.GetEnvFloat("TINKOFF_DAYTRADING_CAPITAL_PCT", 0.1),
	}
}
