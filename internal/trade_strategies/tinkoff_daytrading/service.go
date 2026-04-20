package tinkoff_daytrading

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/exchange_orders"
	"github.com/osman/bot-traider/internal/orderbook"
	"github.com/osman/bot-traider/internal/shared/telegram"
	"github.com/osman/bot-traider/internal/trade"
)

const (
	spreadFilterPct    = 0.0015
	hardSLPct          = 0.005
	cooldownAfterClose = 15 * time.Second
	wallTradeLookback  = 2 * time.Second
	recentTradesPrune  = 5 * time.Second

	longTPImbalance     = 1.2
	shortTPImbalance    = 0.833
	longEntryImbalance  = 1.5
	shortEntryImbalance = 0.667
	deltaChangeMin      = 0.30
)

// symbolState — состояние стратегии по одному символу.
type symbolState struct {
	mu      sync.Mutex
	metrics *SymbolMetrics
	sigWin  *SignalWindow

	inPosition    bool
	side          string  // "buy" | "sell"
	entryPrice    float64
	qty           float64
	tradeID       int64
	entryBidWall  float64
	entryAskWall  float64
	openedAt      time.Time
	cooldownUntil time.Time

	// price → время последней сделки (для определения исчезновения стены)
	recentTrades map[float64]time.Time
}

const warmupDuration = 5 * time.Minute

// Service — основной сервис стратегии дейтрейдинга Тинькофф.
type Service struct {
	mu          sync.RWMutex
	cfg         Config
	ctx         context.Context
	tradeSvc    *trade.Service
	tg          *telegram.Notifier
	tgThread    int
	log         *zap.Logger
	warmupUntil time.Time

	symbols map[string]*symbolState
}

func newService(
	ctx context.Context,
	cfg Config,
	tradeSvc *trade.Service,
	tg *telegram.Notifier,
	tgThread int,
	log *zap.Logger,
) *Service {
	warmupUntil := time.Now().Add(warmupDuration)
	log.Info("tinkoff daytrading: warmup started, trades blocked",
		zap.Duration("duration", warmupDuration),
		zap.Time("until", warmupUntil),
	)
	return &Service{
		cfg:         cfg,
		ctx:         ctx,
		tradeSvc:    tradeSvc,
		tg:          tg,
		tgThread:    tgThread,
		log:         log,
		warmupUntil: warmupUntil,
		symbols:     make(map[string]*symbolState),
	}
}

// OnSymbolsChanged реагирует на изменение топ-листа тинькофф-провайдера.
func (s *Service) OnSymbolsChanged(added, removed []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, sym := range added {
		if _, ok := s.symbols[sym]; !ok {
			s.symbols[sym] = &symbolState{
				metrics:      newSymbolMetrics(),
				sigWin:       newSignalWindow(),
				recentTrades: make(map[float64]time.Time),
			}
			s.log.Info("tinkoff daytrading: tracking symbol", zap.String("symbol", sym))
		}
	}
	for _, sym := range removed {
		st, ok := s.symbols[sym]
		if !ok {
			continue
		}
		st.mu.Lock()
		inPos := st.inPosition
		st.mu.Unlock()
		if !inPos {
			delete(s.symbols, sym)
			s.log.Info("tinkoff daytrading: stopped tracking", zap.String("symbol", sym))
		}
		// Если есть открытая позиция — оставляем символ до закрытия
	}
}

// OnTrade вызывается при каждой сделке из Tinkoff-стрима.
func (s *Service) OnTrade(o exchange_orders.ExchangeOrder) {
	s.mu.RLock()
	st, ok := s.symbols[o.Symbol]
	s.mu.RUnlock()
	if !ok {
		return
	}

	st.metrics.OnTrade(o)
	snap := st.metrics.Snapshot()
	now := time.Now()

	st.mu.Lock()

	// Обновить карту недавних сделок (для SL по исчезновению стены)
	if price := parsePrice(o.Price); price > 0 {
		st.recentTrades[price] = o.TradeTime
		pruneRecentTrades(st.recentTrades, o.TradeTime)
	}

	if st.inPosition {
		reason := exitReasonTrade(st, snap)
		if reason != "" {
			exitPrice := exitPriceFor(st.side, snap)
			st.mu.Unlock()
			go s.closePosition(o.Symbol, reason, exitPrice)
			return
		}
		st.mu.Unlock()
		return
	}

	if now.Before(st.cooldownUntil) {
		st.mu.Unlock()
		return
	}
	st.mu.Unlock()

	s.updateSignalWindow(st, snap, now)
	s.checkEntry(o.Symbol, st, snap, now)
}

