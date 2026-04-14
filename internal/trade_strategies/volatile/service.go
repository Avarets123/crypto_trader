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
	"github.com/osman/bot-traider/internal/shared/utils"
	"github.com/osman/bot-traider/internal/ticker"
	"github.com/osman/bot-traider/internal/trade"
)

// Service реализует микроскальпинг стратегию:
// событийный вход при taker buy «съедающей» Ask1 + OBI_1pct + CVD_5s + спред.
// Выход: TP по стене сопротивления, жёсткий SL, динамический выход при ослаблении CVD.
type Service struct {
	mu          sync.Mutex
	cfg         Config
	ctx         context.Context
	tradeSvc    *trade.Service
	bookSvc     *orderbook.Service
	tracker     *TradeTracker
	cooldowns   map[string]time.Time
	symbols     []string
	metrics     map[string]*SymbolMetrics
	warmupUntil time.Time
	log         *zap.Logger
}

// New создаёт и запускает Service если VOLATILE_ENABLED=true.
func New(ctx context.Context, tradeSvc *trade.Service, tickerService *ticker.TickerService, bookSvc *orderbook.Service, symbols []string, log *zap.Logger) *Service {
	cfg := LoadConfig()
	if !cfg.Enabled {
		log.Info("volatile strategy disabled (VOLATILE_ENABLED=false)")
		return nil
	}

	svc := NewService(ctx, cfg, tradeSvc, bookSvc, log.With(zap.String("component", "volatile")))
	svc.SetSymbols(symbols)
	tickerService.WithOnSend(svc.OnTicker)
	go svc.Start(ctx)

	log.Info("volatile (micro-scalping) strategy enabled",
		zap.String("exchange", cfg.Exchange),
		zap.Float64("obi_min", cfg.OBIMin),
		zap.Float64("spread_max_pct", cfg.SpreadMaxPct),
		zap.Float64("stop_loss_pct", cfg.StopLossPct),
		zap.Float64("tp_fallback_pct", cfg.TPFallbackPct),
		zap.Int("warmup_sec", cfg.WarmupSec),
	)
	return svc
}

// NewService создаёт Service без запуска.
func NewService(ctx context.Context, cfg Config, tradeSvc *trade.Service, bookSvc *orderbook.Service, log *zap.Logger) *Service {
	log.Info("volatile strategy initialized",
		zap.String("exchange", cfg.Exchange),
		zap.Float64("obi_min", cfg.OBIMin),
		zap.Float64("spread_max_pct", cfg.SpreadMaxPct),
		zap.Float64("stop_loss_pct", cfg.StopLossPct),
		zap.Float64("tp_fallback_pct", cfg.TPFallbackPct),
		zap.Float64("wall_mult", cfg.WallMult),
		zap.Float64("whale_mult", cfg.WhaleMult),
		zap.Float64("min_whale_qty", cfg.MinWhaleQty),
		zap.Int("cvd_check_interval_ms", cfg.CVDCheckIntervalMs),
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
		metrics:   make(map[string]*SymbolMetrics),
		log:       log,
	}
}

// SetSymbols задаёт начальный список символов для мониторинга.
func (s *Service) SetSymbols(symbols []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.symbols = make([]string, len(symbols))
	copy(s.symbols, symbols)
	for _, sym := range s.symbols {
		if _, ok := s.metrics[sym]; !ok {
			s.metrics[sym] = newSymbolMetrics(s.log)
		}
	}
}

// OnSymbolsChanged реагирует на изменение топ-листа.
func (s *Service) OnSymbolsChanged(added, removed []string) {
	defer utils.TimeTracker(s.log, "OnSymbolsChanged volatile service")()

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(removed) > 0 {
		removedSet := make(map[string]struct{}, len(removed))
		for _, sym := range removed {
			removedSet[sym] = struct{}{}
			delete(s.metrics, sym)
		}
		filtered := s.symbols[:0]
		for _, sym := range s.symbols {
			if _, ok := removedSet[sym]; !ok {
				filtered = append(filtered, sym)
			}
		}
		s.symbols = filtered
	}

	for _, sym := range added {
		if _, ok := s.metrics[sym]; !ok {
			s.metrics[sym] = newSymbolMetrics(s.log)
		}
	}
	s.symbols = append(s.symbols, added...)
}

