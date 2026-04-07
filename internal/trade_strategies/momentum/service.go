package momentum

import (
	"context"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/shared/detector"
	"github.com/osman/bot-traider/internal/ticker"
	"github.com/osman/bot-traider/internal/trade"
)

// Service реализует Momentum стратегию:
// вход по pump-событию, выход по crash/trailing stop/TP/timeout.
type Service struct {
	mu          sync.Mutex
	cfg         Config
	ctx         context.Context
	tradeSvc    *trade.Service
	tracker     *TradeTracker
	cooldowns   map[string]time.Time
	log         *zap.Logger
}

// New создаёт Service.
func New(ctx context.Context, cfg Config, tradeSvc *trade.Service, log *zap.Logger) *Service {
	log.Info("momentum strategy initialized",
		zap.String("signal_exchange", cfg.SignalExchange),
		zap.Float64("min_pump_pct", cfg.MinPumpPct),
		zap.Float64("trailing_stop_pct", cfg.TrailingStopPct),
		zap.Float64("take_profit_pct", cfg.TakeProfitPct),
		zap.Float64("stop_loss_pct", cfg.StopLossPct),
		zap.Int("cooldown_sec", cfg.CooldownSec),
		zap.Int("max_hold_sec", cfg.MaxHoldSec),
		zap.Int("max_positions", cfg.MaxPositions),
		zap.Float64("trade_amount_usdt", cfg.TradeAmountUSDT),
	)
	return &Service{
		cfg:       cfg,
		ctx:       ctx,
		tradeSvc:  tradeSvc,
		tracker:   NewTradeTracker(),
		cooldowns: make(map[string]time.Time),
		log:       log,
	}
}

// OnPumpEvent вызывается detector при обнаружении pump-события.
func (s *Service) OnPumpEvent(event *detector.DetectorEvent) {
	symbol := event.Symbol

	// 1. Фильтр: только сигналы с нужной биржи (пустая строка = любая биржа)
	if s.cfg.SignalExchange != "" && event.Exchange != s.cfg.SignalExchange {
		s.log.Debug("momentum: pump ignored — wrong exchange",
			zap.String("symbol", symbol),
			zap.String("event_exchange", event.Exchange),
			zap.String("expected", s.cfg.SignalExchange),
		)
		return
	}

	// 2. Фильтр: минимальный % пампа
	if event.ChangePct < s.cfg.MinPumpPct {
		s.log.Debug("momentum: pump ignored — change_pct below threshold",
			zap.String("symbol", symbol),
			zap.Float64("change_pct", event.ChangePct),
			zap.Float64("min_pump_pct", s.cfg.MinPumpPct),
		)
		return
	}

	// 3. Cooldown по символу
	s.mu.Lock()
	if last, ok := s.cooldowns[symbol]; ok {
		cooldownDur := time.Duration(s.cfg.CooldownSec) * time.Second
		if time.Since(last) < cooldownDur {
			remaining := cooldownDur - time.Since(last)
			s.mu.Unlock()
			s.log.Debug("momentum: cooldown active",
				zap.String("symbol", symbol),
				zap.Duration("remaining", remaining),
			)
			return
		}
	}
	s.mu.Unlock()

	// 4. Уже есть открытая сделка по символу
	if s.tracker.Has(symbol) {
		s.log.Debug("momentum: trade already open for symbol", zap.String("symbol", symbol))
		return
	}

	// 5. Лимит одновременных позиций
	if s.tracker.Count() >= s.cfg.MaxPositions {
		s.log.Warn("momentum: max positions limit reached",
			zap.Int("current", s.tracker.Count()),
			zap.Int("max", s.cfg.MaxPositions),
		)
		return
	}

	// 6. Расчёт qty
	entryPrice := event.PriceNow
	if entryPrice <= 0 {
		s.log.Warn("momentum: invalid entry price", zap.String("symbol", symbol), zap.Float64("price", entryPrice))
		return
	}
	qty := s.cfg.TradeAmountUSDT / entryPrice
	if qty <= 0 {
		s.log.Warn("momentum: calculated qty is zero", zap.String("symbol", symbol))
		return
	}

	// trade exchange = биржа где обнаружен памп
	tradeExchange := event.Exchange

	s.log.Info("momentum: pump signal accepted, opening trade",
		zap.String("symbol", symbol),
		zap.String("trade_exchange", tradeExchange),
		zap.Float64("change_pct", event.ChangePct),
		zap.Float64("entry_price", entryPrice),
		zap.Float64("qty", qty),
	)

	// 7. Открываем сделку
	id, err := s.tradeSvc.OpenTrade(s.ctx, trade.Trade{
		Strategy:       "momentum",
		SignalExchange: event.Exchange,
		TradeExchange:  tradeExchange,
		Symbol:         symbol,
		Qty:            qty,
		EntryPrice:     entryPrice,
	})
	if err != nil {
		s.log.Error("momentum: open trade failed",
			zap.String("symbol", symbol),
			zap.Error(err),
		)
		// ставим cooldown чтобы не ломиться повторно
		s.mu.Lock()
		s.cooldowns[symbol] = time.Now()
		s.mu.Unlock()
		return
	}

	// 8. Регистрируем сделку в трекере
	t := &MomentumTrade{
		ID:             id,
		Symbol:         symbol,
		TradeExchange:  tradeExchange,
		SignalExchange: event.Exchange,
		EntryPrice:     entryPrice,
		PeakPrice:      entryPrice,
		Qty:            qty,
		OpenedAt:       time.Now(),
		PriceCh:        make(chan float64, 64),
		CrashCh:        make(chan struct{}, 1),
	}
	s.tracker.Add(t)

	// 9. Запускаем горутину мониторинга
	go s.watchTrade(t)

	// 10. Обновляем cooldown
	s.mu.Lock()
	s.cooldowns[symbol] = time.Now()
	s.mu.Unlock()

}


