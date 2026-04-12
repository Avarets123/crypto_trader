package volatile

import (
	"context"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"

	exchange_orders "github.com/osman/bot-traider/internal/exchange_orders"
	"github.com/osman/bot-traider/internal/orderbook"
	"github.com/osman/bot-traider/internal/shared/detector"
	"github.com/osman/bot-traider/internal/ticker"
	"github.com/osman/bot-traider/internal/trade"
)

// obiDepth — количество уровней стакана для расчёта OBI.
const obiDepth = 50

// baselineRefreshSec — интервал обновления baseline (сек).
// После этого времени baseline перезаписывается текущим снимком.
const baselineRefreshSec = 300

// snapshot — снимок стакана в момент подписки на символ (baseline).
type snapshot struct {
	bidVol    float64
	askVol    float64
	obi       float64
	takenAt   time.Time
}

// Service реализует Volatile стратегию:
// вход по BullScore рассчитанному из стакана, выход по crash/trailing stop/TP/timeout.
// Имеет собственный цикл проверки независимый от AlertService.
type Service struct {
	mu          sync.Mutex
	cfg         Config
	ctx         context.Context
	tradeSvc    *trade.Service
	bookSvc     *orderbook.Service
	tradeAgg    *exchange_orders.TradeAggregator
	tracker     *TradeTracker
	cooldowns   map[string]time.Time
	symbols     []string
	baselines   map[string]snapshot
	warmupUntil time.Time
	log         *zap.Logger
}

// New создаёт Service.
func New(ctx context.Context, cfg Config, tradeSvc *trade.Service, bookSvc *orderbook.Service, log *zap.Logger) *Service {
	log.Info("volatile strategy initialized",
		zap.String("exchange", cfg.Exchange),
		zap.Float64("bull_score_min", cfg.BullScoreMin),
		zap.Int("check_interval_sec", cfg.CheckIntervalSec),
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
		bookSvc:   bookSvc,
		tracker:   NewTradeTracker(),
		cooldowns: make(map[string]time.Time),
		symbols:   nil,
		baselines: make(map[string]snapshot),
		log:       log,
	}
}

// WithTradeAggregator подключает агрегатор сделок для расчёта TFI/VolImb/AggrRatio.
// Опционально: без агрегатора сигнал основывается только на данных стакана.
func (s *Service) WithTradeAggregator(agg *exchange_orders.TradeAggregator) {
	s.tradeAgg = agg
}

// SetSymbols задаёт начальный список с��мволов для мониторинга.
func (s *Service) SetSymbols(symbols []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.symbols = make([]string, len(symbols))
	copy(s.symbols, symbols)
}

// OnSymbolsChanged реагирует на изменение топ-листа.
func (s *Service) OnSymbolsChanged(added, removed []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(removed) > 0 {
		removedSet := make(map[string]struct{}, len(removed))
		for _, sym := range removed {
			removedSet[sym] = struct{}{}
			delete(s.baselines, sym)
		}
		filtered := s.symbols[:0]
		for _, sym := range s.symbols {
			if _, ok := removedSet[sym]; !ok {
				filtered = append(filtered, sym)
			}
		}
		s.symbols = filtered
	}

	s.symbols = append(s.symbols, added...)
}

// Start запускает собственный цикл проверки сигналов. Блокирует до ctx.Done().
func (s *Service) Start(ctx context.Context) {
	warmup := time.Duration(s.cfg.WarmupSec) * time.Second
	s.warmupUntil = time.Now().Add(warmup)
	s.log.Info("volatile: warmup period started",
		zap.Duration("duration", warmup),
		zap.Time("ready_at", s.warmupUntil),
	)

	checkTicker := time.NewTicker(time.Duration(s.cfg.CheckIntervalSec) * time.Second)
	defer checkTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-checkTicker.C:
			s.checkSignals()
		}
	}
}

