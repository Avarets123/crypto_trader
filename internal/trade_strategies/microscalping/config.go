package microscalping

import sharedconfig "github.com/osman/bot-traider/internal/shared/config"

// Config хранит параметры микроскальпинг стратегии v2.
type Config struct {
	Enabled   bool     // MICROSCALPING_ENABLED=false
	Exchanges []string // MICROSCALPING_EXCHANGES=binance,kucoin
	Exchange  string   // заполняется в New() для каждого Service

	// --- Параметры кита ---
	WhaleMult         float64 // MICROSCALPING_WHALE_MULT=15; множитель к среднему объёму для Whale_Threshold_USDT
	WhaleBTCAbsUSD    float64 // MICROSCALPING_WHALE_BTC_ABS_USD=200000; абсолютный порог кита для BTC
	WhaleETHAbsUSD    float64 // MICROSCALPING_WHALE_ETH_ABS_USD=100000; абсолютный порог кита для ETH
	WhaleAltAbsUSD    float64 // MICROSCALPING_WHALE_ALT_ABS_USD=50000; абсолютный порог кита для альткоинов
	WhaleOrderbookPct float64 // MICROSCALPING_WHALE_ORDERBOOK_PCT=1.0; % уровня стакана, съеденного сделкой

	// --- Параметры стены ---
	WallAnomalyMult      float64 // MICROSCALPING_WALL_ANOMALY_MULT=20; порог стены = avg * mult
	WallMinAgeSec        int     // MICROSCALPING_WALL_MIN_AGE_SEC=15; мин. возраст стены (сек) — anti-spoofing
	WallAbsorptionPct    float64 // MICROSCALPING_WALL_ABSORPTION_PCT=30; мин. поглощение для лонг-сигнала (%)
	WallAbsorptionWinSec int     // MICROSCALPING_WALL_ABSORPTION_WIN_SEC=10; окно поглощения (сек, не используется прямо — информационно)
	WallSpoofMaxSec      int     // MICROSCALPING_WALL_SPOOF_MAX_SEC=15; стены моложе этого считаются спуфингом

	// --- Параметры TP ---
	TPWallStableSec int     // MICROSCALPING_TP_WALL_STABLE_SEC=30; мин. возраст стабильной Ask-стены для TP (сек)
	TPWallOffset    float64 // MICROSCALPING_TP_WALL_OFFSET=0.01; отступ ниже стены для расчёта TP-цены
	TPPctBTCETHMin  float64 // MICROSCALPING_TP_PCT_BTC_ETH_MIN=0.7; мин. TP для BTC/ETH (%)
	TPPctBTCETHMax  float64 // MICROSCALPING_TP_PCT_BTC_ETH_MAX=1.2; макс. TP для BTC/ETH (%)
	TPPctAltMin     float64 // MICROSCALPING_TP_PCT_ALT_MIN=1.5; мин. TP для альткоинов (%)
	TPPctAltMax     float64 // MICROSCALPING_TP_PCT_ALT_MAX=3.0; макс. TP для альткоинов (%)

	// --- Параметры шорт-входа ---
	ShortPumpPct    float64 // MICROSCALPING_SHORT_PUMP_PCT=2.0; мин. рост за 15 мин для шорт-входа (%)
	ShortPumpAltPct float64 // MICROSCALPING_SHORT_PUMP_ALT_PCT=4.0; мин. рост за 15 мин для альткоинов (%)

	// --- Общие параметры ---
	MaxPositionDurationH int     // MICROSCALPING_MAX_POSITION_DURATION_H=4; максимальное время удержания позиции (часы)
	NoTradeHourStartMSK  int     // MICROSCALPING_NO_TRADE_HOUR_START_MSK=3; начало запрета торговли МСК
	NoTradeHourEndMSK    int     // MICROSCALPING_NO_TRADE_HOUR_END_MSK=10; конец запрета торговли МСК
	StopLossPct          float64 // MICROSCALPING_STOP_LOSS_PCT=0.35; резервный жёсткий SL (% от цены входа)
	TrailActivationPct   float64 // MICROSCALPING_TRAIL_ACTIVATION_PCT=0.15; trailing stop активируется при росте на X%
	TrailPct             float64 // MICROSCALPING_TRAIL_PCT=0.10; trailing stop отступ от пика (%)
	TPCheckIntervalSec   int     // MICROSCALPING_TP_CHECK_INTERVAL_SEC=5; интервал пересчёта TP/SL (сек)
	WarmupSec            int     // MICROSCALPING_WARMUP_SEC=120; период прогрева при старте (сек)
	CooldownSec          int     // MICROSCALPING_COOLDOWN_SEC=120; cooldown между сделками по символу (сек)
	MaxPositions         int     // MICROSCALPING_MAX_POSITIONS=3; максимум одновременных позиций
	TradeAmountUSDT      float64 // MICROSCALPING_TRADE_AMOUNT_USDT=10; размер сделки в USDT
}