// OnOrderBook вызывается при каждом обновлении стакана из Tinkoff-стрима.
func (s *Service) OnOrderBook(ob *orderbook.OrderBook) {
	s.mu.RLock()
	st, ok := s.symbols[ob.Symbol]
	s.mu.RUnlock()
	if !ok {
		return
	}

	st.metrics.OnOrderBook(ob)
	snap := st.metrics.Snapshot()
	now := time.Now()

	st.mu.Lock()

	if st.inPosition {
		var reason string
		var exitPrice float64

		switch {
		case st.side == "buy" && st.entryBidWall > 0:
			tradeAtWall := hasRecentTradeAt(st.recentTrades, st.entryBidWall, now)
			if wallDisappeared(st.entryBidWall, true, ob, tradeAtWall) {
				reason = "sl_wall"
				exitPrice = snap.BidPrice
			}
		case st.side == "sell" && st.entryAskWall > 0:
			tradeAtWall := hasRecentTradeAt(st.recentTrades, st.entryAskWall, now)
			if wallDisappeared(st.entryAskWall, false, ob, tradeAtWall) {
				reason = "sl_wall"
				exitPrice = snap.AskPrice
			}
		}

		st.mu.Unlock()
		if reason != "" {
			go s.closePosition(ob.Symbol, reason, exitPrice)
		}
		return
	}

	if now.Before(st.cooldownUntil) {
		st.mu.Unlock()
		return
	}
	st.mu.Unlock()

	s.updateSignalWindow(st, snap, now)
	s.checkEntry(ob.Symbol, st, snap, now)
}

func (s *Service) updateSignalWindow(st *symbolState, snap MetricSnapshot, now time.Time) {
	// Лонг
	imbalanceLong := snap.Imbalance >= longEntryImbalance
	whaleBuy := snap.LastWhale != nil &&
		snap.LastWhale.side == "buy" &&
		now.Sub(snap.LastWhale.t) <= whaleWindow
	deltaLong := snap.Delta1m > 0 && snap.DeltaChange > deltaChangeMin
	st.sigWin.UpdateLong(imbalanceLong, whaleBuy, deltaLong, now)

	// Шорт
	imbalanceShort := snap.Imbalance <= shortEntryImbalance
	whaleSell := snap.LastWhale != nil &&
		snap.LastWhale.side == "sell" &&
		now.Sub(snap.LastWhale.t) <= whaleWindow
	deltaShort := snap.Delta1m < 0 && snap.DeltaChange > deltaChangeMin
	st.sigWin.UpdateShort(imbalanceShort, whaleSell, deltaShort, now)
}

func (s *Service) checkEntry(symbol string, st *symbolState, snap MetricSnapshot, now time.Time) {
	if now.Before(s.warmupUntil) {
		s.log.Debug("tinkoff daytrading: warmup, skipping entry",
			zap.String("symbol", symbol),
			zap.Duration("remaining", time.Until(s.warmupUntil)),
		)
		return
	}

	if spreadBlocked(snap) {
		s.log.Debug("tinkoff daytrading: spread filter blocked",
			zap.String("symbol", symbol),
			zap.Float64("spread_pct", (snap.AskPrice-snap.BidPrice)/snap.BidPrice*100),
		)
		return
	}

	if st.sigWin.IsLong(now) {
		go s.openPosition(symbol, "buy", snap)
	} else if st.sigWin.IsShort(now) {
		go s.openPosition(symbol, "sell", snap)
	}
}

