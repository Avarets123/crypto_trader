package microscalping

import sharedconfig "github.com/osman/bot-traider/internal/shared/config"

// Config хранит параметры микроскальпинг стратегии.
type Config struct {
	Enabled            bool     // MICROSCALPING_ENABLED=false
	Exchanges          []string // MICROSCALPING_EXCHANGES=binance,kucoin; если не задан — MICROSCALPING_EXCHANGE=binance
	Exchange           string   // заполняется в New() для каждого Service
	OBIMin             float64  // MICROSCALPING_OBI_MIN=0.6; мин. OBI_1pct для входа
	SpreadMaxPct       float64 // MICROSCALPING_SPREAD_MAX_PCT=0.0005; макс. спред (0.05%)
	TPFallbackPct      float64 // MICROSCALPING_TP_FALLBACK_PCT=0.4; TP если стена не найдена (% от ask0)
	TPRangePct         float64 // MICROSCALPING_TP_RANGE_PCT=2.5; диапазон поиска стены TP (% от mid-price)
	TPCheckIntervalSec int     // MICROSCALPING_TP_CHECK_INTERVAL_SEC=5; интервал пересчёта TP (сек)
	StopLossPct        float64 // MICROSCALPING_STOP_LOSS_PCT=0.35; жёсткий SL (% от цены входа)
	TrailActivationPct float64 // MICROSCALPING_TRAIL_ACTIVATION_PCT=0.15; активация trailing stop (% от entry)
	TrailPct           float64 // MICROSCALPING_TRAIL_PCT=0.10; trailing stop отступ от пика (%)
	WallMult           float64 // MICROSCALPING_WALL_MULT=5.0; порог стены = WallMult * AvgTradeVol_1min
	WhaleMult          float64 // MICROSCALPING_WHALE_MULT=3.0; «кит» если qty >= WhaleMult * AvgTradeVol_1min
	MinWhaleQty        float64 // MICROSCALPING_MIN_WHALE_QTY=0.5; абсолютный минимум объёма «кита»
	CVDCheckIntervalMs int     // MICROSCALPING_CVD_CHECK_INTERVAL_MS=300; интервал проверки CVD при открытой позиции
	MinHoldSec         int     // MICROSCALPING_MIN_HOLD_SEC=8; мин. время удержания до CVD-выхода (сек)
	WarmupSec          int     // MICROSCALPING_WARMUP_SEC=120; период прогрева при старте (сек)
	CooldownSec        int     // MICROSCALPING_COOLDOWN_SEC=120; cooldown между сделками по символу (сек)
	MaxHoldSec         int     // MICROSCALPING_MAX_HOLD_SEC=300; максимальное время удержания позиции (сек)
	MaxPositions       int     // MICROSCALPING_MAX_POSITIONS=3; максимум одновременных позиций
	TradeAmountUSDT    float64 // MICROSCALPING_TRADE_AMOUNT_USDT=10; размер сделки в USDT
}

// LoadConfig читает конфиг из переменных окружения.
func LoadConfig() Config {
	// MICROSCALPING_EXCHANGES имеет приоритет; fallback на MICROSCALPING_EXCHANGE для совместимости.
	exchanges := sharedconfig.GetEnvStringSlice("MICROSCALPING_EXCHANGES")
	if len(exchanges) == 0 {
		if ex := sharedconfig.GetEnv("MICROSCALPING_EXCHANGE", "binance"); ex != "" {
			exchanges = []string{ex}
		}
	}
	return Config{
		Enabled:            sharedconfig.GetEnvBool("MICROSCALPING_ENABLED", false),
		Exchanges:          exchanges,
		OBIMin:             sharedconfig.GetEnvFloat("MICROSCALPING_OBI_MIN", 0.6),
		SpreadMaxPct:       sharedconfig.GetEnvFloat("MICROSCALPING_SPREAD_MAX_PCT", 0.0005),
		TPFallbackPct:      sharedconfig.GetEnvFloat("MICROSCALPING_TP_FALLBACK_PCT", 0.4),
		TPRangePct:         sharedconfig.GetEnvFloat("MICROSCALPING_TP_RANGE_PCT", 2.5),
		TPCheckIntervalSec: sharedconfig.GetEnvInt("MICROSCALPING_TP_CHECK_INTERVAL_SEC", 5),
		StopLossPct:        sharedconfig.GetEnvFloat("MICROSCALPING_STOP_LOSS_PCT", 0.35),
		TrailActivationPct: sharedconfig.GetEnvFloat("MICROSCALPING_TRAIL_ACTIVATION_PCT", 0.15),
		TrailPct:           sharedconfig.GetEnvFloat("MICROSCALPING_TRAIL_PCT", 0.10),
		WallMult:           sharedconfig.GetEnvFloat("MICROSCALPING_WALL_MULT", 5.0),
		WhaleMult:          sharedconfig.GetEnvFloat("MICROSCALPING_WHALE_MULT", 3.0),
		MinWhaleQty:        sharedconfig.GetEnvFloat("MICROSCALPING_MIN_WHALE_QTY", 0.5),
		CVDCheckIntervalMs: sharedconfig.GetEnvInt("MICROSCALPING_CVD_CHECK_INTERVAL_MS", 300),
		MinHoldSec:         sharedconfig.GetEnvInt("MICROSCALPING_MIN_HOLD_SEC", 8),
		WarmupSec:          sharedconfig.GetEnvInt("MICROSCALPING_WARMUP_SEC", 150),
		CooldownSec:        sharedconfig.GetEnvInt("MICROSCALPING_COOLDOWN_SEC", 120),
		MaxHoldSec:         sharedconfig.GetEnvInt("MICROSCALPING_MAX_HOLD_SEC", 300),
		MaxPositions:       sharedconfig.GetEnvInt("MICROSCALPING_MAX_POSITIONS", 3),
		TradeAmountUSDT:    sharedconfig.GetEnvFloat("MICROSCALPING_TRADE_AMOUNT_USDT", 10),
	}
}