// OnTrade вызывается при каждой входящей сделке с биржи.
// Обновляет метрики символа и при taker buy проверяет условия входа.
func (s *Service) OnTrade(order exchange_orders.ExchangeOrder) {
	defer utils.TimeTracker(s.log, "OnTrade volatile service")()

	if order.Exchange != s.cfg.Exchange {
		return
	}
	qty, err := strconv.ParseFloat(order.Quantity, 64)
	if err != nil || qty <= 0 {
		return
	}
	isBuy := order.Side == "buy"

	m := s.getOrCreateMetrics(order.Symbol)
	m.OnTrade(qty, isBuy)

	// Проверяем сигнал только на taker buy
	if isBuy {
		s.checkSignalForSymbol(order.Symbol, qty)
	}
}

// getOrCreateMetrics возвращает или создаёт SymbolMetrics для символа.
func (s *Service) getOrCreateMetrics(symbol string) *SymbolMetrics {
	s.mu.Lock()
	m, ok := s.metrics[symbol]
	if !ok {
		m = newSymbolMetrics(s.log)
		s.metrics[symbol] = m
	}
	s.mu.Unlock()
	return m
}

// Start инициализирует период прогрева и блокирует до ctx.Done().
func (s *Service) Start(ctx context.Context) {
	warmup := time.Duration(s.cfg.WarmupSec) * time.Second
	s.warmupUntil = time.Now().Add(warmup)
	s.log.Info("volatile: warmup period started",
		zap.Duration("duration", warmup),
		zap.Time("ready_at", s.warmupUntil),
	)
	<-ctx.Done()
}

// checkSignalForSymbol проверяет 4 условия микроскальпинг входа для символа.
// Вызывается событийно при каждой taker buy сделке.
func (s *Service) checkSignalForSymbol(symbol string, lastBuyQty float64) {
	defer utils.TimeTracker(s.log, "checkSignalForSymbol volatile service")()

	if time.Now().Before(s.warmupUntil) {
		return
	}

	ob, ok := s.bookSvc.GetBook(symbol)
	if !ok || len(ob.Bids) == 0 || len(ob.Asks) == 0 {
		return
	}

	ask0Price, err := strconv.ParseFloat(ob.Asks[0].Price, 64)
	if err != nil || ask0Price <= 0 {
		return
	}
	ask0Qty, err := strconv.ParseFloat(ob.Asks[0].Qty, 64)
	if err != nil || ask0Qty <= 0 {
		return
	}
	bid0Price, err := strconv.ParseFloat(ob.Bids[0].Price, 64)
	if err != nil || bid0Price <= 0 {
		return
	}

	m := s.getOrCreateMetrics(symbol)
	avgVol1min := m.AvgTradeVol1min()

	// --- Условие 1: триггер «съедена стена Ask1» ---
	// Сделка — кит (qty >= WhaleMult*avg AND qty >= MinWhaleQty)
	// и объём >= 90% от ask[0].qty
	isWhale := avgVol1min > 0 && lastBuyQty >= s.cfg.WhaleMult*avgVol1min && lastBuyQty >= s.cfg.MinWhaleQty
	ask1Eaten := lastBuyQty >= 0.9*ask0Qty
	if !isWhale || !ask1Eaten {
		return
	}

	// --- Условие 2: дисбаланс стакана OBI_1pct > OBIMin ---
	midPrice := (bid0Price + ask0Price) / 2
	obi := s.calcOBI1pct(ob, midPrice)
	if obi <= s.cfg.OBIMin {
		s.log.Debug("volatile: OBI filter failed",
			zap.String("symbol", symbol),
			zap.Float64("obi", obi),
			zap.Float64("min", s.cfg.OBIMin),
		)
		return
	}

	// --- Условие 3: CVD_5s > avg + 2*std (за час) ---
	cvd5s := m.CVD5s()
	threshold := m.CVDThreshold()
	if cvd5s <= threshold {
		s.log.Debug("volatile: CVD filter failed",
			zap.String("symbol", symbol),
			zap.Float64("cvd_5s", cvd5s),
			zap.Float64("threshold", threshold),
		)
		return
	}

	// --- Условие 4: спред < SpreadMaxPct ---
	spread := (ask0Price - bid0Price) / bid0Price
	if spread >= s.cfg.SpreadMaxPct {
		s.log.Debug("volatile: spread filter failed",
			zap.String("symbol", symbol),
			zap.Float64("spread", spread),
			zap.Float64("max", s.cfg.SpreadMaxPct),
		)
		return
	}

	s.log.Info("volatile: all conditions met, attempting entry",
		zap.String("symbol", symbol),
		zap.Float64("obi_1pct", obi),
		zap.Float64("cvd_5s", cvd5s),
		zap.Float64("cvd_threshold", threshold),
		zap.Float64("spread", spread),
		zap.Float64("last_buy_qty", lastBuyQty),
		zap.Float64("ask0_qty", ask0Qty),
	)

	tpPrice := s.calcTP(ob, midPrice, ask0Price, avgVol1min)
	s.tryEnter(symbol, midPrice, tpPrice, obi, cvd5s)
}

