package risk

import sharedconfig "github.com/osman/bot-traider/internal/shared/config"

// Config — параметры risk-менеджера.
type Config struct {
	InitialCapital    float64            // RISK_INITIAL_CAPITAL_USDT — начальный капитал бота (USDT)
	DailyLossLimitPct float64            // RISK_DAILY_LOSS_LIMIT_PCT — % от капитала, при достижении дневного убытка бот блокируется на сутки
	MaxDrawdownPct    float64            // RISK_MAX_DRAWDOWN_PCT — % от капитала, при достижении общего убытка бот блокируется до ручного reset
	PerStrategyCap    map[string]float64 // капы exposure по стратегиям, USDT
}

// LoadConfig читает параметры из ENV.
func LoadConfig() Config {
	return Config{
		InitialCapital:    sharedconfig.GetEnvFloat("RISK_INITIAL_CAPITAL_USDT", 200),
		DailyLossLimitPct: sharedconfig.GetEnvFloat("RISK_DAILY_LOSS_LIMIT_PCT", 10),
		MaxDrawdownPct:    sharedconfig.GetEnvFloat("RISK_MAX_DRAWDOWN_PCT", 20),
		PerStrategyCap: map[string]float64{
			"news_momentum": sharedconfig.GetEnvFloat("RISK_NEWS_MOMENTUM_CAP_USDT", 100),
			"grid":          sharedconfig.GetEnvFloat("RISK_GRID_CAP_USDT", 100),
		},
	}
}
