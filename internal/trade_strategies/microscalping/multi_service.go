package microscalping

import (
	exchange_orders "github.com/osman/bot-traider/internal/exchange_orders"
	"github.com/osman/bot-traider/internal/shared/detector"
	"github.com/osman/bot-traider/internal/ticker"
)

// MultiService управляет несколькими экземплярами Service — по одному на каждую биржу.
// Маршрутизирует события (OnTrade, OnTicker, OnCrashEvent, OnSymbolsChanged) к нужному экземпляру.
type MultiService struct {
	byExchange map[string]*Service
}

func newMultiService(byExchange map[string]*Service) *MultiService {
	return &MultiService{byExchange: byExchange}
}

// OnTrade маршрутизирует торговое событие к Service нужной биржи.
func (m *MultiService) OnTrade(o exchange_orders.ExchangeOrder) {
	if svc, ok := m.byExchange[o.Exchange]; ok {
		svc.OnTrade(o)
	}
}

// OnTicker маршрутизирует тикер к Service нужной биржи.
func (m *MultiService) OnTicker(t ticker.Ticker) {
	if svc, ok := m.byExchange[t.Exchange]; ok {
		svc.OnTicker(t)
	}
}

// OnCrashEvent маршрутизирует crash-событие к Service нужной биржи.
func (m *MultiService) OnCrashEvent(event *detector.DetectorEvent) {
	if svc, ok := m.byExchange[event.Exchange]; ok {
		svc.OnCrashEvent(event)
	}
}

// OnExchangeSymbolsChanged обновляет список символов для конкретной биржи.
// Вызывается из хуков topProvider/kucoinTopProvider.WithOnSymbolsChanged.
func (m *MultiService) OnExchangeSymbolsChanged(exchange string, added, removed []string) {
	if svc, ok := m.byExchange[exchange]; ok {
		svc.OnSymbolsChanged(added, removed)
	}
}

// OnSymbolsChanged обновляет символы во всех биржах (для обратной совместимости).
func (m *MultiService) OnSymbolsChanged(added, removed []string) {
	for _, svc := range m.byExchange {
		svc.OnSymbolsChanged(added, removed)
	}
}

// SetExchangeSymbols устанавливает начальный список символов для биржи.
// Используется при поздней инициализации (например, KuCoin создаётся после microscalping).
func (m *MultiService) SetExchangeSymbols(exchange string, symbols []string) {
	if svc, ok := m.byExchange[exchange]; ok {
		svc.SetSymbols(symbols)
	}
}