// calcOBI1pct вычисляет OBI по объёмам в диапазоне ±1% от mid-price (базовая валюта).
func (s *Service) calcOBI1pct(ob *orderbook.OrderBook, midPrice float64) float64 {
	lower := midPrice * 0.99
	upper := midPrice * 1.01

	var bidVol, askVol float64
	for _, e := range ob.Bids {
		p, _ := strconv.ParseFloat(e.Price, 64)
		if p < lower {
			break // стакан отсортирован по убыванию цены
		}
		q, _ := strconv.ParseFloat(e.Qty, 64)
		bidVol += q
	}
	for _, e := range ob.Asks {
		p, _ := strconv.ParseFloat(e.Price, 64)
		if p > upper {
			break // стакан отсортирован по возрастанию цены
		}
		q, _ := strconv.ParseFloat(e.Qty, 64)
		askVol += q
	}

	return orderbook.CalcOBI(bidVol, askVol)
}

// calcTP рассчитывает цену тейк-профита:
// первый уровень ask >= WallMult*avgVol1min в диапазоне 1% — 1 тик ниже стены.
// Если стена не найдена — fallback: ask0 * (1 + TPFallbackPct%).
func (s *Service) calcTP(ob *orderbook.OrderBook, midPrice, ask0Price, avgVol1min float64) float64 {
	wallThreshold := s.cfg.WallMult * avgVol1min
	upper := midPrice * 1.01

	if wallThreshold > 0 {
		for _, e := range ob.Asks {
			p, _ := strconv.ParseFloat(e.Price, 64)
			if p > upper {
				break
			}
			if p <= ask0Price {
				continue // пропускаем текущий уровень Ask0
			}
			q, _ := strconv.ParseFloat(e.Qty, 64)
			if q >= wallThreshold {
				s.log.Debug("volatile: resistance wall found",
					zap.Float64("wall_price", p),
					zap.Float64("wall_qty", q),
					zap.Float64("threshold", wallThreshold),
				)
				return p - 0.01
			}
		}
	}

	return ask0Price * (1 + s.cfg.TPFallbackPct/100)
}

