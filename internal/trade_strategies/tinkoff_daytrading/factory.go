package tinkoff_daytrading

import (
	"context"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/shared/telegram"
	"github.com/osman/bot-traider/internal/trade"
)

// New создаёт сервис дейтрейдинга если TINKOFF_DAYTRADING_ENABLED=true.
// Возвращает nil если стратегия отключена.
//
// Колбэки OnTrade и OnOrderBook необходимо зарегистрировать на стриминговом
// клиенте Тинькофф в main.go после вызова New.
func New(
	ctx context.Context,
	tradeSvc *trade.Service,
	initialSymbols []string,
	tgNotifier *telegram.Notifier,
	tradesThreadID int,
	log *zap.Logger,
) *Service {
	cfg := LoadConfig()
	if !cfg.Enabled {
		log.Info("tinkoff daytrading: disabled (TINKOFF_DAYTRADING_ENABLED=false)")
		return nil
	}

	svc := newService(ctx, cfg, tradeSvc, tgNotifier, tradesThreadID, log)

	// Зарегистрировать начальный список символов
	if len(initialSymbols) > 0 {
		svc.OnSymbolsChanged(initialSymbols, nil)
	}

	log.Info("tinkoff daytrading: started",
		zap.Int("initial_symbols", len(initialSymbols)),
		zap.Float64("lot_limit", cfg.LotLimit),
	)
	return svc
}
