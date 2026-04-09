package scalping

import (
	"context"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/shared/exchange"
	"github.com/osman/bot-traider/internal/ticker"
	"github.com/osman/bot-traider/internal/trade"
)

// Service реализует Scalping стратегию:
// вход по закрытой свече (EMA+RSI+BB сигнал) лимитным ордером,
// выход по TP/SL/trailing stop/timeout рыночным ордером.
type Service struct {
	mu      sync.Mutex
	cfg     Config
	ctx     context.Context
	tradeSvc *trade.Service
	client  exchange.RestClient
	tracker *TradeTracker
	provider *KlineProvider
	buffers  map[string]*CandleBuffer

	cooldowns    map[string]time.Time
	atrHistory   map[string][]float64 // rolling ATR values per symbol

	dailyLossUSDT float64
	dailyDate     string // "2006-01-02"

	cachedBalance    float64
	balanceFetchedAt time.Time

	log *zap.Logger
}

// New создаёт Service.
func New(ctx context.Context, cfg Config, tradeSvc *trade.Service, client exchange.RestClient, log *zap.Logger) *Service {
	log.Info("scalping strategy initialized",
		zap.String("exchange", cfg.Exchange),
		zap.Strings("symbols", cfg.Symbols),
		zap.String("interval", cfg.Interval),
		zap.Float64("trade_amount_pct", cfg.TradeAmountPct),
		zap.Float64("stop_loss_pct", cfg.StopLossPct),
		zap.Float64("tp_pct_btc", cfg.TakeProfitPctBTC),
		zap.Float64("tp_pct_alt", cfg.TakeProfitPctAlt),
	)

	buffers := make(map[string]*CandleBuffer, len(cfg.Symbols))
	for _, sym := range cfg.Symbols {
		buffers[sym] = NewCandleBuffer(cfg.CandleBufferSize)
	}

	return &Service{
		cfg:        cfg,
		ctx:        ctx,
		tradeSvc:   tradeSvc,
		client:     client,
		tracker:    NewTradeTracker(),
		provider:   NewKlineProvider(cfg.Exchange, log.With(zap.String("component", "kline-provider"))),
		buffers:    buffers,
		cooldowns:  make(map[string]time.Time),
		atrHistory: make(map[string][]float64),
		log:        log,
	}
}

// Start загружает исторические свечи и подписывается на kline-стримы.
func (s *Service) Start(ctx context.Context) error {
	for _, symbol := range s.cfg.Symbols {
		sym := symbol

		candles, err := s.provider.FetchHistory(ctx, sym, s.cfg.Interval, s.cfg.CandleBufferSize)
		if err != nil {
			s.log.Warn("scalping: failed to fetch history",
				zap.String("symbol", sym), zap.Error(err))
		} else {
			buf := s.buffers[sym]
			for _, c := range candles {
				buf.Push(c)
			}
			s.log.Info("scalping: history loaded",
				zap.String("symbol", sym), zap.Int("candles", len(candles)))
		}

		s.provider.Subscribe(ctx, sym, s.cfg.Interval, func(c Candle) {
			s.onCandle(sym, c)
		})
	}
	return nil
}

// OnTicker получает тикеры и направляет цену в активные сделки.
func (s *Service) OnTicker(t ticker.Ticker) {
	if !strings.EqualFold(t.Exchange, s.cfg.Exchange) {
		return
	}

	trade, ok := s.tracker.Get(t.Symbol)
	if !ok {
		return
	}

	price, err := strconv.ParseFloat(t.Price, 64)
	if err != nil || price <= 0 {
		return
	}

	select {
	case trade.PriceCh <- price:
	default:
		s.log.Warn("scalping: price channel full, tick dropped",
			zap.String("symbol", t.Symbol))
	}
}