// checkSignals вычисляет BullScore для каждого символа и открывает позицию при сигнале.
func (s *Service) checkSignals() {
	s.mu.Lock()
	symbols := make([]string, len(s.symbols))
	copy(symbols, s.symbols)
	s.mu.Unlock()

	for _, sym := range symbols {
		ob, ok := s.bookSvc.GetBook(sym)
		if !ok {
			continue
		}

		// Объём топ-50 уровней стакана в USDT.
		var bidVol, askVol float64
		for i, e := range ob.Bids {
			if i >= obiDepth {
				break
			}
			p, _ := strconv.ParseFloat(e.Price, 64)
			q, _ := strconv.ParseFloat(e.Qty, 64)
			bidVol += p * q
		}
		for i, e := range ob.Asks {
			if i >= obiDepth {
				break
			}
			p, _ := strconv.ParseFloat(e.Price, 64)
			q, _ := strconv.ParseFloat(e.Qty, 64)
			askVol += p * q
		}

		// Mid-price.
		mid := 0.0
		if len(ob.Bids) > 0 && len(ob.Asks) > 0 {
			bp, _ := strconv.ParseFloat(ob.Bids[0].Price, 64)
			ap, _ := strconv.ParseFloat(ob.Asks[0].Price, 64)
			mid = (bp + ap) / 2
		}

		obi := orderbook.CalcOBI(bidVol, askVol)
		now := time.Now()
		curr := snapshot{bidVol: bidVol, askVol: askVol, obi: obi, takenAt: now}

		s.mu.Lock()
		base, hasBase := s.baselines[sym]
		if !hasBase || now.Sub(base.takenAt) >= baselineRefreshSec*time.Second {
			s.baselines[sym] = curr
			s.mu.Unlock()
			continue
		}
		s.mu.Unlock()

		// Компоненты BullScore из статистики сделок за скользящее окно.
		var tfi, volImb, aggrRatio float64
		if s.tradeAgg != nil {
			win := s.tradeAgg.GetWindow(sym, time.Duration(s.cfg.TradeWindowSec)*time.Second)
			if tc := float64(win.BuyCount + win.SellCount); tc > 0 {
				tfi = float64(win.BuyCount-win.SellCount) / tc
			}
			if tv := win.BuyVolume + win.SellVolume; tv > 0 {
				volImb = (win.BuyVolume - win.SellVolume) / tv
			}
			if win.BuyCount > 0 && win.SellCount > 0 {
				avgBuy := win.BuyVolume / float64(win.BuyCount)
				avgSell := win.SellVolume / float64(win.SellCount)
				if total := avgBuy + avgSell; total > 0 {
					aggrRatio = (avgBuy - avgSell) / total
				}
			} else if win.BuyCount > 0 {
				aggrRatio = 1.0
			} else if win.SellCount > 0 {
				aggrRatio = -1.0
			}
		}

		// AskDrain: уход продавцов из стакана с момента baseline.
		askDrain := 0.0
		if base.askVol > 0 {
			d := (base.askVol - askVol) / base.askVol
			if d > 1 {
				d = 1
			} else if d < -1 {
				d = -1
			}
			askDrain = d
		}

		bullScore := orderbook.CalcBullScore(obi, obi-base.obi, tfi, volImb, askDrain, aggrRatio)

		s.log.Debug("volatile: signal check",
			zap.String("symbol", sym),
			zap.Float64("obi", obi),
			zap.Float64("bull_score", bullScore),
		)

		if bullScore >= s.cfg.BullScoreMin {
			if time.Now().Before(s.warmupUntil) {
				s.log.Debug("volatile: warmup in progress, skipping entry",
					zap.String("symbol", sym),
					zap.Float64("bull_score", bullScore),
					zap.Duration("remaining", time.Until(s.warmupUntil)),
				)
				continue
			}
			s.tryEnter(sym, mid, bullScore)
		}
	}
}

