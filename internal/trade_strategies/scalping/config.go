package scalping

import (
	"strings"

	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
)

// Config хранит параметры Scalping стратегии.
type Config struct {
	Enabled              bool     // SCALPING_ENABLED=false
	Symbols              []string // SCALPING_SYMBOLS=BTCUSDT,ETHUSDT,SOLUSDT,BNBUSDT
	Exchange             string   // SCALPING_EXCHANGE=binance
	Interval             string   // SCALPING_INTERVAL=1m
	TradeAmountPct       float64  // SCALPING_TRADE_AMOUNT_PCT=1.5 (% от баланса USDT)
	TakeProfitPctBTC     float64  // SCALPING_TP_PCT_BTC=0.4  (BTC, BNB)
	TakeProfitPctAlt     float64  // SCALPING_TP_PCT_ALT=0.5  (ETH, SOL и остальные)
	StopLossPct          float64  // SCALPING_SL_PCT=0.55
	TrailingActivatePct  float64  // SCALPING_TRAILING_ACTIVATE_PCT=0.3
	TrailingStepPct      float64  // SCALPING_TRAILING_STEP_PCT=0.15
	LimitOrderTimeoutSec int      // SCALPING_LIMIT_TIMEOUT_SEC=3
	MaxSlippagePct       float64  // SCALPING_MAX_SLIPPAGE_PCT=0.05
	RSIPeriod            int      // SCALPING_RSI_PERIOD=7
	RSIOversold          float64  // SCALPING_RSI_OVERSOLD=35
	EMAPeriod            int      // SCALPING_EMA_PERIOD=21
	BBPeriod             int      // SCALPING_BB_PERIOD=20
	BBStdDev             float64  // SCALPING_BB_STDDEV=2.0
	ATRPeriod            int      // SCALPING_ATR_PERIOD=14
	ATRWindowHours       int      // SCALPING_ATR_WINDOW_HOURS=24
	DailyLossCapPct      float64  // SCALPING_DAILY_LOSS_CAP_PCT=5.0
	CooldownSec          int      // SCALPING_COOLDOWN_SEC=60
	CandleBufferSize     int      // SCALPING_CANDLE_BUFFER=100
	MACDEnabled          bool     // SCALPING_MACD_ENABLED=false
}

// LoadConfig читает конфиг из env.
func LoadConfig() Config {
	symbolsRaw := sharedconfig.GetEnv("SCALPING_SYMBOLS", "BTCUSDT,ETHUSDT,SOLUSDT,BNBUSDT")
	var symbols []string
	for _, s := range strings.Split(symbolsRaw, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			symbols = append(symbols, s)
		}
	}

	return Config{
		Enabled:             sharedconfig.GetEnvBool("SCALPING_ENABLED", false),
		Symbols:             symbols,
		Exchange:            sharedconfig.GetEnv("SCALPING_EXCHANGE", "binance"),
		Interval:            sharedconfig.GetEnv("SCALPING_INTERVAL", "1m"),
		TradeAmountPct:      sharedconfig.GetEnvFloat("SCALPING_TRADE_AMOUNT_PCT", 1.5),
		TakeProfitPctBTC:    sharedconfig.GetEnvFloat("SCALPING_TP_PCT_BTC", 0.4),
		TakeProfitPctAlt:    sharedconfig.GetEnvFloat("SCALPING_TP_PCT_ALT", 0.5),
		StopLossPct:         sharedconfig.GetEnvFloat("SCALPING_SL_PCT", 0.55),
		TrailingActivatePct: sharedconfig.GetEnvFloat("SCALPING_TRAILING_ACTIVATE_PCT", 0.3),
		TrailingStepPct:     sharedconfig.GetEnvFloat("SCALPING_TRAILING_STEP_PCT", 0.15),
		LimitOrderTimeoutSec: sharedconfig.GetEnvInt("SCALPING_LIMIT_TIMEOUT_SEC", 3),
		MaxSlippagePct:      sharedconfig.GetEnvFloat("SCALPING_MAX_SLIPPAGE_PCT", 0.05),
		RSIPeriod:           sharedconfig.GetEnvInt("SCALPING_RSI_PERIOD", 7),
		RSIOversold:         sharedconfig.GetEnvFloat("SCALPING_RSI_OVERSOLD", 35),
		EMAPeriod:           sharedconfig.GetEnvInt("SCALPING_EMA_PERIOD", 21),
		BBPeriod:            sharedconfig.GetEnvInt("SCALPING_BB_PERIOD", 20),
		BBStdDev:            sharedconfig.GetEnvFloat("SCALPING_BB_STDDEV", 2.0),
		ATRPeriod:           sharedconfig.GetEnvInt("SCALPING_ATR_PERIOD", 14),
		ATRWindowHours:      sharedconfig.GetEnvInt("SCALPING_ATR_WINDOW_HOURS", 24),
		DailyLossCapPct:     sharedconfig.GetEnvFloat("SCALPING_DAILY_LOSS_CAP_PCT", 5.0),
		CooldownSec:         sharedconfig.GetEnvInt("SCALPING_COOLDOWN_SEC", 60),
		CandleBufferSize:    sharedconfig.GetEnvInt("SCALPING_CANDLE_BUFFER", 100),
		MACDEnabled:         sharedconfig.GetEnvBool("SCALPING_MACD_ENABLED", false),
	}
}