// onCandle вызывается KlineProvider при получении свечи.
// Индикаторы пересчитываются только по закрытым свечам.
func (s *Service) onCandle(symbol string, c Candle) {
	buf, ok := s.buffers[symbol]
	if !ok {
		return
	}

	buf.Push(c)

	if !c.IsClosed {
		return // индикаторы считаем только по закрытым свечам
	}

	s.log.Debug("scalping: candle closed",
		zap.String("symbol", symbol),
		zap.Float64("close", c.Close),
		zap.Time("open_time", c.OpenTime),
	)

	snapshot := buf.Snapshot()
	minCandles := s.cfg.EMAPeriod + 1
	if len(snapshot) < minCandles {
		s.log.Debug("scalping: not enough candles for signal",
			zap.String("symbol", symbol),
			zap.Int("have", len(snapshot)),
			zap.Int("need", minCandles),
		)
		return
	}

	// 1. ATR-фильтр волатильности
	if !s.checkATRFilter(symbol, snapshot) {
		s.log.Debug("scalping: ATR filter failed", zap.String("symbol", symbol))
		return
	}

	// 2. Cooldown по символу
	s.mu.Lock()
	if last, ok := s.cooldowns[symbol]; ok {
		if time.Since(last) < time.Duration(s.cfg.CooldownSec)*time.Second {
			remaining := time.Duration(s.cfg.CooldownSec)*time.Second - time.Since(last)
			s.mu.Unlock()
			s.log.Debug("scalping: cooldown active",
				zap.String("symbol", symbol), zap.Duration("remaining", remaining))
			return
		}
	}
	s.mu.Unlock()

	// 3. Уже есть открытая сделка по символу
	if s.tracker.Has(symbol) {
		return
	}

	// 4. Дневной лимит убытков
	if !s.checkDailyLoss() {
		s.log.Warn("scalping: daily loss cap reached, skipping signal",
			zap.String("symbol", symbol),
			zap.Float64("daily_loss_usdt", s.dailyLossUSDT),
		)
		return
	}

	// 5. Вычисляем индикаторы
	closes := extractCloses(snapshot)
	highs := extractHighs(snapshot)
	lows := extractLows(snapshot)

	ema := EMA(closes, s.cfg.EMAPeriod)
	rsi := RSI(closes, s.cfg.RSIPeriod)
	_, _, bbLower := BollingerBands(closes, s.cfg.BBPeriod, s.cfg.BBStdDev)
	price := c.Close

	s.log.Debug("scalping: indicators",
		zap.String("symbol", symbol),
		zap.Float64("price", price),
		zap.Float64("ema", ema),
		zap.Float64("rsi", rsi),
		zap.Float64("bb_lower", bbLower),
	)

	// Обновляем ATR историю
	atr := ATR(highs, lows, closes, s.cfg.ATRPeriod)
	s.appendATRHistory(symbol, atr)

	// 5a. Основной сигнал входа
	signalEntry := price > ema && rsi < s.cfg.RSIOversold && price <= bbLower
	if !signalEntry {
		return
	}

	// 5b. Опциональный MACD-фильтр
	if s.cfg.MACDEnabled {
		_, _, hist1 := MACD(closes, 12, 26, 9)
		var hist2 float64
		if len(closes) > 1 {
			_, _, hist2 = MACD(closes[:len(closes)-1], 12, 26, 9)
		}
		if hist1 <= hist2 {
			s.log.Debug("scalping: MACD filter failed", zap.String("symbol", symbol))
			return
		}
	}

	s.log.Info("scalping: entry signal",
		zap.String("symbol", symbol),
		zap.Float64("price", price),
		zap.Float64("ema", ema),
		zap.Float64("rsi", rsi),
		zap.Float64("bb_lower", bbLower),
	)

	// 6. Получаем баланс и рассчитываем qty
	balance, err := s.getFreeUSDT()
	if err != nil {
		s.log.Warn("scalping: failed to get balance", zap.String("symbol", symbol), zap.Error(err))
		return
	}
	if balance <= 0 {
		s.log.Warn("scalping: zero USDT balance", zap.String("symbol", symbol))
		return
	}
	tradeUSDT := balance * s.cfg.TradeAmountPct / 100
	qty := tradeUSDT / price
	if qty <= 0 {
		s.log.Warn("scalping: calculated qty is zero", zap.String("symbol", symbol))
		return
	}

	// 7. Размещаем лимитный POST-ONLY ордер на входе
	result, err := s.client.PlaceLimitOrder(s.ctx, symbol, "BUY", qty, price, true)
	if err != nil {
		s.log.Warn("scalping: failed to place limit order",
			zap.String("symbol", symbol), zap.Float64("price", price), zap.Error(err))
		s.mu.Lock()
		s.cooldowns[symbol] = time.Now()
		s.mu.Unlock()
		return
	}

	s.log.Info("scalping: limit order placed",
		zap.String("symbol", symbol),
		zap.String("order_id", result.OrderID),
		zap.Float64("price", price),
		zap.Float64("qty", qty),
	)

	// Ставим cooldown
	s.mu.Lock()
	s.cooldowns[symbol] = time.Now()
	s.mu.Unlock()

	// 8. Запускаем горутину ожидания исполнения
	go s.waitForFill(symbol, result.OrderID, price, qty)
}

