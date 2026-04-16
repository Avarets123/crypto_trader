package microscalping

import (
	"context"
	"strconv"
	"strings"
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

// Service реализует микроскальпинг стратегию v2.
//
// Лонг-сигнал: Ask-стена поглощается + кит Buy + VolumeCluster выше среднего.
// Шорт-сигнал: локальный перегрев +2% за 15 мин + новая Ask-стена + CD_1m < 0 + кит Sell + дивергенция объёма.
// Выход: TP по проценту прибыли при стабильной Ask-стене / wall-SL / trailing-SL / 4-часовой timeout.
//
// Короткие позиции (shorts) требуют маржинального или фьючерсного аккаунта на бирже.
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
	walls       map[string]*WallTracker
	warmupUntil time.Time
	mskLoc      *time.Location
	log         *zap.Logger
}

// New создаёт и запускает MultiService если MICROSCALPING_ENABLED=true.
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

	log.Info("microscalping v2 strategy enabled",
		zap.Strings("exchanges", cfg.Exchanges),
		zap.Float64("whale_mult", cfg.WhaleMult),
		zap.Float64("wall_anomaly_mult", cfg.WallAnomalyMult),
		zap.Int("wall_min_age_sec", cfg.WallMinAgeSec),
		zap.Float64("wall_absorption_pct", cfg.WallAbsorptionPct),
		zap.Int("max_position_duration_h", cfg.MaxPositionDurationH),
		zap.Int("no_trade_msk", cfg.NoTradeHourStartMSK),
		zap.Int("warmup_sec", cfg.WarmupSec),
	)
	return multi
}

// NewService создаёт Service без запуска.
func NewService(ctx context.Context, cfg Config, tradeSvc *trade.Service, bookSvc *orderbook.Service, log *zap.Logger) *Service {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		log.Warn("microscalping: failed to load Moscow timezone, falling back to UTC", zap.Error(err))
		loc = time.UTC
	}

	log.Info("microscalping v2 initialized",
		zap.String("exchange", cfg.Exchange),
		zap.Float64("whale_mult", cfg.WhaleMult),
		zap.Float64("wall_anomaly_mult", cfg.WallAnomalyMult),
		zap.Float64("wall_absorption_pct", cfg.WallAbsorptionPct),
		zap.Float64("stop_loss_pct", cfg.StopLossPct),
		zap.Float64("tp_pct_btceth", (cfg.TPPctBTCETHMin+cfg.TPPctBTCETHMax)/2),
		zap.Float64("tp_pct_alt", (cfg.TPPctAltMin+cfg.TPPctAltMax)/2),
		zap.Int("max_position_h", cfg.MaxPositionDurationH),
		zap.Int("cooldown_sec", cfg.CooldownSec),
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
		walls:     make(map[string]*WallTracker),
		mskLoc:    loc,
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
			s.metrics[sym] = newSymbolMetrics()
		}
		if _, ok := s.walls[sym]; !ok {
			s.walls[sym] = newWallTracker(s.log)
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
			delete(s.walls, sym)
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
			s.metrics[sym] = newSymbolMetrics()
		}
		if _, ok := s.walls[sym]; !ok {
			s.walls[sym] = newWallTracker(s.log)
		}
	}
	s.symbols = append(s.symbols, added...)
}

// OnTrade вызывается при каждой входящей сделке с биржи.
// Обновляет метрики, стены и проверяет условия входа.
func (s *Service) OnTrade(order exchange_orders.ExchangeOrder) {
	defer utils.TimeTracker(s.log, "OnTrade microscalping service")()

	if order.Exchange != s.cfg.Exchange {
		return
	}
	qty, err := strconv.ParseFloat(order.Quantity, 64)
	if err != nil || qty <= 0 {
		return
	}
	price, err := strconv.ParseFloat(order.Price, 64)
	if err != nil || price <= 0 {
		return
	}
	isBuy := order.Side == "buy"
	tradeUSDT := qty * price

	m := s.getOrCreateMetrics(order.Symbol)
	m.OnTrade(qty, price, isBuy)

	ob, ok := s.bookSvc.GetBook(order.Symbol)
	if !ok || len(ob.Asks) == 0 || len(ob.Bids) == 0 {
		return
	}

	wt := s.getOrCreateWallTracker(order.Symbol)
	wt.Update(ob, s.cfg.WallAnomalyMult)

	if isBuy {
		s.checkLongSignal(order.Symbol, tradeUSDT, ob, m, wt)
	} else {
		s.checkShortSignal(order.Symbol, tradeUSDT, ob, m, wt)
	}
}

