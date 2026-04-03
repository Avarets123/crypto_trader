package ticker

import "time"

// Ticker — унифицированная модель тикера для всех бирж.
type Ticker struct {
	Exchange  string // биржа-источник (binance, gateio, bybit)
	Symbol    string // торговая пара (BTCUSDT / BTC_USDT)
	Quote     string // котируемая валюта (USDT, BTC)
	Price     string // последняя цена
	Open24h   string // цена открытия 24ч назад
	High24h   string // максимум за 24ч
	Low24h    string // минимум за 24ч
	Volume24h string // объём в базовой валюте за 24ч
	ChangePct string // изменение цены в % (+1.23% / -0.45%)
	CreatedAt time.Time
}
