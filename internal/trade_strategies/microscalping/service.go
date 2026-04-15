package microscalping

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

// New создаёт и запускает MultiService если MICROSCALPING_ENABLED=true.
// initialSymbols — начальные списки символов, ключ — имя биржи (например "binance", "kucoin").
// Для каждой биржи из cfg.Exchanges создаётся отдельный Service.
func New(ctx context.Context, tradeSvc *trade.Service, tickerService *ticker.TickerService, bookSvc *orderbook.Service, initialSymbols map[string][]string, log *zap.Logger) *MultiService {
	cfg := LoadConfig()
	if !cfg.Enabled {
		log.Info("microscalping strategy disabled (MICROSCALPING_ENABLED=false)")
		return nil
	}
	if len(cfg.Exchanges) == 0 {
		log.Warn("microscalping: no exchanges configured (MICROSCALPING_EXCHANGES is empty)")
		return nil
	}

	byExchange := make(map[string]*Service, len(cfg.Exchanges))
	for _, exchange := range cfg.Exchanges {
		perCfg := cfg
		perCfg.Exchange = exchange
		svcLog := log.With(zap.String("component", "microscalping"), zap.String("exchange", exchange))
		svc := NewService(ctx, perCfg, tradeSvc, bookSvc, svcLog)
		if syms, ok := initialSymbols[exchange]; ok {
			svc.SetSymbols(syms)
		}
		go svc.Start(ctx)
		byExchange[exchange] = svc
	}

	multi := newMultiService(byExchange)
	tickerService.WithOnSend(multi.OnTicker)

	log.Info("microscalping strategy enabled",
		zap.Strings("exchanges", cfg.Exchanges),
		zap.Float64("obi_min", cfg.OBIMin),
		zap.Float64("spread_max_pct", cfg.SpreadMaxPct),
		zap.Float64("stop_loss_pct", cfg.StopLossPct),
		zap.Float64("tp_fallback_pct", cfg.TPFallbackPct),
		zap.Int("warmup_sec", cfg.WarmupSec),
	)
	return multi
}

