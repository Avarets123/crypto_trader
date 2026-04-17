package tinkoff

// VolatileTicker — инструмент с информацией о волатильности.
type VolatileTicker struct {
	Symbol         string  // тикер (SBER, GAZP, AAPL)
	UID            string  // instrument UID для gRPC подписок
	Currency       string  // валюта котирования (rub, usd)
	PriceChangePct float64 // % изменение цены за торговую сессию
}
