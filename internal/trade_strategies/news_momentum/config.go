package news_momentum

import sharedconfig "github.com/osman/bot-traider/internal/shared/config"

// Config — параметры news-momentum стратегии.
type Config struct {
	Enabled  bool   // NEWS_MOMENTUM_ENABLED — включить стратегию
	Exchange string // NEWS_MOMENTUM_EXCHANGE — биржа исполнения (binance/kucoin)

	TradeAmountUSDT float64 // NEWS_MOMENTUM_TRADE_AMOUNT_USDT — размер одной сделки
	MaxPositions    int     // NEWS_MOMENTUM_MAX_POSITIONS — максимум одновременных позиций

	TPPct              float64 // NEWS_MOMENTUM_TP_PCT — take-profit, %
	SLPct              float64 // NEWS_MOMENTUM_SL_PCT — stop-loss, %
	TrailActivationPct float64 // NEWS_MOMENTUM_TRAIL_ACTIVATION_PCT — активация trailing при росте, %
	TrailPct           float64 // NEWS_MOMENTUM_TRAIL_PCT — отступ trailing от пика, %
	TimeoutMin         int     // NEWS_MOMENTUM_TIMEOUT_MIN — принудительное закрытие через N минут

	MaxNewsAgeMin       int     // NEWS_MOMENTUM_MAX_NEWS_AGE_MIN — игнорировать новости старше N минут
	MaxPriceGain1HPct   float64 // NEWS_MOMENTUM_MAX_PRICE_GAIN_1H_PCT — anti-frontrun: не входить если цена уже выросла > X% за час
	CooldownPerSymbolH  int     // NEWS_MOMENTUM_COOLDOWN_PER_SYMBOL_H — cooldown между сделками по одному символу
	RequireHighConfidence bool  // NEWS_MOMENTUM_REQUIRE_HIGH_CONFIDENCE — торговать только high-confidence сигналы

	Tier1Sources     []string // NEWS_MOMENTUM_TIER1_SOURCES — источники, сигналы из которых торгуем
	WhitelistSymbols []string // NEWS_MOMENTUM_WHITELIST_SYMBOLS — какие символы вообще разрешено торговать
}

// LoadConfig читает параметры из ENV.
func LoadConfig() Config {
	tier1 := sharedconfig.GetEnvStringSlice("NEWS_MOMENTUM_TIER1_SOURCES")
	if len(tier1) == 0 {
		tier1 = []string{"coindesk", "cointelegraph", "decrypt", "theblock", "binance"}
	}
	whitelist := sharedconfig.GetEnvStringSlice("NEWS_MOMENTUM_WHITELIST_SYMBOLS")
	if len(whitelist) == 0 {
		whitelist = []string{
			"BTCUSDT", "ETHUSDT", "SOLUSDT", "BNBUSDT", "XRPUSDT", "DOGEUSDT",
			"AVAXUSDT", "LINKUSDT", "DOTUSDT", "MATICUSDT", "ADAUSDT", "LTCUSDT",
		}
	}
	return Config{
		Enabled:               sharedconfig.GetEnvBool("NEWS_MOMENTUM_ENABLED", false),
		Exchange:              sharedconfig.GetEnv("NEWS_MOMENTUM_EXCHANGE", "binance"),
		TradeAmountUSDT:       sharedconfig.GetEnvFloat("NEWS_MOMENTUM_TRADE_AMOUNT_USDT", 30),
		MaxPositions:          sharedconfig.GetEnvInt("NEWS_MOMENTUM_MAX_POSITIONS", 3),
		TPPct:                 sharedconfig.GetEnvFloat("NEWS_MOMENTUM_TP_PCT", 2.5),
		SLPct:                 sharedconfig.GetEnvFloat("NEWS_MOMENTUM_SL_PCT", 1.5),
		TrailActivationPct:    sharedconfig.GetEnvFloat("NEWS_MOMENTUM_TRAIL_ACTIVATION_PCT", 1.0),
		TrailPct:              sharedconfig.GetEnvFloat("NEWS_MOMENTUM_TRAIL_PCT", 0.5),
		TimeoutMin:            sharedconfig.GetEnvInt("NEWS_MOMENTUM_TIMEOUT_MIN", 30),
		MaxNewsAgeMin:         sharedconfig.GetEnvInt("NEWS_MOMENTUM_MAX_NEWS_AGE_MIN", 10),
		MaxPriceGain1HPct:     sharedconfig.GetEnvFloat("NEWS_MOMENTUM_MAX_PRICE_GAIN_1H_PCT", 5.0),
		CooldownPerSymbolH:    sharedconfig.GetEnvInt("NEWS_MOMENTUM_COOLDOWN_PER_SYMBOL_H", 4),
		RequireHighConfidence: sharedconfig.GetEnvBool("NEWS_MOMENTUM_REQUIRE_HIGH_CONFIDENCE", true),
		Tier1Sources:          tier1,
		WhitelistSymbols:      whitelist,
	}
}