// NewService создаёт Service без запуска.
func NewService(ctx context.Context, cfg Config, tradeSvc *trade.Service, bookSvc *orderbook.Service, log *zap.Logger) *Service {
	log.Info("microscalping strategy initialized",
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
	defer utils.TimeTracker(s.log, "OnSymbolsChanged microscalping service")()

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
	defer utils.TimeTracker(s.log, "OnTrade microscalping service")()

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
	s.log.Info("microscalping: warmup period started",
		zap.Duration("duration", warmup),
		zap.Time("ready_at", s.warmupUntil),
	)
	<-ctx.Done()
}

// checkSignalForSymbol проверяет 4 условия микроскальпинг входа для символа.
// Вызывается событийно при каждой taker buy сделке.
func (s *Service) checkSignalForSymbol(symbol string, lastBuyQty float64) {
	defer utils.TimeTracker(s.log, "checkSignalForSymbol microscalping service")()

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
		s.log.Debug("microscalping: OBI filter failed",
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
		s.log.Debug("microscalping: CVD filter failed",
			zap.String("symbol", symbol),
			zap.Float64("cvd_5s", cvd5s),
			zap.Float64("threshold", threshold),
		)
		return
	}

	// --- Условие 4: спред < SpreadMaxPct ---
	spread := (ask0Price - bid0Price) / bid0Price
	if spread >= s.cfg.SpreadMaxPct {
		s.log.Debug("microscalping: spread filter failed",
			zap.String("symbol", symbol),
			zap.Float64("spread", spread),
			zap.Float64("max", s.cfg.SpreadMaxPct),
		)
		return
	}

	s.log.Info("microscalping: all conditions met, attempting entry",
		zap.String("symbol", symbol),
		zap.Float64("obi_1pct", obi),
		zap.Float64("cvd_5s", cvd5s),
		zap.Float64("cvd_threshold", threshold),
		zap.Float64("spread", spread),
		zap.Float64("last_buy_qty", lastBuyQty),
		zap.Float64("ask0_qty", ask0Qty),
	)

	// Входим по ask0Price — фактическая цена исполнения taker-покупки
	tpPrice := s.calcTP(ob, midPrice, ask0Price, avgVol1min)
	s.tryEnter(symbol, ask0Price, tpPrice, obi, cvd5s)
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
	upper := midPrice * (1 + s.cfg.TPRangePct/100)

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
				s.log.Debug("microscalping: resistance wall found",
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
			s.log.Debug("microscalping: cooldown active",
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
		s.log.Warn("microscalping: max positions limit reached",
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

	s.log.Info("microscalping: opening trade",
		zap.String("symbol", symbol),
		zap.String("exchange", s.cfg.Exchange),
		zap.Float64("entry_price", entryPrice),
		zap.Float64("tp_price", tpPrice),
		zap.Float64("sl_price", entryPrice*(1-s.cfg.StopLossPct/100)),
		zap.Float64("obi_1pct", obi),
		zap.Float64("cvd_5s", cvd5s),
		zap.Float64("qty", qty),
	)

	slPrice := entryPrice * (1 - s.cfg.StopLossPct/100)
	id, err := s.tradeSvc.OpenTrade(s.ctx, trade.Trade{
		Strategy:      "microscalping",
		TradeExchange: s.cfg.Exchange,
		Symbol:        symbol,
		Qty:           qty,
		EntryPrice:    entryPrice,
		TargetPrice:   &tpPrice,
		StopLossPrice: &slPrice,
	})
	if err != nil {
		s.log.Error("microscalping: open trade failed",
			zap.String("symbol", symbol),
			zap.Error(err),
		)
		s.mu.Lock()
		s.cooldowns[symbol] = time.Now()
		s.mu.Unlock()
		return
	}

	t := &MicroscalpingTrade{
		ID:         id,
		Symbol:     symbol,
		Exchange:   s.cfg.Exchange,
		EntryPrice: entryPrice,
		TPPrice:    tpPrice,
		Qty:        qty,
		OBI:        obi,
		OpenedAt:   time.Now(),
		PriceCh:    make(chan float64, 512),
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
	s.log.Warn("microscalping: crash signal received, closing",
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
	defer utils.TimeTracker(s.log, "OnTicker microscalping service")()

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
		s.log.Warn("microscalping: price channel full, tick dropped",
			zap.String("symbol", t.Symbol),
		)
	}
}

// watchTrade мониторит открытую сделку:
// - TP по resistance wall / fallback (пересчитывается каждые TPCheckIntervalSec)
// - SL жёсткий -StopLossPct%
// - Trailing stop: активируется при росте на TrailActivationPct%, трейлит с шагом TrailPct%
// - Динамический выход при ослаблении CVD_5s (не раньше MinHoldSec с момента входа)
// - Crash-событие от detector
// - Timeout MaxHoldSec
func (s *Service) watchTrade(t *MicroscalpingTrade) {
	timeout := time.NewTimer(time.Duration(s.cfg.MaxHoldSec) * time.Second)
	defer timeout.Stop()

	cvdCheck := time.NewTicker(time.Duration(s.cfg.CVDCheckIntervalMs) * time.Millisecond)
	defer cvdCheck.Stop()

	tpCheck := time.NewTicker(time.Duration(s.cfg.TPCheckIntervalSec) * time.Second)
	defer tpCheck.Stop()

	hardSL := t.EntryPrice * (1 - s.cfg.StopLossPct/100)
	t.HighestPrice = t.EntryPrice

	s.log.Info("microscalping: watching trade",
		zap.Int64("id", t.ID),
		zap.String("symbol", t.Symbol),
		zap.Float64("entry_price", t.EntryPrice),
		zap.Float64("tp_price", t.TPPrice),
		zap.Float64("hard_sl", hardSL),
		zap.Float64("obi_at_entry", t.OBI),
		zap.Int("max_hold_sec", s.cfg.MaxHoldSec),
		zap.Float64("trail_activation_pct", s.cfg.TrailActivationPct),
		zap.Float64("trail_pct", s.cfg.TrailPct),
	)

	lastPrice := t.EntryPrice

	for {
		select {
		case <-s.ctx.Done():
			return

		case <-timeout.C:
			s.log.Warn("microscalping: timeout, force closing",
				zap.Int64("id", t.ID),
				zap.String("symbol", t.Symbol),
				zap.Float64("last_price", lastPrice),
				zap.Int("hold_sec", int(time.Since(t.OpenedAt).Seconds())),
			)
			s.closeTrade(t, lastPrice, "timeout")
			return

		case <-t.CrashCh:
			s.log.Warn("microscalping: crash exit",
				zap.Int64("id", t.ID),
				zap.String("symbol", t.Symbol),
				zap.Float64("last_price", lastPrice),
			)
			s.closeTrade(t, lastPrice, "crash")
			return

		case <-cvdCheck.C:
			// Грейс-период: не выходим по CVD в первые MinHoldSec секунд
			if time.Since(t.OpenedAt) < time.Duration(s.cfg.MinHoldSec)*time.Second {
				continue
			}
			m := s.getOrCreateMetrics(t.Symbol)
			cvd := m.CVD5s()
			avgCVD := m.AvgCVD1h()
			if cvd <= avgCVD || cvd < 0 {
				s.log.Info("microscalping: CVD weakened, closing",
					zap.Int64("id", t.ID),
					zap.String("symbol", t.Symbol),
					zap.Float64("cvd_5s", cvd),
					zap.Float64("avg_cvd_1h", avgCVD),
					zap.Float64("last_price", lastPrice),
					zap.Int("hold_sec", int(time.Since(t.OpenedAt).Seconds())),
				)
				s.closeTrade(t, lastPrice, "cvd_weak")
				return
			}

		case <-tpCheck.C:
			// Динамический пересчёт TP по актуальному стакану
			newTP := s.recalcTP(t.Symbol, t.EntryPrice, t.TPPrice)
			if newTP != t.TPPrice {
				s.log.Info("microscalping: TP recalculated",
					zap.Int64("id", t.ID),
					zap.String("symbol", t.Symbol),
					zap.Float64("old_tp", t.TPPrice),
					zap.Float64("new_tp", newTP),
				)
				t.TPPrice = newTP
			}

		case price := <-t.PriceCh:
			lastPrice = price

			// Обновляем максимум
			if price > t.HighestPrice {
				t.HighestPrice = price
			}

			// Trailing stop: активируем когда цена поднялась на TrailActivationPct%
			trailActivationLevel := t.EntryPrice * (1 + s.cfg.TrailActivationPct/100)
			if price >= trailActivationLevel {
				newTrailSL := t.HighestPrice * (1 - s.cfg.TrailPct/100)
				if !t.TrailActive {
					t.TrailActive = true
					t.TrailSL = newTrailSL
					s.log.Info("microscalping: trailing stop activated",
						zap.Int64("id", t.ID),
						zap.String("symbol", t.Symbol),
						zap.Float64("price", price),
						zap.Float64("trail_sl", t.TrailSL),
					)
				} else if newTrailSL > t.TrailSL {
					t.TrailSL = newTrailSL
				}
			}

			// Проверка trailing SL
			if t.TrailActive && price <= t.TrailSL {
				s.log.Info("microscalping: trailing SL hit",
					zap.Int64("id", t.ID),
					zap.String("symbol", t.Symbol),
					zap.Float64("price", price),
					zap.Float64("trail_sl", t.TrailSL),
					zap.Float64("highest", t.HighestPrice),
				)
				s.closeTrade(t, price, "trail_sl")
				return
			}

			// Жёсткий SL
			if price <= hardSL {
				s.log.Warn("microscalping: hard SL hit",
					zap.Int64("id", t.ID),
					zap.String("symbol", t.Symbol),
					zap.Float64("price", price),
					zap.Float64("hard_sl", hardSL),
				)
				s.closeTrade(t, price, "sl")
				return
			}

			// TP
			if t.TPPrice > 0 && price >= t.TPPrice {
				s.log.Info("microscalping: TP hit",
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

// recalcTP пересчитывает TP по актуальному стакану.
// Если новая стена не найдена или находится ниже цены входа — возвращает currentTP без изменений.
func (s *Service) recalcTP(symbol string, entryPrice, currentTP float64) float64 {
	ob, ok := s.bookSvc.GetBook(symbol)
	if !ok || len(ob.Asks) == 0 || len(ob.Bids) == 0 {
		return currentTP
	}
	ask0Price, err := strconv.ParseFloat(ob.Asks[0].Price, 64)
	if err != nil || ask0Price <= 0 {
		return currentTP
	}
	bid0Price, err := strconv.ParseFloat(ob.Bids[0].Price, 64)
	if err != nil || bid0Price <= 0 {
		return currentTP
	}
	midPrice := (bid0Price + ask0Price) / 2
	avgVol1min := s.getOrCreateMetrics(symbol).AvgTradeVol1min()

	newTP := s.calcTP(ob, midPrice, ask0Price, avgVol1min)
	if newTP <= entryPrice {
		return currentTP
	}
	return newTP
}

// closeTrade закрывает сделку и логирует результат.
func (s *Service) closeTrade(t *MicroscalpingTrade, exitPrice float64, reason string) {
	defer utils.TimeTracker(s.log, "closeTrade microscalping service")()

	defer s.tracker.Remove(t.Symbol)

	err := s.tradeSvc.CloseTrade(s.ctx, t.ID, exitPrice, reason)
	if err != nil {
		s.log.Error("microscalping: close trade failed",
			zap.Int64("id", t.ID),
			zap.String("symbol", t.Symbol),
			zap.String("reason", reason),
			zap.Error(err),
		)
		return
	}

	pnl := (exitPrice - t.EntryPrice) * t.Qty
	s.log.Info("microscalping: trade closed",
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
