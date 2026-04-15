package microscalping

import sharedconfig "github.com/osman/bot-traider/internal/shared/config"

// Config хранит параметры микроскальпинг стратегии.
type Config struct {
	Enabled            bool    // MICROSCALPING_ENABLED=false
	Exchange           string  // MICROSCALPING_EXCHANGE=binance
	OBIMin             float64 // MICROSCALPING_OBI_MIN=0.6; мин. OBI_1pct для входа
	SpreadMaxPct       float64 // MICROSCALPING_SPREAD_MAX_PCT=0.0005; макс. спред (0.05%)
	TPFallbackPct      float64 // MICROSCALPING_TP_FALLBACK_PCT=0.2; TP если стена не найдена (% от ask0)
	StopLossPct        float64 // MICROSCALPING_STOP_LOSS_PCT=0.35; жёсткий SL (% от цены входа)
	WallMult           float64 // MICROSCALPING_WALL_MULT=5.0; порог стены = WallMult * AvgTradeVol_1min
	WhaleMult          float64 // MICROSCALPING_WHALE_MULT=3.0; «кит» если qty >= WhaleMult * AvgTradeVol_1min
	MinWhaleQty        float64 // MICROSCALPING_MIN_WHALE_QTY=0.5; абсолютный минимум объёма «кита»
	CVDCheckIntervalMs int     // MICROSCALPING_CVD_CHECK_INTERVAL_MS=300; интервал проверки CVD при открытой позиции
	WarmupSec          int     // MICROSCALPING_WARMUP_SEC=120; период прогрева при старте (сек)
	CooldownSec        int     // MICROSCALPING_COOLDOWN_SEC=120; cooldown между сделками по символу (сек)
	MaxHoldSec         int     // MICROSCALPING_MAX_HOLD_SEC=300; максимальное время удержания позиции (сек)
	MaxPositions       int     // MICROSCALPING_MAX_POSITIONS=3; максимум одновременных позиций
	TradeAmountUSDT    float64 // MICROSCALPING_TRADE_AMOUNT_USDT=10; размер сделки в USDT
}

// LoadConfig читает конфиг из переменных окружения.
func LoadConfig() Config {
	return Config{
		Enabled:            sharedconfig.GetEnvBool("MICROSCALPING_ENABLED", false),
		Exchange:           sharedconfig.GetEnv("MICROSCALPING_EXCHANGE", "binance"),
		OBIMin:             sharedconfig.GetEnvFloat("MICROSCALPING_OBI_MIN", 0.6),
		SpreadMaxPct:       sharedconfig.GetEnvFloat("MICROSCALPING_SPREAD_MAX_PCT", 0.0005),
		TPFallbackPct:      sharedconfig.GetEnvFloat("MICROSCALPING_TP_FALLBACK_PCT", 0.2),
		StopLossPct:        sharedconfig.GetEnvFloat("MICROSCALPING_STOP_LOSS_PCT", 0.35),
		WallMult:           sharedconfig.GetEnvFloat("MICROSCALPING_WALL_MULT", 5.0),
		WhaleMult:          sharedconfig.GetEnvFloat("MICROSCALPING_WHALE_MULT", 3.0),
		MinWhaleQty:        sharedconfig.GetEnvFloat("MICROSCALPING_MIN_WHALE_QTY", 0.5),
		CVDCheckIntervalMs: sharedconfig.GetEnvInt("MICROSCALPING_CVD_CHECK_INTERVAL_MS", 300),
		WarmupSec:          sharedconfig.GetEnvInt("MICROSCALPING_WARMUP_SEC", 150),
		CooldownSec:        sharedconfig.GetEnvInt("MICROSCALPING_COOLDOWN_SEC", 120),
		MaxHoldSec:         sharedconfig.GetEnvInt("MICROSCALPING_MAX_HOLD_SEC", 300),
		MaxPositions:       sharedconfig.GetEnvInt("MICROSCALPING_MAX_POSITIONS", 3),
		TradeAmountUSDT:    sharedconfig.GetEnvFloat("MICROSCALPING_TRADE_AMOUNT_USDT", 10),
	}
}