func (s *Service) openPosition(symbol, side string, snap MetricSnapshot) {
	s.mu.RLock()
	st, ok := s.symbols[symbol]
	s.mu.RUnlock()
	if !ok {
		return
	}

	st.mu.Lock()
	if st.inPosition || time.Now().Before(st.cooldownUntil) {
		st.mu.Unlock()
		return
	}
	// Оптимистичная блокировка — предотвращаем дублирующие входы
	st.inPosition = true
	st.mu.Unlock()

	entryPrice := snap.AskPrice
	if side == "sell" {
		entryPrice = snap.BidPrice
	}

	id, err := s.tradeSvc.OpenTrade(s.ctx, trade.Trade{
		Strategy:       "tinkoff_daytrading",
		SignalExchange: "tinkoff",
		TradeExchange:  "tinkoff",
		Symbol:         symbol,
		Side:           side,
		Qty:            s.cfg.LotLimit,
		EntryPrice:     entryPrice,
	})
	if err != nil {
		st.mu.Lock()
		st.inPosition = false
		st.mu.Unlock()
		s.log.Error("tinkoff daytrading: open trade failed",
			zap.String("symbol", symbol),
			zap.String("side", side),
			zap.Error(err),
		)
		return
	}

	// Берём свежий снимок метрик для проверки условий выхода
	freshSnap := st.metrics.Snapshot()

	st.mu.Lock()
	st.side = side
	st.entryPrice = entryPrice
	st.qty = s.cfg.LotLimit
	st.tradeID = id
	st.entryBidWall = snap.BidWall
	st.entryAskWall = snap.AskWall
	st.openedAt = time.Now()
	st.sigWin.Reset()
	// Немедленно проверяем условия выхода: пока API-вызов выполнялся, рынок мог измениться
	immediateReason := exitReasonTrade(st, freshSnap)
	st.mu.Unlock()

	if immediateReason != "" {
		s.log.Warn("tinkoff daytrading: immediate exit after open",
			zap.String("symbol", symbol),
			zap.String("reason", immediateReason),
		)
		go s.closePosition(symbol, immediateReason, exitPriceFor(side, freshSnap))
	}

	s.log.Info("tinkoff daytrading: position opened",
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.Float64("price", entryPrice),
		zap.Float64("imbalance", snap.Imbalance),
		zap.Float64("delta1m", snap.Delta1m),
		zap.Float64("delta_change", snap.DeltaChange),
		zap.Int64("trade_id", id),
	)
	if s.tg != nil {
		sideEmoji, sideLabel := "🟢", "ЛОНГ"
		if side == "sell" {
			sideEmoji, sideLabel = "🔴", "ШОРТ"
		}
		deltaSign := "+"
		if snap.Delta1m < 0 {
			deltaSign = ""
		}
		msg := fmt.Sprintf(
			"%s <b>Tinkoff DayTrading — %s</b>\n"+
				"📊 <b>%s</b> | Вход: <b>%.2f ₽</b>\n"+
				"Дисбаланс: %.2f | Объём 1м: %s%.0f ₽ | Давление: %.0f%%",
			sideEmoji, sideLabel, symbol, entryPrice,
			snap.Imbalance, deltaSign, snap.Delta1m, snap.DeltaChange*100,
		)
		s.tg.SendToThread(s.ctx, msg, s.tgThread)
	}
}