// OnCrashEvent вызывается detector при обнаружении flash crash.
func (s *Service) OnCrashEvent(event *detector.DetectorEvent) {
	t, ok := s.tracker.Get(event.Symbol)
	if !ok {
		return
	}

	// Crash должен быть на той же бирже где куплен актив
	if event.Exchange != t.TradeExchange {
		return
	}

	s.log.Warn("momentum: crash signal received, sending exit to watchTrade",
		zap.String("symbol", event.Symbol),
		zap.String("exchange", event.Exchange),
		zap.Float64("change_pct", event.ChangePct),
	)

	// Неблокирующий send
	select {
	case t.CrashCh <- struct{}{}:
	default:
	}
}

// OnTicker получает тикеры и направляет цену в активные сделки.
func (s *Service) OnTicker(t ticker.Ticker) {
	trade, ok := s.tracker.Get(t.Symbol)
	if !ok {
		return
	}

	// Следим за ценой только на бирже где куплен актив
	if t.Exchange != trade.TradeExchange {
		return
	}

	price, err := strconv.ParseFloat(t.Price, 64)
	if err != nil || price <= 0 {
		return
	}

	select {
	case trade.PriceCh <- price:
	default:
		s.log.Warn("momentum: price channel full, tick dropped",
			zap.String("symbol", t.Symbol),
		)
	}
}

