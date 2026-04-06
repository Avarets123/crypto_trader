package trade

import (
	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
)

// TradeChecker предоставляет in-memory проверку открытых позиций.
type TradeChecker interface {
	HasOpenPosition(symbol string) bool
	CountOpenPositions() int
}


// Config хранит параметры риск-менеджера.
type Config struct {
	TradeAmountUSDT  float64 // TRADE_AMOUNT_USDT=10
	DailyLossCapUSDT float64 // RISK_DAILY_LOSS_CAP_USDT=50
	MaxOpenPositions int     // RISK_MAX_OPEN_POSITIONS=3
}

// LoadConfig читает конфиг из env.
func LoadConfig() Config {
	return Config{
		TradeAmountUSDT:  sharedconfig.GetEnvFloat("TRADE_AMOUNT_USDT", 10),
		DailyLossCapUSDT: sharedconfig.GetEnvFloat("RISK_DAILY_LOSS_CAP_USDT", 50),
		MaxOpenPositions: sharedconfig.GetEnvInt("RISK_MAX_OPEN_POSITIONS", 3),
	}
}


