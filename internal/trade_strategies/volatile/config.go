package volatile

import sharedconfig "github.com/osman/bot-traider/internal/shared/config"

// Config хранит параметры микроскальпинг стратегии.
type Config struct {
	Enabled            bool    // VOLATILE_ENABLED=false
	Exchange           string  // VOLATILE_EXCHANGE=binance
	OBIMin             float64 // VOLATILE_OBI_MIN=0.6; мин. OBI_1pct для входа
	SpreadMaxPct       float64 // VOLATILE_SPREAD_MAX_PCT=0.0005; макс. спред (0.05%)
	TPFallbackPct      float64 // VOLATILE_TP_FALLBACK_PCT=0.2; TP если стена не найдена (% от ask0)
	StopLossPct        float64 // VOLATILE_STOP_LOSS_PCT=0.35; жёсткий SL (% от цены входа)
	WallMult           float64 // VOLATILE_WALL_MULT=5.0; порог стены = WallMult * AvgTradeVol_1min
	WhaleMult          float64 // VOLATILE_WHALE_MULT=3.0; «кит» если qty >= WhaleMult * AvgTradeVol_1min
	MinWhaleQty        float64 // VOLATILE_MIN_WHALE_QTY=0.5; абсолютный минимум объёма «кита»
	CVDCheckIntervalMs int     // VOLATILE_CVD_CHECK_INTERVAL_MS=300; интервал проверки CVD при открытой позиции
	WarmupSec          int     // VOLATILE_WARMUP_SEC=120; период прогрева при старте (сек)
	CooldownSec        int     // VOLATILE_COOLDOWN_SEC=120; cooldown между сделками по символу (сек)
	MaxHoldSec         int     // VOLATILE_MAX_HOLD_SEC=300; максимальное время удержания позиции (сек)
	MaxPositions       int     // VOLATILE_MAX_POSITIONS=3; максимум одновременных позиций
	TradeAmountUSDT    float64 // VOLATILE_TRADE_AMOUNT_USDT=10; размер сделки в USDT
}

// LoadConfig читает конфиг из переменных окружения.
func LoadConfig() Config {
	return Config{
		Enabled:            sharedconfig.GetEnvBool("VOLATILE_ENABLED", false),
		Exchange:           sharedconfig.GetEnv("VOLATILE_EXCHANGE", "binance"),
		OBIMin:             sharedconfig.GetEnvFloat("VOLATILE_OBI_MIN", 0.6),
		SpreadMaxPct:       sharedconfig.GetEnvFloat("VOLATILE_SPREAD_MAX_PCT", 0.0005),
		TPFallbackPct:      sharedconfig.GetEnvFloat("VOLATILE_TP_FALLBACK_PCT", 0.2),
		StopLossPct:        sharedconfig.GetEnvFloat("VOLATILE_STOP_LOSS_PCT", 0.35),
		WallMult:           sharedconfig.GetEnvFloat("VOLATILE_WALL_MULT", 5.0),
		WhaleMult:          sharedconfig.GetEnvFloat("VOLATILE_WHALE_MULT", 3.0),
		MinWhaleQty:        sharedconfig.GetEnvFloat("VOLATILE_MIN_WHALE_QTY", 0.5),
		CVDCheckIntervalMs: sharedconfig.GetEnvInt("VOLATILE_CVD_CHECK_INTERVAL_MS", 300),
		WarmupSec:          sharedconfig.GetEnvInt("VOLATILE_WARMUP_SEC", 150),
		CooldownSec:        sharedconfig.GetEnvInt("VOLATILE_COOLDOWN_SEC", 120),
		MaxHoldSec:         sharedconfig.GetEnvInt("VOLATILE_MAX_HOLD_SEC", 300),
		MaxPositions:       sharedconfig.GetEnvInt("VOLATILE_MAX_POSITIONS", 3),
		TradeAmountUSDT:    sharedconfig.GetEnvFloat("VOLATILE_TRADE_AMOUNT_USDT", 10),
	}
}