// tryEnter пытается открыть позицию по символу при прохождении всех фильтров.
func (s *Service) tryEnter(symbol string, price, bullScore float64) {
	// 1. Cooldown по символу.
	s.mu.Lock()
	if last, ok := s.cooldowns[symbol]; ok {
		cooldownDur := time.Duration(s.cfg.CooldownSec) * time.Second
		if time.Since(last) < cooldownDur {
			remaining := cooldownDur - time.Since(last)
			s.mu.Unlock()
			s.log.Debug("volatile: cooldown active",
				zap.String("symbol", symbol),
				zap.Duration("remaining", remaining),
			)
			return
		}
	}
	s.mu.Unlock()

	// 2. Уже есть открытая сделка по символу.
	if s.tracker.Has(symbol) {
		return
	}

	// 3. Лимит одновременных позиций.
	if s.tracker.Count() >= s.cfg.MaxPositions {
		s.log.Warn("volatile: max positions limit reached",
			zap.Int("current", s.tracker.Count()),
			zap.Int("max", s.cfg.MaxPositions),
		)
		return
	}

	// 4. Валидная цена.
	if price <= 0 {
		s.log.Warn("volatile: invalid entry price",
			zap.String("symbol", symbol),
			zap.Float64("price", price),
		)
		return
	}

	// 5. Расчёт qty.
	qty := s.cfg.TradeAmountUSDT / price
	if qty <= 0 {
		s.log.Warn("volatile: calculated qty is zero", zap.String("symbol", symbol))
		return
	}

	s.log.Info("volatile: bull signal accepted, opening trade",
		zap.String("symbol", symbol),
		zap.String("exchange", s.cfg.Exchange),
		zap.Float64("bull_score", bullScore),
		zap.Float64("entry_price", price),
		zap.Float64("qty", qty),
	)

	// 6. Открываем сделку.
	id, err := s.tradeSvc.OpenTrade(s.ctx, trade.Trade{
		Strategy:      "volatile",
		TradeExchange: s.cfg.Exchange,
		Symbol:        symbol,
		Qty:           qty,
		EntryPrice:    price,
	})
	if err != nil {
		s.log.Error("volatile: open trade failed",
			zap.String("symbol", symbol),
			zap.Error(err),
		)
		s.mu.Lock()
		s.cooldowns[symbol] = time.Now()
		s.mu.Unlock()
		return
	}

	// 7. Регистрируем в трекере.
	t := &VolatileTrade{
		ID:         id,
		Symbol:     symbol,
		Exchange:   s.cfg.Exchange,
		EntryPrice: price,
		PeakPrice:  price,
		Qty:        qty,
		BullScore:  bullScore,
		OpenedAt:   time.Now(),
		PriceCh:    make(chan float64, 64),
		CrashCh:    make(chan struct{}, 1),
	}
	s.tracker.Add(t)

	// 8. Запускаем горутину мониторинга.
	go s.watchTrade(t)

	// 9. Обновляем cooldown.
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

	if event.Exchange != t.Exchange {
		return
	}

	s.log.Warn("volatile: crash signal received, sending exit to watchTrade",
		zap.String("symbol", event.Symbol),
		zap.String("exchange", event.Exchange),
		zap.Float64("change_pct", event.ChangePct),
	)

	select {
	case t.CrashCh <- struct{}{}:
	default:
	}
}

// OnTicker получает тикеры и направляет цену в активные сделки.
func (s *Service) OnTicker(t ticker.Ticker) {
	tr, ok := s.tracker.Get(t.Symbol)
	if !ok {
		return
	}

	if t.Exchange != tr.Exchange {
		return
	}

	price, err := strconv.ParseFloat(t.Price, 64)
	if err != nil || price <= 0 {
		return
	}

	select {
	case tr.PriceCh <- price:
	default:
		s.log.Warn("volatile: price channel full, tick dropped",
			zap.String("symbol", t.Symbol),
		)
	}
}