func (s *Service) closePosition(symbol, reason string, exitPrice float64) {
	s.mu.RLock()
	st, ok := s.symbols[symbol]
	s.mu.RUnlock()
	if !ok {
		return
	}

	st.mu.Lock()
	if !st.inPosition {
		st.mu.Unlock()
		return
	}
	tradeID := st.tradeID
	if tradeID == 0 {
		// openPosition ещё не завершил API-вызов — следующий тик повторит попытку
		st.mu.Unlock()
		s.log.Warn("tinkoff daytrading: close skipped, trade still opening",
			zap.String("symbol", symbol),
			zap.String("reason", reason),
		)
		return
	}
	side := st.side
	entryPrice := st.entryPrice
	openedAt := st.openedAt
	qty := st.qty
	st.mu.Unlock()

	const closeMaxAttempts = 5
	var closeErr error
	for attempt := 0; attempt < closeMaxAttempts; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt) * 3 * time.Second
			s.log.Warn("tinkoff daytrading: close retry",
				zap.String("symbol", symbol),
				zap.Int("attempt", attempt+1),
				zap.Duration("delay", delay),
			)
			time.Sleep(delay)
		}
		closeErr = s.tradeSvc.CloseTrade(s.ctx, tradeID, exitPrice, reason)
		if closeErr == nil {
			break
		}
		s.log.Error("tinkoff daytrading: close trade failed",
			zap.String("symbol", symbol),
			zap.Int64("trade_id", tradeID),
			zap.String("reason", reason),
			zap.Int("attempt", attempt+1),
			zap.Error(closeErr),
		)
	}
	if closeErr != nil {
		// После всех попыток сбрасываем позицию чтобы стратегия не зависла
		st.mu.Lock()
		st.inPosition = false
		st.cooldownUntil = time.Now().Add(cooldownAfterClose)
		st.mu.Unlock()
		s.log.Error("tinkoff daytrading: close trade exhausted all attempts, position reset",
			zap.String("symbol", symbol),
			zap.Int64("trade_id", tradeID),
			zap.Error(closeErr),
		)
		return
	}

	pnlPct := 0.0
	if entryPrice > 0 {
		if side == "buy" {
			pnlPct = (exitPrice - entryPrice) / entryPrice * 100
		} else {
			pnlPct = (entryPrice - exitPrice) / entryPrice * 100
		}
	}

	st.mu.Lock()
	st.inPosition = false
	st.cooldownUntil = time.Now().Add(cooldownAfterClose)
	st.sigWin.Reset()
	st.mu.Unlock()

	s.log.Info("tinkoff daytrading: position closed",
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.String("reason", reason),
		zap.Float64("entry", entryPrice),
		zap.Float64("exit", exitPrice),
		zap.Float64("pnl_pct", pnlPct),
	)
	if s.tg != nil {
		emoji, reasonLabel := exitEmoji(reason)
		holdStr := formatHold(time.Since(openedAt))
		pnlSign := "+"
		if pnlPct < 0 {
			pnlSign = ""
		}
		pnlRub := (exitPrice - entryPrice) * qty
		if side == "sell" {
			pnlRub = (entryPrice - exitPrice) * qty
		}
		msg := fmt.Sprintf(
			"%s <b>Tinkoff DayTrading — %s</b>\n"+
				"📊 <b>%s</b> | %.2f → %.2f ₽\n"+
				"PnL: <b>%s%.2f ₽</b> (%s%.2f%%) | Держание: %s",
			emoji, reasonLabel, symbol, entryPrice, exitPrice,
			pnlSign, pnlRub, pnlSign, pnlPct, holdStr,
		)
		s.tg.SendToThread(s.ctx, msg, s.tgThread)
	}
}

// exitReasonTrade проверяет условия TP/SL на основе метрик сделки.
// Вызывается с уже удержанным st.mu.
func exitReasonTrade(st *symbolState, snap MetricSnapshot) string {
	if st.side == "buy" {
		if snap.Delta1m < 0 || snap.Imbalance < longTPImbalance {
			return "tp"
		}
		if snap.BidPrice > 0 && snap.BidPrice < st.entryPrice*(1-hardSLPct) {
			return "sl"
		}
	} else {
		if snap.Delta1m > 0 || snap.Imbalance > shortTPImbalance {
			return "tp"
		}
		if snap.AskPrice > 0 && snap.AskPrice > st.entryPrice*(1+hardSLPct) {
			return "sl"
		}
	}
	return ""
}

func exitPriceFor(side string, snap MetricSnapshot) float64 {
	if side == "sell" {
		return snap.AskPrice
	}
	return snap.BidPrice
}

func spreadBlocked(snap MetricSnapshot) bool {
	if snap.BidPrice <= 0 || snap.AskPrice <= 0 {
		return false
	}
	return (snap.AskPrice-snap.BidPrice)/snap.BidPrice > spreadFilterPct
}

func hasRecentTradeAt(trades map[float64]time.Time, price float64, now time.Time) bool {
	for p, t := range trades {
		if math.Abs(p-price) < 0.0001 && now.Sub(t) <= wallTradeLookback {
			return true
		}
	}
	return false
}

func pruneRecentTrades(trades map[float64]time.Time, now time.Time) {
	for p, t := range trades {
		if now.Sub(t) > recentTradesPrune {
			delete(trades, p)
		}
	}
}

func parsePrice(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func exitEmoji(reason string) (emoji, label string) {
	switch reason {
	case "tp":
		return "✅", "TP"
	case "sl":
		return "🛑", "SL"
	case "sl_wall":
		return "🛑", "SL (стена)"
	case "timeout":
		return "⏱", "Timeout"
	default:
		return "🔴", strings.ToUpper(reason)
	}
}

func formatHold(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dч %dм", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dм %dс", m, s)
	}
	return fmt.Sprintf("%dс", s)
}
