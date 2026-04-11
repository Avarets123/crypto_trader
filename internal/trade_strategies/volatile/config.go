package volatile

import sharedconfig "github.com/osman/bot-traider/internal/shared/config"

// Config хранит параметры Volatile стратегии.
type Config struct {
	Enabled          bool    // VOLATILE_ENABLED=false
	Exchange         string  // VOLATILE_EXCHANGE=bybit; биржа где размещаются ордера
	BullScoreMin     float64 // VOLATILE_BULL_SCORE_MIN=0.45; минимальный BullScore для входа
	CheckIntervalSec int     // VOLATILE_CHECK_INTERVAL_SEC=5; интервал проверки сигнала (сек)
	TradeWindowSec   int     // VOLATILE_TRADE_WINDOW_SEC=300; окно статистики сделок (сек)
	TrailingStopPct  float64 // VOLATILE_TRAILING_STOP_PCT=1.0
	TakeProfitPct    float64 // VOLATILE_TAKE_PROFIT_PCT=2.5
	StopLossPct      float64 // VOLATILE_STOP_LOSS_PCT=1.5
	CooldownSec      int     // VOLATILE_COOLDOWN_SEC=120
	MaxHoldSec       int     // VOLATILE_MAX_HOLD_SEC=300
	MaxPositions     int     // VOLATILE_MAX_POSITIONS=3
	TradeAmountUSDT  float64 // VOLATILE_TRADE_AMOUNT_USDT=10
}

// LoadConfig читает конфиг из env.
func LoadConfig() Config {
	return Config{
		Enabled:          sharedconfig.GetEnvBool("VOLATILE_ENABLED", false),
		Exchange:         sharedconfig.GetEnv("VOLATILE_EXCHANGE", "binance"),
		BullScoreMin:     sharedconfig.GetEnvFloat("VOLATILE_BULL_SCORE_MIN", 0.45),
		CheckIntervalSec: sharedconfig.GetEnvInt("VOLATILE_CHECK_INTERVAL_SEC", 5),
		TradeWindowSec:   sharedconfig.GetEnvInt("VOLATILE_TRADE_WINDOW_SEC", 300),
		TrailingStopPct:  sharedconfig.GetEnvFloat("VOLATILE_TRAILING_STOP_PCT", 1.0),
		TakeProfitPct:    sharedconfig.GetEnvFloat("VOLATILE_TAKE_PROFIT_PCT", 2.5),
		StopLossPct:      sharedconfig.GetEnvFloat("VOLATILE_STOP_LOSS_PCT", 1.5),
		CooldownSec:      sharedconfig.GetEnvInt("VOLATILE_COOLDOWN_SEC", 120),
		MaxHoldSec:       sharedconfig.GetEnvInt("VOLATILE_MAX_HOLD_SEC", 300),
		MaxPositions:     sharedconfig.GetEnvInt("VOLATILE_MAX_POSITIONS", 3),
		TradeAmountUSDT:  sharedconfig.GetEnvFloat("VOLATILE_TRADE_AMOUNT_USDT", 10),
	}
}