// watchTrade мониторит сделку до TP/SL/trailing/crash/timeout.
func (s *Service) watchTrade(t *MomentumTrade) {
	timeout := time.NewTimer(time.Duration(s.cfg.MaxHoldSec) * time.Second)
	defer timeout.Stop()

	trailingStop := t.EntryPrice * (1 - s.cfg.TrailingStopPct/100)
	hardSL := t.EntryPrice * (1 - s.cfg.StopLossPct/100)
	hardTPEnabled := s.cfg.TakeProfitPct > 0
	hardTP := t.EntryPrice * (1 + s.cfg.TakeProfitPct/100)

	s.log.Info("momentum: watching trade",
		zap.Int64("id", t.ID),
		zap.String("symbol", t.Symbol),
		zap.Float64("entry_price", t.EntryPrice),
		zap.Bool("hard_tp_enabled", hardTPEnabled),
		zap.Float64("hard_tp", hardTP),
		zap.Float64("hard_sl", hardSL),
		zap.Float64("trailing_stop_initial", trailingStop),
		zap.Int("max_hold_sec", s.cfg.MaxHoldSec),
	)

	for {
		select {
		case <-s.ctx.Done():
			s.log.Info("momentum: context cancelled, closing trade",
				zap.Int64("id", t.ID),
				zap.String("symbol", t.Symbol),
			)
			return

		case <-timeout.C:
			holdSec := int(time.Since(t.OpenedAt).Seconds())
			s.log.Warn("momentum: timeout, force closing",
				zap.Int64("id", t.ID),
				zap.String("symbol", t.Symbol),
				zap.Int("hold_sec", holdSec),
			)
			s.closeTrade(t, t.PeakPrice, "timeout")
			return

		case <-t.CrashCh:
			s.log.Warn("momentum: crash exit triggered",
				zap.Int64("id", t.ID),
				zap.String("symbol", t.Symbol),
				zap.Float64("peak_price", t.PeakPrice),
			)
			s.closeTrade(t, t.PeakPrice, "crash")
			return

		case price := <-t.PriceCh:
			// Обновляем пиковую цену и trailing stop
			if price > t.PeakPrice {
				t.PeakPrice = price
				trailingStop = t.PeakPrice * (1 - s.cfg.TrailingStopPct/100)
				s.log.Debug("momentum: new peak price",
					zap.Int64("id", t.ID),
					zap.String("symbol", t.Symbol),
					zap.Float64("peak_price", t.PeakPrice),
					zap.Float64("trailing_stop", trailingStop),
				)
			}

			// Hard SL
			if price <= hardSL {
				s.log.Warn("momentum: hard SL hit",
					zap.Int64("id", t.ID),
					zap.String("symbol", t.Symbol),
					zap.Float64("price", price),
					zap.Float64("hard_sl", hardSL),
				)
				s.closeTrade(t, price, "sl")
				return
			}

			// Trailing stop
			if price <= trailingStop {
				s.log.Info("momentum: trailing stop hit",
					zap.Int64("id", t.ID),
					zap.String("symbol", t.Symbol),
					zap.Float64("price", price),
					zap.Float64("trailing_stop", trailingStop),
					zap.Float64("peak_price", t.PeakPrice),
				)
				s.closeTrade(t, price, "trailing")
				return
			}

			// Hard TP (пропускается если MOMENTUM_TAKE_PROFIT_PCT=0 — выход только при падении)
			if hardTPEnabled && price >= hardTP {
				s.log.Info("momentum: hard TP hit",
					zap.Int64("id", t.ID),
					zap.String("symbol", t.Symbol),
					zap.Float64("price", price),
					zap.Float64("hard_tp", hardTP),
				)
				s.closeTrade(t, price, "tp")
				return
			}
		}
	}
}

// closeTrade закрывает сделку и фиксирует результат.
func (s *Service) closeTrade(t *MomentumTrade, exitPrice float64, reason string) {
	defer s.tracker.Remove(t.Symbol)

	err := s.tradeSvc.CloseTrade(s.ctx, t.ID, exitPrice, reason)
	if err != nil {
		s.log.Error("momentum: close trade failed",
			zap.Int64("id", t.ID),
			zap.String("symbol", t.Symbol),
			zap.String("reason", reason),
			zap.Error(err),
		)
		// сделку всё равно убираем из трекера (defer выше)
		return
	}

	pnl := (exitPrice - t.EntryPrice) * t.Qty
	holdSec := int(time.Since(t.OpenedAt).Seconds())

	s.log.Info("momentum: trade closed",
		zap.Int64("id", t.ID),
		zap.String("symbol", t.Symbol),
		zap.String("reason", reason),
		zap.Float64("entry_price", t.EntryPrice),
		zap.Float64("exit_price", exitPrice),
		zap.Float64("peak_price", t.PeakPrice),
		zap.Float64("pnl_usdt", pnl),
		zap.Int("hold_sec", holdSec),
	)

}
