package arbitration

import sharedconfig "github.com/osman/bot-traider/internal/shared/config"

// Config хранит параметры Lead-Lag арбитража.
type Config struct {
	Enabled      bool    // ARB_ENABLED=true
	MinSpreadPct float64 // ARB_MIN_SPREAD_PCT=0.5  (эффективный спред после комиссий)
	CooldownSec  int     // ARB_COOLDOWN_SEC=60
	MaxHoldSec   int     // ARB_MAX_HOLD_SEC=300
	StopLossPct  float64 // ARB_STOP_LOSS_PCT=0.5
	TradeAmount float64
}

// LoadConfig читает конфиг из env.
func LoadConfig() Config {
	return Config{
		Enabled:      sharedconfig.GetEnv("ARB_ENABLED", "true") == "true",
		MinSpreadPct: sharedconfig.GetEnvFloat("ARB_MIN_SPREAD_PCT", 0.5),
		CooldownSec:  sharedconfig.GetEnvInt("ARB_COOLDOWN_SEC", 60),
		MaxHoldSec:   sharedconfig.GetEnvInt("ARB_MAX_HOLD_SEC", 300),
		StopLossPct:  sharedconfig.GetEnvFloat("ARB_STOP_LOSS_PCT", 0.5),
		TradeAmount: sharedconfig.GetEnvFloat("TRADE_AMOUNT_USDT", 10),
	}
}