// watchTrade мониторит сделку до TP/SL/trailing/crash/timeout.
func (s *Service) watchTrade(t *VolatileTrade) {
	timeout := time.NewTimer(time.Duration(s.cfg.MaxHoldSec) * time.Second)
	defer timeout.Stop()

	trailingStop := t.EntryPrice * (1 - s.cfg.TrailingStopPct/100)
	hardSL := t.EntryPrice * (1 - s.cfg.StopLossPct/100)
	hardTPEnabled := s.cfg.TakeProfitPct > 0
	hardTP := t.EntryPrice * (1 + s.cfg.TakeProfitPct/100)

	s.log.Info("volatile: watching trade",
		zap.Int64("id", t.ID),
		zap.String("symbol", t.Symbol),
		zap.Float64("entry_price", t.EntryPrice),
		zap.Float64("bull_score", t.BullScore),
		zap.Float64("hard_sl", hardSL),
		zap.Float64("hard_tp", hardTP),
		zap.Float64("trailing_stop_initial", trailingStop),
		zap.Int("max_hold_sec", s.cfg.MaxHoldSec),
	)

	lastPrice := t.EntryPrice

	for {
		select {
		case <-s.ctx.Done():
			return

		case <-timeout.C:
			s.log.Warn("volatile: timeout, force closing",
				zap.Int64("id", t.ID),
				zap.String("symbol", t.Symbol),
				zap.Float64("last_price", lastPrice),
				zap.Int("hold_sec", int(time.Since(t.OpenedAt).Seconds())),
			)
			s.closeTrade(t, lastPrice, "timeout")
			return

		case <-t.CrashCh:
			s.log.Warn("volatile: crash exit triggered",
				zap.Int64("id", t.ID),
				zap.String("symbol", t.Symbol),
				zap.Float64("last_price", lastPrice),
			)
			s.closeTrade(t, lastPrice, "crash")
			return

		case price := <-t.PriceCh:
			lastPrice = price
			if price > t.PeakPrice {
				t.PeakPrice = price
				trailingStop = t.PeakPrice * (1 - s.cfg.TrailingStopPct/100)
				s.log.Debug("volatile: new peak price",
					zap.Int64("id", t.ID),
					zap.String("symbol", t.Symbol),
					zap.Float64("peak_price", t.PeakPrice),
					zap.Float64("trailing_stop", trailingStop),
				)
			}

			if price <= hardSL {
				s.log.Warn("volatile: hard SL hit",
					zap.Int64("id", t.ID),
					zap.String("symbol", t.Symbol),
					zap.Float64("price", price),
					zap.Float64("hard_sl", hardSL),
				)
				s.closeTrade(t, price, "sl")
				return
			}

			if price <= trailingStop {
				s.log.Info("volatile: trailing stop hit",
					zap.Int64("id", t.ID),
					zap.String("symbol", t.Symbol),
					zap.Float64("price", price),
					zap.Float64("trailing_stop", trailingStop),
					zap.Float64("peak_price", t.PeakPrice),
				)
				s.closeTrade(t, price, "trailing")
				return
			}

			if hardTPEnabled && price >= hardTP {
				s.log.Info("volatile: hard TP hit",
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
func (s *Service) closeTrade(t *VolatileTrade, exitPrice float64, reason string) {
	defer s.tracker.Remove(t.Symbol)

	err := s.tradeSvc.CloseTrade(s.ctx, t.ID, exitPrice, reason)
	if err != nil {
		s.log.Error("volatile: close trade failed",
			zap.Int64("id", t.ID),
			zap.String("symbol", t.Symbol),
			zap.String("reason", reason),
			zap.Error(err),
		)
		return
	}

	pnl := (exitPrice - t.EntryPrice) * t.Qty
	s.log.Info("volatile: trade closed",
		zap.Int64("id", t.ID),
		zap.String("symbol", t.Symbol),
		zap.String("reason", reason),
		zap.Float64("entry_price", t.EntryPrice),
		zap.Float64("exit_price", exitPrice),
		zap.Float64("peak_price", t.PeakPrice),
		zap.Float64("bull_score", t.BullScore),
		zap.Float64("pnl_usdt", pnl),
		zap.Int("hold_sec", int(time.Since(t.OpenedAt).Seconds())),
	)
}