// tryEnter пытается открыть позицию при прохождении всех фильтров.
func (s *Service) tryEnter(symbol string, entryPrice, tpPrice, obi, cvd5s float64) {
	// 1. Cooldown
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

	// 2. Уже открытая сделка по символу
	if s.tracker.Has(symbol) {
		return
	}

	// 3. Лимит позиций
	if s.tracker.Count() >= s.cfg.MaxPositions {
		s.log.Warn("volatile: max positions limit reached",
			zap.Int("current", s.tracker.Count()),
			zap.Int("max", s.cfg.MaxPositions),
		)
		return
	}

	if entryPrice <= 0 {
		return
	}

	qty := s.cfg.TradeAmountUSDT / entryPrice
	if qty <= 0 {
		return
	}

	s.log.Info("volatile: opening trade",
		zap.String("symbol", symbol),
		zap.String("exchange", s.cfg.Exchange),
		zap.Float64("entry_price", entryPrice),
		zap.Float64("tp_price", tpPrice),
		zap.Float64("sl_price", entryPrice*(1-s.cfg.StopLossPct/100)),
		zap.Float64("obi_1pct", obi),
		zap.Float64("cvd_5s", cvd5s),
		zap.Float64("qty", qty),
	)

	id, err := s.tradeSvc.OpenTrade(s.ctx, trade.Trade{
		Strategy:      "volatile",
		TradeExchange: s.cfg.Exchange,
		Symbol:        symbol,
		Qty:           qty,
		EntryPrice:    entryPrice,
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

	t := &VolatileTrade{
		ID:         id,
		Symbol:     symbol,
		Exchange:   s.cfg.Exchange,
		EntryPrice: entryPrice,
		TPPrice:    tpPrice,
		Qty:        qty,
		OBI:        obi,
		OpenedAt:   time.Now(),
		PriceCh:    make(chan float64, 64),
		CrashCh:    make(chan struct{}, 1),
	}
	s.tracker.Add(t)
	go s.watchTrade(t)

	s.mu.Lock()
	s.cooldowns[symbol] = time.Now()
	s.mu.Unlock()
}

// OnCrashEvent вызывается detector при обнаружении flash crash.
func (s *Service) OnCrashEvent(event *detector.DetectorEvent) {
	t, ok := s.tracker.Get(event.Symbol)
	if !ok || event.Exchange != t.Exchange {
		return
	}
	s.log.Warn("volatile: crash signal received, closing",
		zap.String("symbol", event.Symbol),
		zap.Float64("change_pct", event.ChangePct),
	)
	select {
	case t.CrashCh <- struct{}{}:
	default:
	}
}

// OnTicker получает тикеры и передаёт цену в горутины активных сделок.
func (s *Service) OnTicker(t ticker.Ticker) {
	defer utils.TimeTracker(s.log, "OnTicker volatile service")()

	tr, ok := s.tracker.Get(t.Symbol)
	if !ok || t.Exchange != tr.Exchange {
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

// watchTrade мониторит открытую сделку:
// - TP по resistance wall / fallback
// - SL жёсткий -StopLossPct%
// - Динамический выход при ослаблении CVD_5s (каждые CVDCheckIntervalMs мс)
// - Crash-событие от detector
// - Timeout MaxHoldSec
func (s *Service) watchTrade(t *VolatileTrade) {
	timeout := time.NewTimer(time.Duration(s.cfg.MaxHoldSec) * time.Second)
	defer timeout.Stop()

	cvdCheck := time.NewTicker(time.Duration(s.cfg.CVDCheckIntervalMs) * time.Millisecond)
	defer cvdCheck.Stop()

	hardSL := t.EntryPrice * (1 - s.cfg.StopLossPct/100)

	s.log.Info("volatile: watching trade",
		zap.Int64("id", t.ID),
		zap.String("symbol", t.Symbol),
		zap.Float64("entry_price", t.EntryPrice),
		zap.Float64("tp_price", t.TPPrice),
		zap.Float64("hard_sl", hardSL),
		zap.Float64("obi_at_entry", t.OBI),
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
			s.log.Warn("volatile: crash exit",
				zap.Int64("id", t.ID),
				zap.String("symbol", t.Symbol),
				zap.Float64("last_price", lastPrice),
			)
			s.closeTrade(t, lastPrice, "crash")
			return

		case <-cvdCheck.C:
			// Динамический выход: CVD ослаб до среднего или стал отрицательным
			m := s.getOrCreateMetrics(t.Symbol)
			cvd := m.CVD5s()
			avgCVD := m.AvgCVD1h()
			if cvd <= avgCVD || cvd < 0 {
				s.log.Info("volatile: CVD weakened, closing",
					zap.Int64("id", t.ID),
					zap.String("symbol", t.Symbol),
					zap.Float64("cvd_5s", cvd),
					zap.Float64("avg_cvd_1h", avgCVD),
					zap.Float64("last_price", lastPrice),
				)
				s.closeTrade(t, lastPrice, "cvd_weak")
				return
			}

		case price := <-t.PriceCh:
			lastPrice = price

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

			if t.TPPrice > 0 && price >= t.TPPrice {
				s.log.Info("volatile: TP hit",
					zap.Int64("id", t.ID),
					zap.String("symbol", t.Symbol),
					zap.Float64("price", price),
					zap.Float64("tp_price", t.TPPrice),
				)
				s.closeTrade(t, price, "tp")
				return
			}
		}
	}
}

// closeTrade закрывает сделку и логирует результат.
func (s *Service) closeTrade(t *VolatileTrade, exitPrice float64, reason string) {
	defer utils.TimeTracker(s.log, "closeTrade volatile service")()

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
		zap.Float64("tp_price", t.TPPrice),
		zap.Float64("obi_at_entry", t.OBI),
		zap.Float64("pnl_usdt", pnl),
		zap.Int("hold_sec", int(time.Since(t.OpenedAt).Seconds())),
	)
}