// waitForFill ожидает исполнения лимитного ордера.
// При исполнении регистрирует позицию и запускает watchTrade.
// При истечении таймаута — отменяет ордер.
func (s *Service) waitForFill(symbol, orderID string, signalPrice, qty float64) {
	timeout := time.After(time.Duration(s.cfg.LimitOrderTimeoutSec) * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return

		case <-timeout:
			// Таймаут: отменяем ордер
			s.log.Info("scalping: limit order timeout, cancelling",
				zap.String("symbol", symbol), zap.String("order_id", orderID))
			if err := s.client.CancelOrder(s.ctx, symbol, orderID); err != nil {
				s.log.Warn("scalping: cancel order failed",
					zap.String("order_id", orderID), zap.Error(err))
			}
			return

		case <-ticker.C:
			// Проверяем остался ли ордер в open orders
			orders, err := s.client.GetOpenOrders(s.ctx, symbol)
			if err != nil {
				s.log.Warn("scalping: GetOpenOrders failed",
					zap.String("symbol", symbol), zap.Error(err))
				continue
			}

			filled := true
			for _, o := range orders {
				if o.OrderID == orderID {
					filled = false
					break
				}
			}

			if !filled {
				continue // ещё не исполнен
			}

			// Ордер исполнен
			fillPrice := signalPrice // для limit-order fillPrice = signalPrice

			// Проверяем проскальзывание
			slippage := math.Abs(fillPrice-signalPrice) / signalPrice * 100
			if slippage > s.cfg.MaxSlippagePct {
				s.log.Warn("scalping: slippage too high, rejecting fill",
					zap.String("symbol", symbol),
					zap.Float64("fill_price", fillPrice),
					zap.Float64("signal_price", signalPrice),
					zap.Float64("slippage_pct", slippage),
				)
				// Продаём по рынку чтобы закрыть нежелательную позицию
				s.client.PlaceMarketOrder(s.ctx, symbol, "SELL", qty) //nolint:errcheck
				return
			}

			// Регистрируем позицию в trade.Service
			tpPct := s.takeProfitPct(symbol)
			tpPrice := fillPrice * (1 + tpPct/100)
			slPrice := fillPrice * (1 - s.cfg.StopLossPct/100)

			id, err := s.tradeSvc.OpenTrade(s.ctx, trade.Trade{
				Strategy:      "scalping",
				TradeExchange: s.cfg.Exchange,
				SignalExchange: s.cfg.Exchange,
				Symbol:        symbol,
				Qty:           qty,
				EntryPrice:    fillPrice,
				TargetPrice:   &tpPrice,
				StopLossPrice: &slPrice,
			})
			if err != nil {
				s.log.Error("scalping: failed to register trade",
					zap.String("symbol", symbol), zap.Error(err))
				return
			}

			scalpTrade := &ScalpTrade{
				ID:           id,
				Symbol:       symbol,
				Exchange:     s.cfg.Exchange,
				EntryPrice:   fillPrice,
				Qty:          qty,
				PeakPrice:    fillPrice,
				TakeProfit:   tpPrice,
				StopLoss:     slPrice,
				OpenedAt:     time.Now(),
				LimitOrderID: orderID,
				PriceCh:      make(chan float64, 64),
			}
			s.tracker.Add(scalpTrade)

			s.log.Info("scalping: trade opened",
				zap.String("symbol", symbol),
				zap.Int64("id", id),
				zap.Float64("entry_price", fillPrice),
				zap.Float64("qty", qty),
				zap.Float64("take_profit", tpPrice),
				zap.Float64("stop_loss", slPrice),
			)

			go s.watchTrade(scalpTrade)
			return
		}
	}
}