// getOrCreateMetrics возвращает или создаёт SymbolMetrics для символа.
func (s *Service) getOrCreateMetrics(symbol string) *SymbolMetrics {
	s.mu.Lock()
	m, ok := s.metrics[symbol]
	if !ok {
		m = newSymbolMetrics()
		s.metrics[symbol] = m
	}
	s.mu.Unlock()
	return m
}

// getOrCreateWallTracker возвращает или создаёт WallTracker для символа.
func (s *Service) getOrCreateWallTracker(symbol string) *WallTracker {
	s.mu.Lock()
	wt, ok := s.walls[symbol]
	if !ok {
		wt = newWallTracker(s.log)
		s.walls[symbol] = wt
	}
	s.mu.Unlock()
	return wt
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

// isNoTradeWindow возвращает true если сейчас запрещённое окно по времени МСК.
// Запрет: NoTradeHourStartMSK..NoTradeHourEndMSK (по умолчанию 03:00–10:00 МСК).
func (s *Service) isNoTradeWindow() bool {
	h := time.Now().In(s.mskLoc).Hour()
	return h >= s.cfg.NoTradeHourStartMSK && h < s.cfg.NoTradeHourEndMSK
}

// symbolType возвращает тип символа: "btc", "eth" или "alt".
func symbolType(symbol string) string {
	upper := strings.ToUpper(symbol)
	if strings.HasPrefix(upper, "BTC") {
		return "btc"
	}
	if strings.HasPrefix(upper, "ETH") {
		return "eth"
	}
	return "alt"
}

// whaleThresholdUSDT вычисляет порог «кита» в USDT для символа.
// Берётся максимум из динамического (avg * WhaleMult) и абсолютного порога.
func (s *Service) whaleThresholdUSDT(symbol string, avgTradeSizeUSDT float64) float64 {
	dynamic := avgTradeSizeUSDT * s.cfg.WhaleMult
	var absolute float64
	switch symbolType(symbol) {
	case "btc":
		absolute = s.cfg.WhaleBTCAbsUSD
	case "eth":
		absolute = s.cfg.WhaleETHAbsUSD
	default:
		absolute = s.cfg.WhaleAltAbsUSD
	}
	if dynamic > absolute {
		return dynamic
	}
	return absolute
}

// isWhale возвращает true если сделка является «китовой».
// Критерии: объём > порога WhaleMult*avg ИЛИ > абсолютного порога ИЛИ съедает > WhaleOrderbookPct% уровня.
func (s *Service) isWhale(symbol string, tradeUSDT, avgTradeSizeUSDT float64, ob *orderbook.OrderBook) bool {
	threshold := s.whaleThresholdUSDT(symbol, avgTradeSizeUSDT)
	if tradeUSDT >= threshold {
		return true
	}
	// Дополнительно: сделка съела > WhaleOrderbookPct% уровня стакана
	if ob != nil && len(ob.Asks) > 0 {
		askPrice, _ := strconv.ParseFloat(ob.Asks[0].Price, 64)
		askQty, _ := strconv.ParseFloat(ob.Asks[0].Qty, 64)
		if askPrice > 0 && askQty > 0 {
			tradeQty := tradeUSDT / askPrice
			if tradeQty/askQty*100 >= s.cfg.WhaleOrderbookPct {
				return true
			}
		}
	}
	return false
}

// calcTPPrice рассчитывает целевую цену TP в зависимости от типа символа.
// Использует середину диапазона TPPctMin..TPPctMax.
func (s *Service) calcTPPrice(symbol string, entryPrice float64, isShort bool) (tpPrice, tpPct float64) {
	var minPct, maxPct float64
	switch symbolType(symbol) {
	case "btc", "eth":
		minPct, maxPct = s.cfg.TPPctBTCETHMin, s.cfg.TPPctBTCETHMax
	default:
		minPct, maxPct = s.cfg.TPPctAltMin, s.cfg.TPPctAltMax
	}
	midPct := (minPct + maxPct) / 2
	if isShort {
		return entryPrice * (1 - midPct/100), midPct
	}
	return entryPrice * (1 + midPct/100), midPct
}

// checkLongSignal проверяет условия входа в ЛОНГ на основе сделки Buy.
//
// Условия (все должны совпасть):
//  1. Не в запрещённом временном окне (03:00–10:00 МСК)
//  2. Ask-стена (age > WallMinAgeSec) поглощается на >= WallAbsorptionPct%
//  3. Текущая сделка — «кит» Buy
//  4. VolumeCluster_3m > VolumeCluster_1h_avg (рынок «живой»)
func (s *Service) checkLongSignal(symbol string, tradeUSDT float64, ob *orderbook.OrderBook, m *SymbolMetrics, wt *WallTracker) {
	defer utils.TimeTracker(s.log, "checkLongSignal microscalping")()

	if time.Now().Before(s.warmupUntil) {
		return
	}
	if s.isNoTradeWindow() {
		return
	}

	ask0Price, err1 := strconv.ParseFloat(ob.Asks[0].Price, 64)
	bid0Price, err2 := strconv.ParseFloat(ob.Bids[0].Price, 64)
	if err1 != nil || err2 != nil || ask0Price <= 0 || bid0Price <= 0 {
		return
	}

	avgTradeSizeUSDT := m.AvgTradeSizeUSDT()

	// Условие 1+2: Ask-стена с поглощением >= WallAbsorptionPct%
	absorbWall := wt.GetAbsorbingAskWall(bid0Price, s.cfg.WallMinAgeSec, s.cfg.WallAbsorptionPct)
	if absorbWall == nil {
		return
	}

	// Условие 3: кит Buy
	if !s.isWhale(symbol, tradeUSDT, avgTradeSizeUSDT, ob) {
		s.log.Debug("microscalping long: not a whale",
			zap.String("symbol", symbol),
			zap.Float64("trade_usdt", tradeUSDT),
			zap.Float64("threshold", s.whaleThresholdUSDT(symbol, avgTradeSizeUSDT)),
		)
		return
	}

	// Условие 4: VolumeCluster_3m > VolumeCluster_1h_avg
	vc3m := m.VolumeCluster3m()
	vc1hAvg := m.VolumeCluster1hAvg()
	if vc3m <= vc1hAvg {
		s.log.Debug("microscalping long: volume cluster below average",
			zap.String("symbol", symbol),
			zap.Float64("vc_3m", vc3m),
			zap.Float64("vc_1h_avg", vc1hAvg),
		)
		return
	}

	s.log.Info("microscalping long: all conditions met, attempting entry",
		zap.String("symbol", symbol),
		zap.Float64("wall_price", absorbWall.Price),
		zap.Float64("wall_age_sec", absorbWall.Age().Seconds()),
		zap.Float64("wall_absorption_pct", absorbWall.AbsorptionPct()),
		zap.Float64("whale_usdt", tradeUSDT),
		zap.Float64("vc_3m", vc3m),
		zap.Float64("vc_1h_avg", vc1hAvg),
	)

	// Ищем Bid-стену поддержки для SL
	supportWallLevel := float64(0)
	if suppWall := wt.GetBidSupportWall(ask0Price, s.cfg.WallMinAgeSec); suppWall != nil {
		supportWallLevel = suppWall.Price
	}

	tpPrice, _ := s.calcTPPrice(symbol, ask0Price, false)
	s.tryEnter(symbol, "long", ask0Price, tpPrice, supportWallLevel)
}

// checkShortSignal проверяет условия входа в ШОРТ на основе сделки Sell.
//
// Условия (все должны совпасть):
//  1. Цена выросла > ShortPumpPct% за последние 15 минут
//  2. Новая Ask-стена (15–20 сек) на Ask, не убывает
//  3. CD_1m < 0 И текущая сделка — «кит» Sell
//  4. Дивергенция: VolumeCluster_3m < VolumeCluster_1h_avg (объём падает, цена выросла)
//
// Требует маржинального / фьючерсного аккаунта.
func (s *Service) checkShortSignal(symbol string, tradeUSDT float64, ob *orderbook.OrderBook, m *SymbolMetrics, wt *WallTracker) {
	defer utils.TimeTracker(s.log, "checkShortSignal microscalping")()

	if time.Now().Before(s.warmupUntil) {
		return
	}
	if s.isNoTradeWindow() {
		return
	}

	ask0Price, err1 := strconv.ParseFloat(ob.Asks[0].Price, 64)
	bid0Price, err2 := strconv.ParseFloat(ob.Bids[0].Price, 64)
	if err1 != nil || err2 != nil || ask0Price <= 0 || bid0Price <= 0 {
		return
	}

	avgTradeSizeUSDT := m.AvgTradeSizeUSDT()

	// Условие 1: локальный перегрев за 15 минут
	pumpThreshold := s.cfg.ShortPumpPct
	if symbolType(symbol) == "alt" {
		pumpThreshold = s.cfg.ShortPumpAltPct
	}
	changePct := m.PriceChangePct15m()
	if changePct <= pumpThreshold {
		return
	}

	// Условие 2: новая Ask-стена (minAgeSec..20сек), не убывает
	newWall := wt.GetNewAskWall(bid0Price, s.cfg.WallMinAgeSec, 20)
	if newWall == nil {
		s.log.Debug("microscalping short: no new stable ask wall",
			zap.String("symbol", symbol),
			zap.Float64("price_change_pct", changePct),
		)
		return
	}

	// Условие 3: CD_1m < 0 И кит Sell
	cd1m := m.CD1m()
	if cd1m >= 0 {
		s.log.Debug("microscalping short: CD1m is positive",
			zap.String("symbol", symbol),
			zap.Float64("cd1m", cd1m),
		)
		return
	}
	if !s.isWhale(symbol, tradeUSDT, avgTradeSizeUSDT, ob) {
		return
	}

	// Условие 4: дивергенция — VolumeCluster падает
	vc3m := m.VolumeCluster3m()
	vc1hAvg := m.VolumeCluster1hAvg()
	if vc3m >= vc1hAvg {
		s.log.Debug("microscalping short: no volume divergence",
			zap.String("symbol", symbol),
			zap.Float64("vc_3m", vc3m),
			zap.Float64("vc_1h_avg", vc1hAvg),
		)
		return
	}

	s.log.Info("microscalping short: all conditions met, attempting entry",
		zap.String("symbol", symbol),
		zap.Float64("price_change_pct_15m", changePct),
		zap.Float64("pump_threshold", pumpThreshold),
		zap.Float64("new_wall_price", newWall.Price),
		zap.Float64("cd1m", cd1m),
		zap.Float64("whale_sell_usdt", tradeUSDT),
		zap.Float64("vc_3m", vc3m),
		zap.Float64("vc_1h_avg", vc1hAvg),
	)

	tpPrice, _ := s.calcTPPrice(symbol, bid0Price, true)
	// Для шорта SupportWall используется как уровень resistance для SL (Ask-стена)
	s.tryEnter(symbol, "short", bid0Price, tpPrice, newWall.Price)
}

// tryEnter пытается открыть позицию при прохождении всех фильтров.
// side: "long" или "short".
// supportWall: для лонга — Bid-стена поддержки (SL); для шорта — уровень резистенции (SL).
func (s *Service) tryEnter(symbol, side string, entryPrice, tpPrice, supportWall float64) {
	// 1. Cooldown
	s.mu.Lock()
	if last, ok := s.cooldowns[symbol]; ok {
		cooldownDur := time.Duration(s.cfg.CooldownSec) * time.Second
		if time.Since(last) < cooldownDur {
			remaining := cooldownDur - time.Since(last)
			s.mu.Unlock()
			s.log.Debug("microscalping: cooldown active",
				zap.String("symbol", symbol),
				zap.String("side", side),
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

	// Защита: TP должен быть на правильной стороне от entry
	if side == "long" && tpPrice <= entryPrice {
		tpPrice = entryPrice * (1 + s.cfg.TPPctBTCETHMin/100)
		s.log.Warn("microscalping: tp <= entry (long), adjusted",
			zap.String("symbol", symbol),
			zap.Float64("tp_price", tpPrice),
		)
	}
	if side == "short" && tpPrice >= entryPrice {
		tpPrice = entryPrice * (1 - s.cfg.TPPctBTCETHMin/100)
		s.log.Warn("microscalping: tp >= entry (short), adjusted",
			zap.String("symbol", symbol),
			zap.Float64("tp_price", tpPrice),
		)
	}

	qty := s.cfg.TradeAmountUSDT / entryPrice
	if qty <= 0 {
		return
	}

	tradeSide := "buy"
	if side == "short" {
		tradeSide = "sell"
	}

	slPrice := float64(0)
	if side == "long" {
		hardSL := entryPrice * (1 - s.cfg.StopLossPct/100)
		slPrice = hardSL
		if supportWall > 0 && supportWall < entryPrice && supportWall > hardSL {
			slPrice = supportWall // используем стену если она выше hard SL
		}
	} else {
		slPrice = entryPrice * (1 + s.cfg.StopLossPct/100)
	}

	s.log.Info("microscalping: opening trade",
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.String("exchange", s.cfg.Exchange),
		zap.Float64("entry_price", entryPrice),
		zap.Float64("tp_price", tpPrice),
		zap.Float64("sl_price", slPrice),
		zap.Float64("support_wall", supportWall),
		zap.Float64("qty", qty),
	)

	id, err := s.tradeSvc.OpenTrade(s.ctx, trade.Trade{
		Strategy:      "microscalping",
		TradeExchange: s.cfg.Exchange,
		Symbol:        symbol,
		Side:          tradeSide,
		Qty:           qty,
		EntryPrice:    entryPrice,
		TargetPrice:   &tpPrice,
		StopLossPrice: &slPrice,
	})
	if err != nil {
		s.log.Error("microscalping: open trade failed",
			zap.String("symbol", symbol),
			zap.String("side", side),
			zap.Error(err),
		)
		s.mu.Lock()
		s.cooldowns[symbol] = time.Now()
		s.mu.Unlock()
		return
	}

	t := &MicroscalpingTrade{
		ID:           id,
		Symbol:       symbol,
		Exchange:     s.cfg.Exchange,
		Side:         side,
		EntryPrice:   entryPrice,
		TPPrice:      tpPrice,
		SupportWall:  supportWall,
		Qty:          qty,
		OpenedAt:     time.Now(),
		HighestPrice: entryPrice,
		PriceCh:      make(chan float64, 512),
		CrashCh:      make(chan struct{}, 1),
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

// OnTicker получает тикеры: передаёт цену в горутины активных сделок и обновляет историю цен.
func (s *Service) OnTicker(t ticker.Ticker) {
	defer utils.TimeTracker(s.log, "OnTicker microscalping service")()

	price, err := strconv.ParseFloat(t.Price, 64)
	if err != nil || price <= 0 {
		return
	}

	// Обновляем историю цен для шорт-сигнала (PriceChangePct15m)
	m := s.getOrCreateMetrics(t.Symbol)
	m.OnPrice(price)

	tr, ok := s.tracker.Get(t.Symbol)
	if !ok || t.Exchange != tr.Exchange {
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

// watchTrade мониторит открытую сделку до выхода по одному из условий:
//   - TP: цена достигла цели И Ask-стена стабильна (для лонга) / цена достигла цели (для шорта)
//   - SL: цена упала ниже Bid-стены поддержки или hard SL (лонг) / поднялась выше hard SL (шорт)
//   - Trailing stop (только для лонга)
//   - Timeout: MaxPositionDurationH часов
//   - Crash-событие от detector
func (s *Service) watchTrade(t *MicroscalpingTrade) {
	maxDur := time.Duration(s.cfg.MaxPositionDurationH) * time.Hour
	timeout := time.NewTimer(maxDur)
	defer timeout.Stop()

	tpCheck := time.NewTicker(time.Duration(s.cfg.TPCheckIntervalSec) * time.Second)
	defer tpCheck.Stop()

	hardSL := float64(0)
	if t.Side == "long" {
		hardSL = t.EntryPrice * (1 - s.cfg.StopLossPct/100)
	} else {
		hardSL = t.EntryPrice * (1 + s.cfg.StopLossPct/100)
	}

	s.log.Info("microscalping: watching trade",
		zap.Int64("id", t.ID),
		zap.String("symbol", t.Symbol),
		zap.String("side", t.Side),
		zap.Float64("entry_price", t.EntryPrice),
		zap.Float64("tp_price", t.TPPrice),
		zap.Float64("hard_sl", hardSL),
		zap.Float64("support_wall", t.SupportWall),
		zap.Duration("max_duration", maxDur),
	)

	lastPrice := t.EntryPrice
	slLevel := hardSL // текущий SL уровень (обновляется по стенам)
	if t.Side == "long" && t.SupportWall > 0 && t.SupportWall > hardSL {
		slLevel = t.SupportWall
	}

	for {
		select {
		case <-s.ctx.Done():
			return

		case <-timeout.C:
			s.log.Warn("microscalping: timeout, force closing",
				zap.Int64("id", t.ID),
				zap.String("symbol", t.Symbol),
				zap.String("side", t.Side),
				zap.Float64("last_price", lastPrice),
				zap.Int("hold_h", int(time.Since(t.OpenedAt).Hours())),
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

		case <-tpCheck.C:
			if t.Side == "long" {
				// Обновляем SL по Bid-стене
				wt := s.getOrCreateWallTracker(t.Symbol)
				if suppWall := wt.GetBidSupportWall(lastPrice, s.cfg.WallMinAgeSec); suppWall != nil {
					newSL := suppWall.Price
					if newSL > hardSL && newSL < lastPrice {
						if newSL > slLevel {
							slLevel = newSL
							s.log.Debug("microscalping: SL updated via bid wall",
								zap.Int64("id", t.ID),
								zap.String("symbol", t.Symbol),
								zap.Float64("new_sl", slLevel),
							)
						}
					}
				}

				// TP: цена достигла цели И Ask-стена стабильна > TPWallStableSec
				if lastPrice >= t.TPPrice {
					wt2 := s.getOrCreateWallTracker(t.Symbol)
					stableWall := wt2.GetStableAskWall(lastPrice*0.999, s.cfg.TPWallStableSec)
					if stableWall != nil {
						exitPrice := stableWall.Price - s.cfg.TPWallOffset
						if exitPrice <= 0 {
							exitPrice = lastPrice
						}
						s.log.Info("microscalping: TP triggered (stable ask wall)",
							zap.Int64("id", t.ID),
							zap.String("symbol", t.Symbol),
							zap.Float64("price", lastPrice),
							zap.Float64("wall_price", stableWall.Price),
							zap.Float64("exit_price", exitPrice),
							zap.Float64("wall_age_sec", stableWall.Age().Seconds()),
						)
						s.closeTrade(t, exitPrice, "tp")
						return
					}
				}
			}

		case price := <-t.PriceCh:
			lastPrice = price

			if t.Side == "long" {
				// Обновляем максимум для trailing stop
				if price > t.HighestPrice {
					t.HighestPrice = price
				}

				// Trailing stop: активируем при росте на TrailActivationPct%
				trailActivation := t.EntryPrice * (1 + s.cfg.TrailActivationPct/100)
				if price >= trailActivation {
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

				// Trailing SL
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

				// SL по стене / hard SL
				if price <= slLevel {
					s.log.Warn("microscalping: SL hit (long)",
						zap.Int64("id", t.ID),
						zap.String("symbol", t.Symbol),
						zap.Float64("price", price),
						zap.Float64("sl_level", slLevel),
						zap.Float64("support_wall", t.SupportWall),
					)
					s.closeTrade(t, price, "sl")
					return
				}

				// TP (быстрая проверка в price loop — стену проверяем в tpCheck)
				if t.TPPrice > 0 && price >= t.TPPrice {
					// Не закрываем сразу — ждём подтверждения стеной в tpCheck
					// Но если прошло более 30 сек с момента достижения цели — закрываем без стены
					s.log.Debug("microscalping: TP target reached, waiting for wall confirmation",
						zap.Int64("id", t.ID),
						zap.String("symbol", t.Symbol),
						zap.Float64("price", price),
						zap.Float64("tp_price", t.TPPrice),
					)
				}

			} else {
				// SHORT: SL если цена выросла выше hard SL
				if price >= hardSL {
					s.log.Warn("microscalping: SL hit (short)",
						zap.Int64("id", t.ID),
						zap.String("symbol", t.Symbol),
						zap.Float64("price", price),
						zap.Float64("hard_sl", hardSL),
					)
					s.closeTrade(t, price, "sl")
					return
				}

				// SHORT TP: цена упала до целевого уровня
				if t.TPPrice > 0 && price <= t.TPPrice {
					s.log.Info("microscalping: TP hit (short)",
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

	var pnl float64
	if t.Side == "long" {
		pnl = (exitPrice - t.EntryPrice) * t.Qty
	} else {
		pnl = (t.EntryPrice - exitPrice) * t.Qty
	}

	s.log.Info("microscalping: trade closed",
		zap.Int64("id", t.ID),
		zap.String("symbol", t.Symbol),
		zap.String("side", t.Side),
		zap.String("reason", reason),
		zap.Float64("entry_price", t.EntryPrice),
		zap.Float64("exit_price", exitPrice),
		zap.Float64("tp_price", t.TPPrice),
		zap.Float64("pnl_usdt", pnl),
		zap.Int("hold_sec", int(time.Since(t.OpenedAt).Seconds())),
	)
}