// LoadConfig читает конфиг из переменных окружения.
func LoadConfig() Config {
	exchanges := sharedconfig.GetEnvStringSlice("MICROSCALPING_EXCHANGES")
	if len(exchanges) == 0 {
		if ex := sharedconfig.GetEnv("MICROSCALPING_EXCHANGE", "binance"); ex != "" {
			exchanges = []string{ex}
		}
	}
	return Config{
		Enabled:   sharedconfig.GetEnvBool("MICROSCALPING_ENABLED", false),
		Exchanges: exchanges,

		WhaleMult:         sharedconfig.GetEnvFloat("MICROSCALPING_WHALE_MULT", 15),
		WhaleBTCAbsUSD:    sharedconfig.GetEnvFloat("MICROSCALPING_WHALE_BTC_ABS_USD", 200000),
		WhaleETHAbsUSD:    sharedconfig.GetEnvFloat("MICROSCALPING_WHALE_ETH_ABS_USD", 100000),
		WhaleAltAbsUSD:    sharedconfig.GetEnvFloat("MICROSCALPING_WHALE_ALT_ABS_USD", 50000),
		WhaleOrderbookPct: sharedconfig.GetEnvFloat("MICROSCALPING_WHALE_ORDERBOOK_PCT", 1.0),

		WallAnomalyMult:      sharedconfig.GetEnvFloat("MICROSCALPING_WALL_ANOMALY_MULT", 20),
		WallMinAgeSec:        sharedconfig.GetEnvInt("MICROSCALPING_WALL_MIN_AGE_SEC", 15),
		WallAbsorptionPct:    sharedconfig.GetEnvFloat("MICROSCALPING_WALL_ABSORPTION_PCT", 30),
		WallAbsorptionWinSec: sharedconfig.GetEnvInt("MICROSCALPING_WALL_ABSORPTION_WIN_SEC", 10),
		WallSpoofMaxSec:      sharedconfig.GetEnvInt("MICROSCALPING_WALL_SPOOF_MAX_SEC", 15),

		TPWallStableSec: sharedconfig.GetEnvInt("MICROSCALPING_TP_WALL_STABLE_SEC", 30),
		TPWallOffset:    sharedconfig.GetEnvFloat("MICROSCALPING_TP_WALL_OFFSET", 0.01),
		TPPctBTCETHMin:  sharedconfig.GetEnvFloat("MICROSCALPING_TP_PCT_BTC_ETH_MIN", 0.7),
		TPPctBTCETHMax:  sharedconfig.GetEnvFloat("MICROSCALPING_TP_PCT_BTC_ETH_MAX", 1.2),
		TPPctAltMin:     sharedconfig.GetEnvFloat("MICROSCALPING_TP_PCT_ALT_MIN", 1.5),
		TPPctAltMax:     sharedconfig.GetEnvFloat("MICROSCALPING_TP_PCT_ALT_MAX", 3.0),

		ShortPumpPct:    sharedconfig.GetEnvFloat("MICROSCALPING_SHORT_PUMP_PCT", 2.0),
		ShortPumpAltPct: sharedconfig.GetEnvFloat("MICROSCALPING_SHORT_PUMP_ALT_PCT", 4.0),

		MaxPositionDurationH: sharedconfig.GetEnvInt("MICROSCALPING_MAX_POSITION_DURATION_H", 4),
		NoTradeHourStartMSK:  sharedconfig.GetEnvInt("MICROSCALPING_NO_TRADE_HOUR_START_MSK", 3),
		NoTradeHourEndMSK:    sharedconfig.GetEnvInt("MICROSCALPING_NO_TRADE_HOUR_END_MSK", 10),
		StopLossPct:          sharedconfig.GetEnvFloat("MICROSCALPING_STOP_LOSS_PCT", 0.35),
		TrailActivationPct:   sharedconfig.GetEnvFloat("MICROSCALPING_TRAIL_ACTIVATION_PCT", 0.15),
		TrailPct:             sharedconfig.GetEnvFloat("MICROSCALPING_TRAIL_PCT", 0.10),
		TPCheckIntervalSec:   sharedconfig.GetEnvInt("MICROSCALPING_TP_CHECK_INTERVAL_SEC", 5),
		WarmupSec:            sharedconfig.GetEnvInt("MICROSCALPING_WARMUP_SEC", 150),
		CooldownSec:          sharedconfig.GetEnvInt("MICROSCALPING_COOLDOWN_SEC", 120),
		MaxPositions:         sharedconfig.GetEnvInt("MICROSCALPING_MAX_POSITIONS", 3),
		TradeAmountUSDT:      sharedconfig.GetEnvFloat("MICROSCALPING_TRADE_AMOUNT_USDT", 10),
	}
}
