package grid

import (
	"strings"

	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
)

// Config хранит параметры Grid стратегии.
type Config struct {
	Enabled         bool     // GRID_ENABLED — включить Grid
	Exchange        string   // GRID_EXCHANGE — биржа исполнения
	Symbols         []string // GRID_SYMBOLS — символы для торговли
	Grids           int      // GRID_GRIDS — количество уровней (5–50)
	LowerBoundPct   float64  // GRID_LOWER_BOUND_PCT — нижняя граница в % ниже текущей цены (если ATR range выключен)
	UpperBoundPct   float64  // GRID_UPPER_BOUND_PCT — верхняя граница в % выше текущей цены (если ATR range выключен)
	TotalUSDT       float64  // GRID_TOTAL_USDT — общий капитал на одну сетку
	StopLossPct     float64  // GRID_STOP_LOSS_PCT — % ниже LowerBound для аварийного закрытия
	TrailingUp      bool     // GRID_TRAILING_UP — автосдвиг сетки вверх при пробое UpperBound
	CooldownSec     int      // GRID_COOLDOWN_SEC — cooldown перед перезапуском сетки
	MinNotionalUSDT float64  // GRID_MIN_NOTIONAL_USDT — мин. объём ордера в USDT (Binance ≥5)

	// --- Adaptive Grid v2 ---
	UseATRRange       bool    // GRID_USE_ATR_RANGE — рассчитывать диапазон через ATR(24h, 1h)
	ATRMultiplier     float64 // GRID_ATR_MULTIPLIER — Lower=price-mult*ATR, Upper=price+mult*ATR
	ATRPeriodHours    int     // GRID_ATR_PERIOD_HOURS — кол-во часовых свечей для ATR (по умолчанию 24)
	UseADXFilter      bool    // GRID_USE_ADX_FILTER — pause при сильном тренде (ADX > порог)
	ADXPauseThreshold float64 // GRID_ADX_PAUSE_THRESHOLD — выше этого pause (типично 25)
	ADXResumeThreshold float64 // GRID_ADX_RESUME_THRESHOLD — ниже этого resume (типично 20)
	ADXCheckIntervalMin int    // GRID_ADX_CHECK_INTERVAL_MIN — интервал проверки тренда (мин)
	ADXPeriod          int     // GRID_ADX_PERIOD — период ADX (по умолчанию 14)
	AutoShiftDown      bool    // GRID_AUTO_SHIFT_DOWN — пересоздать grid при пробое нижней границы
	AutoShiftDownBufPct float64 // GRID_AUTO_SHIFT_DOWN_BUF_PCT — % ниже lower для срабатывания shift-down

	// --- Telegram-уведомления ---
	TGDigestIntervalMin int // GRID_TG_DIGEST_INTERVAL_MIN — периодичность TG-сводки sell-циклов (мин); 0 = отключить
}

// LoadConfig читает конфиг из env.
func LoadConfig() Config {
	symbolsRaw := sharedconfig.GetEnv("GRID_SYMBOLS", "BTCUSDT,ETHUSDT,SOLUSDT,BNBUSDT")
	var symbols []string
	for _, s := range strings.Split(symbolsRaw, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			symbols = append(symbols, s)
		}
	}

	grids := sharedconfig.GetEnvInt("GRID_GRIDS", 20)
	if grids < 5 || grids > 50 {
		grids = 20
	}

	return Config{
		Enabled:         sharedconfig.GetEnvBool("GRID_ENABLED", false),
		Exchange:        sharedconfig.GetEnv("GRID_EXCHANGE", "binance"),
		Symbols:         symbols,
		Grids:           grids,
		LowerBoundPct:   sharedconfig.GetEnvFloat("GRID_LOWER_BOUND_PCT", 5.0),
		UpperBoundPct:   sharedconfig.GetEnvFloat("GRID_UPPER_BOUND_PCT", 5.0),
		TotalUSDT:       sharedconfig.GetEnvFloat("GRID_TOTAL_USDT", 100.0),
		StopLossPct:     sharedconfig.GetEnvFloat("GRID_STOP_LOSS_PCT", 2.0),
		TrailingUp:      sharedconfig.GetEnvBool("GRID_TRAILING_UP", false),
		CooldownSec:     sharedconfig.GetEnvInt("GRID_COOLDOWN_SEC", 60),
		MinNotionalUSDT: sharedconfig.GetEnvFloat("GRID_MIN_NOTIONAL_USDT", 6.0),

		UseATRRange:         sharedconfig.GetEnvBool("GRID_USE_ATR_RANGE", false),
		ATRMultiplier:       sharedconfig.GetEnvFloat("GRID_ATR_MULTIPLIER", 1.5),
		ATRPeriodHours:      sharedconfig.GetEnvInt("GRID_ATR_PERIOD_HOURS", 24),
		UseADXFilter:        sharedconfig.GetEnvBool("GRID_USE_ADX_FILTER", false),
		ADXPauseThreshold:   sharedconfig.GetEnvFloat("GRID_ADX_PAUSE_THRESHOLD", 25),
		ADXResumeThreshold:  sharedconfig.GetEnvFloat("GRID_ADX_RESUME_THRESHOLD", 20),
		ADXCheckIntervalMin: sharedconfig.GetEnvInt("GRID_ADX_CHECK_INTERVAL_MIN", 15),
		ADXPeriod:           sharedconfig.GetEnvInt("GRID_ADX_PERIOD", 14),
		AutoShiftDown:       sharedconfig.GetEnvBool("GRID_AUTO_SHIFT_DOWN", false),
		AutoShiftDownBufPct: sharedconfig.GetEnvFloat("GRID_AUTO_SHIFT_DOWN_BUF_PCT", 0.5),

		TGDigestIntervalMin: sharedconfig.GetEnvInt("GRID_TG_DIGEST_INTERVAL_MIN", 30),
	}
}
