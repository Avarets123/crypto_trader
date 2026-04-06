package arbitration

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/shared/comparator"
	"github.com/osman/bot-traider/internal/ticker"
	"github.com/osman/bot-traider/internal/trade"
)

const totalFeePct = 0.2 // суммарные комиссии Binance + Bybit (taker fee)

// Executor реализует Lead-Lag арбитраж: сигнал от Binance, торговля на Bybit.
type Service struct {
	mu          sync.Mutex
	cfg         Config
	ctx         context.Context
	tradeSvc    *trade.Service
	tracker     *PositionTracker
	log         *zap.Logger
	lastTrade   map[string]time.Time // cooldown по символу
	onTrade     func(result string)  // хук для stats
}

// New создаёт Service.
func New(ctx context.Context, cfg Config, tradeSvc *trade.Service,log *zap.Logger) *Service {
	log.Info("arb executor initialized",
		zap.Float64("min_spread_pct", cfg.MinSpreadPct),
		zap.Int("max_hold_sec", cfg.MaxHoldSec),
		zap.Float64("stop_loss_pct", cfg.StopLossPct),
	)
	return &Service{
		cfg:       cfg,
		ctx:       ctx,
		tradeSvc:  tradeSvc,
		tracker:   NewPositionTracker(),
		log:       log,
		lastTrade: make(map[string]time.Time),
	}
}

// WithOnTrade устанавливает хук для stats.
func (e *Service) WithOnTrade(fn func(result string)) {
	e.onTrade = fn
}

// OnSpreadOpen вызывается comparator при обнаружении спреда.
func (s *Service) OnSpreadOpen(event *comparator.SpreadEvent) {
	s.log.Warn("arbitration: spread event received",
		zap.String("symbol", event.Symbol),
		zap.String("exchange_high", event.ExchangeHigh),
		zap.String("exchange_low", event.ExchangeLow),
		zap.Float64("spread_pct", event.MaxSpreadPct),
	)

	// покупаем на бирже с низкой ценой (отстающей), сигнал — биржа с высокой ценой
	tradeExchange := event.ExchangeLow
	signalExchange := event.ExchangeHigh

	symbol := event.Symbol

	// проверяем эффективный спред
	effectiveSpread := event.MaxSpreadPct - totalFeePct
	if effectiveSpread < s.cfg.MinSpreadPct {
		s.log.Warn("arbitration order cancel: effective spread too small",
			zap.String("symbol", symbol),
			zap.Float64("effective_spread", effectiveSpread),
			zap.Float64("min_spread", s.cfg.MinSpreadPct),
		)
		return
	}

	// cooldown
	s.mu.Lock()
	if last, ok := s.lastTrade[symbol]; ok {
		if time.Since(last) < time.Duration(s.cfg.CooldownSec)*time.Second {
			remaining := time.Duration(s.cfg.CooldownSec)*time.Second - time.Since(last)
			s.mu.Unlock()
			s.log.Debug("arbitration: cooldown active",
				zap.String("symbol", symbol),
				zap.Duration("remaining", remaining),
			)
			return
		}
	}
	s.mu.Unlock()

	// уже есть открытая позиция по этому символу
	if s.tracker.Has(symbol) {
		s.log.Warn("arbitration: position already open for symbol", zap.String("symbol", symbol))
		return
	}


	// покупаем по низкой цене, цель — высокая цена (биржа-сигнал)
	entryPrice := event.PriceLow
	targetPrice := event.PriceHigh
	stopLoss := entryPrice * (1 - s.cfg.StopLossPct/100)

	//взять из конфига
	qty := s.CalcQty(entryPrice, 10)

	if qty <= 0 {
		s.log.Warn("arbitration: qty is zero", zap.String("symbol", symbol))
		return
	}


	signalData, _ := json.Marshal(event)

	id, err := s.tradeSvc.OpenTrade(s.ctx, trade.Trade{
		Strategy:       "arbitration",
		SignalExchange: signalExchange,
		TradeExchange:  tradeExchange,
		Symbol:         symbol,
		Qty:            qty,
		EntryPrice:     entryPrice,
		TargetPrice:    &targetPrice,
		StopLossPrice:  &stopLoss,
		SignalData:     signalData,
	})
	if err != nil {
		s.log.Error("arb: open position failed",
			zap.String("symbol", symbol),
			zap.String("trade_exchange", tradeExchange),
			zap.Error(err),
		)
		// ставим cooldown чтобы не повторять немедленно
		s.mu.Lock()
		s.lastTrade[symbol] = time.Now()
		s.mu.Unlock()
		return
	}

	s.log.Info("arb trade opened",
		zap.Int64("id", id),
		zap.String("symbol", symbol),
		zap.String("trade_exchange", tradeExchange),
		zap.Float64("entry_price", entryPrice),
		zap.Float64("target_price", targetPrice),
		zap.Float64("stop_loss", stopLoss),
	)

	// обновляем cooldown
	s.mu.Lock()
	s.lastTrade[symbol] = time.Now()
	s.mu.Unlock()

	// регистрируем позицию для мониторинга
	pos := &ArbPosition{
		ID:            id,
		Symbol:        symbol,
		TradeExchange: tradeExchange,
		SignalExchange: signalExchange,
		EntryPrice:    entryPrice,
		TargetPrice:   targetPrice,
		StopLosPrice:  stopLoss,
		Qty:           qty,
		OpenedAt:      time.Now(),
		PriceCh:       make(chan float64, 20),
	}
	s.tracker.Add(pos)

	go s.watchArb(pos)
}

