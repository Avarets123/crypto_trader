package orderbook

import (
	"context"
	"strings"
	"sync"

	"go.uber.org/zap"

	exchange_orders "github.com/osman/bot-traider/internal/exchange_orders"
	"github.com/osman/bot-traider/internal/shared/telegram"
)

// AlertService отслеживает изменения топ-листа символов и уведомляет в Telegram.
type AlertService struct {
	mu       sync.Mutex
	symbols  []string
	bookSvc  *Service
	notifier *telegram.Notifier
	threadID int
	cfg      AlertsConfig
	log      *zap.Logger
	tradeAgg *exchange_orders.TradeAggregator
}

// NewAlertService создаёт AlertService.
func NewAlertService(
	bookSvc *Service,
	notifier *telegram.Notifier,
	threadID int,
	cfg AlertsConfig,
	log *zap.Logger,
) *AlertService {
	return &AlertService{
		bookSvc:  bookSvc,
		notifier: notifier,
		threadID: threadID,
		cfg:      cfg,
		log:      log,
	}
}

// WithTradeAggregator подключает агрегатор сделок (используется для сброса при смене символов).
func (s *AlertService) WithTradeAggregator(agg *exchange_orders.TradeAggregator) {
	s.tradeAgg = agg
}

// SetSymbols задаёт начальный список символов для мониторинга.
func (s *AlertService) SetSymbols(symbols []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.symbols = make([]string, len(symbols))
	copy(s.symbols, symbols)
}

// Start блокирует до ctx.Done().
func (s *AlertService) Start(ctx context.Context) {
	s.log.Info("orderbook alerts: started", zap.Strings("symbols", s.symbols))
	<-ctx.Done()
}

// OnSymbolsChanged вызывается TopVolatileProvider при изменении топ-листа.
// Обновляет внутренний список символов и отправляет уведомление в Telegram.
func (s *AlertService) OnSymbolsChanged(ctx context.Context, added, removed []string) {
	if len(added) == 0 && len(removed) == 0 {
		return
	}

	s.mu.Lock()
	newSymbols := make([]string, 0, len(s.symbols)+len(added))
	newSymbols = append(newSymbols, s.symbols...)

	if len(removed) > 0 {
		removedSet := make(map[string]struct{}, len(removed))
		for _, sym := range removed {
			removedSet[sym] = struct{}{}
		}
		filtered := newSymbols[:0]
		for _, sym := range newSymbols {
			if _, ok := removedSet[sym]; !ok {
				filtered = append(filtered, sym)
			}
		}
		newSymbols = filtered
		for _, sym := range removed {
			if s.tradeAgg != nil {
				s.tradeAgg.Reset(sym)
			}
		}
	}

	newSymbols = append(newSymbols, added...)
	s.symbols = newSymbols
	s.mu.Unlock()

	s.log.Info("orderbook alerts: symbol list updated",
		zap.Strings("added", added),
		zap.Strings("removed", removed),
	)

	msg := formatSymbolsUpdate(added, removed)
	s.notifier.SendToThread(ctx, msg, s.threadID)
}

func formatSymbolsUpdate(added, removed []string) string {
	msg := "🔄 <b>Топ волатильных монет обновлён</b>\n"
	if len(added) > 0 {
		msg += "\n➕ Добавлены:  " + strings.Join(added, ", ")
	}
	if len(removed) > 0 {
		msg += "\n➖ Убраны:     " + strings.Join(removed, ", ")
	}
	return msg
}

