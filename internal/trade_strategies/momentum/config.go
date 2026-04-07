package momentum

import sharedconfig "github.com/osman/bot-traider/internal/shared/config"

// Config хранит параметры Momentum стратегии.
type Config struct {
	Enabled          bool    // MOMENTUM_ENABLED=false
	SignalExchange   string  // MOMENTUM_SIGNAL_EXCHANGE=binance; пусто = все биржи
	MinPumpPct       float64 // MOMENTUM_MIN_PUMP_PCT=2.0
	TrailingStopPct  float64 // MOMENTUM_TRAILING_STOP_PCT=0.8
	TakeProfitPct    float64 // MOMENTUM_TAKE_PROFIT_PCT=3.0
	StopLossPct      float64 // MOMENTUM_STOP_LOSS_PCT=1.5
	CooldownSec      int     // MOMENTUM_COOLDOWN_SEC=120
	MaxHoldSec       int     // MOMENTUM_MAX_HOLD_SEC=300
	MaxPositions     int     // MOMENTUM_MAX_POSITIONS=3
	TradeAmountUSDT  float64 // MOMENTUM_TRADE_AMOUNT_USDT=10
}

// LoadConfig читает конфиг из env.
func LoadConfig() Config {
	return Config{
		Enabled:         sharedconfig.GetEnvBool("MOMENTUM_ENABLED", false),
		SignalExchange:  sharedconfig.GetEnv("MOMENTUM_SIGNAL_EXCHANGE", "binance"),
		MinPumpPct:      sharedconfig.GetEnvFloat("MOMENTUM_MIN_PUMP_PCT", 2.0),
		TrailingStopPct: sharedconfig.GetEnvFloat("MOMENTUM_TRAILING_STOP_PCT", 0.8),
		TakeProfitPct:   sharedconfig.GetEnvFloat("MOMENTUM_TAKE_PROFIT_PCT", 3.0),
		StopLossPct:     sharedconfig.GetEnvFloat("MOMENTUM_STOP_LOSS_PCT", 1.5),
		CooldownSec:     sharedconfig.GetEnvInt("MOMENTUM_COOLDOWN_SEC", 120),
		MaxHoldSec:      sharedconfig.GetEnvInt("MOMENTUM_MAX_HOLD_SEC", 300),
		MaxPositions:    sharedconfig.GetEnvInt("MOMENTUM_MAX_POSITIONS", 3),
		TradeAmountUSDT: sharedconfig.GetEnvFloat("MOMENTUM_TRADE_AMOUNT_USDT", 10),
	}
}
