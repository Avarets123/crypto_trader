package kucoin

import (
	"context"
	"sync"
	"time"

	"github.com/osman/bot-traider/internal/orderbook"
	"go.uber.org/zap"
)

// initDelay — задержка между запусками горутин, чтобы не перегружать REST KuCoin.
const kucoinObInitDelay = 300 * time.Millisecond

// OrderBookService управляет WS-подписками /market/level2 для набора символов.
// Пишет стаканы в общий orderbook.Store — они доступны через obSvc.GetBook(symbol).
type OrderBookService struct {
	fetcher *OrderBookFetcher
	store   *orderbook.Store
	log     *zap.Logger
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	rootCtx context.Context
}

// NewOrderBookService создаёт OrderBookService.
// store — тот же, что используется Binance orderbook.Service, чтобы GetBook работал для обеих бирж.
func NewOrderBookService(rest *RestClient, store *orderbook.Store, log *zap.Logger) *OrderBookService {
	return &OrderBookService{
		fetcher: NewOrderBookFetcher(rest, log),
		store:   store,
		log:     log,
		cancels: make(map[string]context.CancelFunc),
	}
}

// Init запускает diff-стримы для начального списка символов.
func (s *OrderBookService) Init(ctx context.Context, symbols []string) {
	s.rootCtx = ctx
	for i, sym := range symbols {
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(kucoinObInitDelay):
			}
		}
		s.startSymbol(sym)
	}
	s.log.Info("kucoin orderbook: started", zap.Int("symbols", len(symbols)), zap.Strings("list", symbols))
}

// OnSymbolsChanged реагирует на изменение топ-листа KuCoin.
func (s *OrderBookService) OnSymbolsChanged(added, removed []string) {
	for _, sym := range removed {
		s.stopSymbol(sym)
	}
	for i, sym := range added {
		if i > 0 {
			select {
			case <-s.rootCtx.Done():
				return
			case <-time.After(kucoinObInitDelay):
			}
		}
		s.startSymbol(sym)
	}
}

func (s *OrderBookService) startSymbol(symbol string) {
	s.mu.Lock()
	if _, exists := s.cancels[symbol]; exists {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(s.rootCtx)
	s.cancels[symbol] = cancel
	s.mu.Unlock()

	s.log.Info("kucoin orderbook: subscribing", zap.String("symbol", symbol))
	go s.fetcher.SubscribeDiff(ctx, symbol, s.store)
}

func (s *OrderBookService) stopSymbol(symbol string) {
	s.mu.Lock()
	cancel, ok := s.cancels[symbol]
	if ok {
		delete(s.cancels, symbol)
	}
	s.mu.Unlock()
	if ok {
		cancel()
		s.store.Remove(symbol)
		s.log.Info("kucoin orderbook: unsubscribed", zap.String("symbol", symbol))
	}
}