// OnTicker получает тикеры и направляет цену активным позициям по нужной бирже.
func (s *Service) OnTicker(t ticker.Ticker) {
	price, err := strconv.ParseFloat(t.Price, 64)
	if err != nil || price <= 0 {
		return
	}

	pos, ok := s.tracker.GetBySymbol(t.Symbol)
	if !ok {
		return
	}

	// следим только за биржей где открыта позиция
	if t.Exchange != pos.TradeExchange {
		return
	}

	select {
	case pos.PriceCh <- price:
	default:
	}
}

// watchArb мониторит позицию до TP/SL/timeout.
func (s *Service) watchArb(pos *ArbPosition) {
	defer s.tracker.Remove(pos.Symbol)

	timeout := time.NewTimer(time.Duration(s.cfg.MaxHoldSec) * time.Second)
	defer timeout.Stop()

	s.log.Info("arbitration: watching position",
		zap.Int64("id", pos.ID),
		zap.String("symbol", pos.Symbol),
		zap.Float64("entry_price", pos.EntryPrice),
		zap.Float64("target_price", pos.TargetPrice),
		zap.Float64("stop_loss", pos.StopLosPrice),
	)

	for {
		select {
		case <-s.ctx.Done():
			return

		case <-timeout.C:
			holdSec := int(time.Since(pos.OpenedAt).Seconds())
			s.log.Warn("arbitration: timeout, force close",
				zap.Int64("id", pos.ID),
				zap.String("symbol", pos.Symbol),
				zap.Int("hold_sec", holdSec),
			)
			s.closePosition(pos, pos.EntryPrice, "timeout")
			return

		case price := <-pos.PriceCh:
			if price >= pos.TargetPrice {
				s.log.Info("arbitration tp hit",
					zap.Int64("id", pos.ID),
					zap.String("symbol", pos.Symbol),
					zap.Float64("exit_price", price),
					zap.Float64("target", pos.TargetPrice),
				)
				s.closePosition(pos, price, "tp")
				return
			}
			if price <= pos.StopLosPrice {
				s.log.Warn("arbitration sl hit",
					zap.Int64("id", pos.ID),
					zap.String("symbol", pos.Symbol),
					zap.Float64("exit_price", price),
					zap.Float64("stop_loss", pos.StopLosPrice),
				)
				s.closePosition(pos, price, "sl")
				return
			}
		}
	}
}

// closePosition закрывает позицию и фиксирует результат.
func (s *Service) closePosition(pos *ArbPosition, exitPrice float64, reason string) {
	err := s.tradeSvc.CloseTrade(
		s.ctx, pos.ID, exitPrice, reason,
	)
	if err != nil {
		s.log.Error("arbitration: close position failed",
			zap.Int64("id", pos.ID),
			zap.Error(err),
		)
		return
	}

	pnl := (exitPrice - pos.EntryPrice) * pos.Qty

	s.log.Info("arbitration position closed",
		zap.Int64("id", pos.ID),
		zap.String("symbol", pos.Symbol),
		zap.String("reason", reason),
		zap.Float64("exit_price", exitPrice),
		zap.Float64("pnl_usdt", pnl),
	)

	if s.onTrade != nil {
		s.onTrade(reason)
	}

	
}


func (s *Service) CalcQty(price float64, tradeAmountUsdt float64) float64 {
	if price <= 0 {
		return 0
	}

	qty := tradeAmountUsdt / price
	s.log.Debug("risk: calc qty",
		zap.Float64("trade_amount_usdt", tradeAmountUsdt),
		zap.Float64("price", price),
		zap.Float64("qty", qty),
	)
	return qty
}