// watchTrade мониторит активную сделку до выхода.
func (s *Service) watchTrade(t *ScalpTrade) {
	timeout := time.NewTimer(10 * time.Minute)
	defer timeout.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return

		case <-timeout.C:
			s.log.Warn("scalping: trade timeout, force closing",
				zap.Int64("id", t.ID), zap.String("symbol", t.Symbol))
			s.closeTrade(t, t.PeakPrice, "timeout")
			return

		case price := <-t.PriceCh:
			// Обновляем пиковую цену
			if price > t.PeakPrice {
				t.PeakPrice = price
			}

			// Активируем trailing stop
			if !t.TrailingActive && price >= t.EntryPrice*(1+s.cfg.TrailingActivatePct/100) {
				t.TrailingActive = true
				t.TrailingStop = t.PeakPrice * (1 - s.cfg.TrailingStepPct/100)
				s.log.Info("scalping: trailing stop activated",
					zap.Int64("id", t.ID),
					zap.String("symbol", t.Symbol),
					zap.Float64("price", price),
					zap.Float64("trailing_stop", t.TrailingStop),
				)
			}

			// Обновляем trailing stop при новом пике
			if t.TrailingActive {
				newStop := t.PeakPrice * (1 - s.cfg.TrailingStepPct/100)
				if newStop > t.TrailingStop {
					t.TrailingStop = newStop
				}
			}

			// Hard SL
			if price <= t.StopLoss {
				s.log.Warn("scalping: hard SL hit",
					zap.Int64("id", t.ID),
					zap.String("symbol", t.Symbol),
					zap.Float64("price", price),
					zap.Float64("stop_loss", t.StopLoss),
				)
				s.closeTrade(t, price, "sl")
				return
			}

			// Trailing stop exit
			if t.TrailingActive && price <= t.TrailingStop {
				s.log.Info("scalping: trailing stop hit",
					zap.Int64("id", t.ID),
					zap.String("symbol", t.Symbol),
					zap.Float64("price", price),
					zap.Float64("trailing_stop", t.TrailingStop),
					zap.Float64("peak_price", t.PeakPrice),
				)
				s.closeTrade(t, price, "trailing")
				return
			}

			// Hard TP
			if price >= t.TakeProfit {
				s.log.Info("scalping: hard TP hit",
					zap.Int64("id", t.ID),
					zap.String("symbol", t.Symbol),
					zap.Float64("price", price),
					zap.Float64("take_profit", t.TakeProfit),
				)
				s.closeTrade(t, price, "tp")
				return
			}
		}
	}
}

