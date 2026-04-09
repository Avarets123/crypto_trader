package grid

import (
	"strings"

	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
)

// Config хранит параметры Grid стратегии.
type Config struct {
	Enabled          bool     // GRID_ENABLED=false
	Exchange         string   // GRID_EXCHANGE=binance
	Symbols          []string // GRID_SYMBOLS=BTCUSDT,ETHUSDT,SOLUSDT,BNBUSDT
	Grids            int      // GRID_GRIDS=20 (количество уровней, 10–50)
	LowerBoundPct    float64  // GRID_LOWER_BOUND_PCT=5.0 (% ниже текущей цены)
	UpperBoundPct    float64  // GRID_UPPER_BOUND_PCT=5.0 (% выше текущей цены)
	TotalUSDT        float64  // GRID_TOTAL_USDT=100.0 (общий капитал на сетку)
	StopLossPct      float64  // GRID_STOP_LOSS_PCT=2.0 (% ниже LowerBound для аварийного закрытия)
	TrailingUp       bool     // GRID_TRAILING_UP=false (автосдвиг сетки вверх при пробое UpperBound)
	SlippageLimitPct float64  // GRID_SLIPPAGE_LIMIT_PCT=0.1 (макс. проскальзывание)
	CooldownSec      int      // GRID_COOLDOWN_SEC=60 (cooldown перед перезапуском сетки)
	MinNotionalUSDT  float64  // GRID_MIN_NOTIONAL_USDT=6.0 (мин. объём ордера в USDT, Binance требует ≥5)
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
		Enabled:          sharedconfig.GetEnvBool("GRID_ENABLED", false),
		Exchange:         sharedconfig.GetEnv("GRID_EXCHANGE", "binance"),
		Symbols:          symbols,
		Grids:            grids,
		LowerBoundPct:    sharedconfig.GetEnvFloat("GRID_LOWER_BOUND_PCT", 5.0),
		UpperBoundPct:    sharedconfig.GetEnvFloat("GRID_UPPER_BOUND_PCT", 5.0),
		TotalUSDT:        sharedconfig.GetEnvFloat("GRID_TOTAL_USDT", 100.0),
		StopLossPct:      sharedconfig.GetEnvFloat("GRID_STOP_LOSS_PCT", 2.0),
		TrailingUp:       sharedconfig.GetEnvBool("GRID_TRAILING_UP", false),
		SlippageLimitPct: sharedconfig.GetEnvFloat("GRID_SLIPPAGE_LIMIT_PCT", 0.1),
		CooldownSec:      sharedconfig.GetEnvInt("GRID_COOLDOWN_SEC", 60),
		MinNotionalUSDT:  sharedconfig.GetEnvFloat("GRID_MIN_NOTIONAL_USDT", 6.0),
	}
}
