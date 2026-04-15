package kucoin

import (
	"context"
	"sync"

	exchange_orders "github.com/osman/bot-traider/internal/exchange_orders"
	"go.uber.org/zap"
)

// TradeService управляет WS-подписками на сделки KuCoin (/market/match) для набора символов.
// Аналог exchange_orders.Service, но для KuCoin.
// Предоставляет поток торговых событий для microscalping CVD/whale метрик.
type TradeService struct {
	fetcher *TradeFetcher
	log     *zap.Logger
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	rootCtx context.Context
}

// NewTradeService создаёт TradeService.
func NewTradeService(rest *RestClient, log *zap.Logger) *TradeService {
	return &TradeService{
		fetcher: NewTradeFetcher(rest, log),
		log:     log,
		cancels: make(map[string]context.CancelFunc),
	}
}

// WithOnTrade устанавливает хук, вызываемый при каждой входящей сделке.
// Должен быть вызван до Start().
func (s *TradeService) WithOnTrade(fn func(exchange_orders.ExchangeOrder)) {
	s.fetcher.WithOnTrade(fn)
}

// Start запускает подписки для начального списка символов.
func (s *TradeService) Start(ctx context.Context, symbols []string) {
	s.rootCtx = ctx
	for _, sym := range symbols {
		s.startSymbol(sym)
	}
	s.log.Info("kucoin trade: started subscriptions", zap.Int("symbols", len(symbols)), zap.Strings("list", symbols))
}

// OnSymbolsChanged реагирует на изменение топ-листа: запускает новые и останавливает выбывшие.
func (s *TradeService) OnSymbolsChanged(added, removed []string) {
	for _, sym := range removed {
		s.stopSymbol(sym)
	}
	for _, sym := range added {
		s.startSymbol(sym)
	}
}

func (s *TradeService) startSymbol(symbol string) {
	s.mu.Lock()
	if _, exists := s.cancels[symbol]; exists {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(s.rootCtx)
	s.cancels[symbol] = cancel
	s.mu.Unlock()

	s.log.Info("kucoin trade: subscribing", zap.String("symbol", symbol))
	go s.fetcher.Subscribe(ctx, symbol)
}

func (s *TradeService) stopSymbol(symbol string) {
	s.mu.Lock()
	cancel, ok := s.cancels[symbol]
	if ok {
		delete(s.cancels, symbol)
	}
	s.mu.Unlock()
	if ok {
		cancel()
		s.log.Info("kucoin trade: unsubscribed", zap.String("symbol", symbol))
	}
}