// closeTrade закрывает сделку через tradeSvc и фиксирует PnL.
func (s *Service) closeTrade(t *ScalpTrade, exitPrice float64, reason string) {
	defer s.tracker.Remove(t.Symbol)

	err := s.tradeSvc.CloseTrade(s.ctx, t.ID, exitPrice, reason)
	if err != nil {
		s.log.Error("scalping: close trade failed",
			zap.Int64("id", t.ID),
			zap.String("symbol", t.Symbol),
			zap.String("reason", reason),
			zap.Error(err),
		)
		return
	}

	pnl := (exitPrice - t.EntryPrice) * t.Qty
	holdSec := int(time.Since(t.OpenedAt).Seconds())

	// Учитываем убытки для дневного лимита
	if pnl < 0 {
		s.mu.Lock()
		s.dailyLossUSDT += math.Abs(pnl)
		s.mu.Unlock()
	}

	s.log.Info("scalping: trade closed",
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

// ─── Helpers ─────────────────────────────────────────────────────────────────

// checkATRFilter проверяет, что текущий ATR выше среднего ATR за окно.
func (s *Service) checkATRFilter(symbol string, snapshot []Candle) bool {
	if len(snapshot) < s.cfg.ATRPeriod+1 {
		return true // недостаточно данных — пропускаем фильтр
	}

	highs := extractHighs(snapshot)
	lows := extractLows(snapshot)
	closes := extractCloses(snapshot)
	currentATR := ATR(highs, lows, closes, s.cfg.ATRPeriod)

	s.mu.Lock()
	history := s.atrHistory[symbol]
	s.mu.Unlock()

	if len(history) < 2 {
		return currentATR > 0 // мало истории — требуем только ненулевой ATR
	}

	// Среднее из накопленных ATR значений
	var sum float64
	for _, v := range history {
		sum += v
	}
	avgATR := sum / float64(len(history))

	return currentATR > avgATR
}

// appendATRHistory добавляет значение ATR в скользящее окно.
func (s *Service) appendATRHistory(symbol string, atr float64) {
	if atr <= 0 {
		return
	}
	maxLen := s.cfg.ATRWindowHours * 60 // предполагаем 1m свечи
	if maxLen <= 0 {
		maxLen = 1440
	}

	s.mu.Lock()
	s.atrHistory[symbol] = append(s.atrHistory[symbol], atr)
	if len(s.atrHistory[symbol]) > maxLen {
		s.atrHistory[symbol] = s.atrHistory[symbol][len(s.atrHistory[symbol])-maxLen:]
	}
	s.mu.Unlock()
}

// checkDailyLoss проверяет, не превышен ли дневной лимит убытков.
func (s *Service) checkDailyLoss() bool {
	today := time.Now().Format("2006-01-02")

	s.mu.Lock()
	defer s.mu.Unlock()

	// Сброс в новый день
	if s.dailyDate != today {
		s.dailyDate = today
		s.dailyLossUSDT = 0
	}

	// Получаем баланс (из кеша)
	balance := s.cachedBalance
	if balance <= 0 {
		return true // не знаем баланс — разрешаем
	}

	capUSDT := balance * s.cfg.DailyLossCapPct / 100
	return s.dailyLossUSDT < capUSDT
}

// getFreeUSDT возвращает баланс USDT, кешированный на 30 секунд.
func (s *Service) getFreeUSDT() (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if time.Since(s.balanceFetchedAt) < 30*time.Second && s.cachedBalance > 0 {
		return s.cachedBalance, nil
	}

	balance, err := s.client.GetFreeUSDT(s.ctx)
	if err != nil {
		return 0, err
	}
	s.cachedBalance = balance
	s.balanceFetchedAt = time.Now()
	return balance, nil
}

// takeProfitPct возвращает нужный % TP в зависимости от символа.
func (s *Service) takeProfitPct(symbol string) float64 {
	base := strings.TrimSuffix(strings.TrimSuffix(symbol, "USDT"), "USDC")
	switch base {
	case "BTC", "BNB":
		return s.cfg.TakeProfitPctBTC
	default:
		return s.cfg.TakeProfitPctAlt
	}
}

// extractCloses возвращает срез цен закрытия из снапшота.
func extractCloses(snapshot []Candle) []float64 {
	out := make([]float64, len(snapshot))
	for i, c := range snapshot {
		out[i] = c.Close
	}
	return out
}

// extractHighs возвращает срез максимальных цен из снапшота.
func extractHighs(snapshot []Candle) []float64 {
	out := make([]float64, len(snapshot))
	for i, c := range snapshot {
		out[i] = c.High
	}
	return out
}

// extractLows возвращает срез минимальных цен из снапшота.
func extractLows(snapshot []Candle) []float64 {
	out := make([]float64, len(snapshot))
	for i, c := range snapshot {
		out[i] = c.Low
	}
	return out
}
