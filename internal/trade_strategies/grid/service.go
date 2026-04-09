package grid

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/shared/exchange"
	"github.com/osman/bot-traider/internal/shared/telegram"
	"github.com/osman/bot-traider/internal/ticker"
)

// Service реализует Grid торговую стратегию:
// геометрическая сетка лимитных buy/sell ордеров, стоп-лосс, trailing up.
type Service struct {
	mu       sync.Mutex
	cfg      Config
	ctx      context.Context
	client   exchange.RestClient
	tracker  *GridTracker
	notifier *telegram.Notifier
	log      *zap.Logger
	repo     *GridRepository // nil если persistence не настроена
}

// NewService создаёт Service.
func NewService(cfg Config, client exchange.RestClient, log *zap.Logger, notifier *telegram.Notifier) *Service {
	if cfg.Grids < 5 || cfg.Grids > 50 {
		log.Warn("grid: invalid GRID_GRIDS value, using default 20",
			zap.Int("grids", cfg.Grids),
		)
		cfg.Grids = 20
	}

	log.Info("grid strategy initialized",
		zap.String("exchange", cfg.Exchange),
		zap.Strings("symbols", cfg.Symbols),
		zap.Int("grids", cfg.Grids),
		zap.Float64("total_usdt", cfg.TotalUSDT),
		zap.Float64("lower_bound_pct", cfg.LowerBoundPct),
		zap.Float64("upper_bound_pct", cfg.UpperBoundPct),
		zap.Float64("stop_loss_pct", cfg.StopLossPct),
		zap.Bool("trailing_up", cfg.TrailingUp),
		zap.Int("cooldown_sec", cfg.CooldownSec),
	)

	return &Service{
		cfg:      cfg,
		client:   client,
		tracker:  NewGridTracker(),
		notifier: notifier,
		log:      log,
	}
}

// WithRepository подключает GridRepository для сохранения исполненных ордеров.
func (s *Service) WithRepository(repo *GridRepository) {
	s.repo = repo
}

// Start проверяет баланс и инициализирует placeholders для символов.
// Вызывается в горутине из main.go.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()

	// Проверяем баланс перед стартом
	balance, err := s.client.GetFreeUSDT(ctx)
	if err != nil {
		s.log.Error("grid: failed to get USDT balance, grid will not start", zap.Error(err))
		return
	}
	if balance < s.cfg.TotalUSDT {
		s.log.Error("grid: insufficient USDT balance",
			zap.Float64("balance", balance),
			zap.Float64("required", s.cfg.TotalUSDT),
		)
		return
	}

	s.log.Info("grid: balance check passed",
		zap.Float64("balance_usdt", balance),
		zap.Float64("grid_total_usdt", s.cfg.TotalUSDT),
	)

	// Инициализируем placeholders для каждого символа
	for _, sym := range s.cfg.Symbols {
		state := &GridState{
			Symbol:  sym,
			Active:  false,
			PriceCh: make(chan float64, 128),
		}
		s.tracker.Set(state)
	}
}

// OnTicker получает тикеры и обновляет состояния сеток.
func (s *Service) OnTicker(t ticker.Ticker) {
	if !strings.EqualFold(t.Exchange, s.cfg.Exchange) {
		return
	}

	state, ok := s.tracker.Get(t.Symbol)
	if !ok {
		return
	}

	price, err := strconv.ParseFloat(t.Price, 64)
	if err != nil || price <= 0 {
		return
	}

	s.tracker.UpdatePrice(t.Symbol, price)

	if !state.Active {
		// Запускаем сетку при первом тике (один раз)
		s.mu.Lock()
		shouldStart := !state.Active
		if shouldStart {
			state.Active = true // ставим флаг сразу чтобы не запустить дважды
		}
		s.mu.Unlock()

		if shouldStart {
			go s.startGrid(s.ctx, t.Symbol, price)
		}
		return
	}

	// Проверяем стоп-лосс
	if state.StopLoss > 0 && price <= state.StopLoss {
		s.log.Error("grid: stop-loss triggered",
			zap.String("symbol", t.Symbol),
			zap.Float64("price", price),
			zap.Float64("stop_loss", state.StopLoss),
			zap.Float64("lower_bound", state.LowerBound),
		)
		go s.emergencyClose(s.ctx, t.Symbol, price)
		return
	}

	// Проверяем trailing up
	if s.cfg.TrailingUp && state.UpperBound > 0 && price > state.UpperBound*1.005 {
		s.log.Info("grid: trailing up triggered",
			zap.String("symbol", t.Symbol),
			zap.Float64("price", price),
			zap.Float64("upper_bound", state.UpperBound),
			zap.Float64("overshoot_pct", (price/state.UpperBound-1)*100),
		)
		go s.shiftGridUp(s.ctx, t.Symbol, price)
		return
	}



	// Отправляем цену в watchGrid
	select {
	case state.PriceCh <- price:
	default:
	}
}

// startGrid инициализирует уровни и размещает ордера.
// Вызывается в горутине; флаг Active уже выставлен в OnTicker.
func (s *Service) startGrid(ctx context.Context, symbol string, currentPrice float64) {
	lower := currentPrice * (1 - s.cfg.LowerBoundPct/100)
	upper := currentPrice * (1 + s.cfg.UpperBoundPct/100)

	prices := CalcLevels(lower, upper, s.cfg.Grids)

	// Проверяем минимальный notional (цена × qty должна быть ≥ GRID_MIN_NOTIONAL_USDT)
	qty := CalcQtyPerLevel(s.cfg.TotalUSDT, prices)
	notional := qty * currentPrice
	if notional < s.cfg.MinNotionalUSDT {
		s.log.Error("grid: order notional too small, grid will not start — increase GRID_TOTAL_USDT or decrease GRID_GRIDS",
			zap.String("symbol", symbol),
			zap.Float64("notional_usdt", notional),
			zap.Float64("min_notional_usdt", s.cfg.MinNotionalUSDT),
			zap.Float64("qty_per_level", qty),
			zap.Float64("price", currentPrice),
			zap.Float64("total_usdt", s.cfg.TotalUSDT),
			zap.Int("grids", s.cfg.Grids),
			zap.Float64("recommended_total_usdt", s.cfg.MinNotionalUSDT*float64(s.cfg.Grids)*2),
		)
		// Сбрасываем флаг Active чтобы не блокировать повторный старт
		if state, ok := s.tracker.Get(symbol); ok {
			s.mu.Lock()
			state.Active = false
			s.mu.Unlock()
		}
		return
	}

	gridLevels := make([]*GridLevel, len(prices))
	for i, p := range prices {
		gridLevels[i] = &GridLevel{
			Index: i,
			Price: p,
		}
	}

	sl := CalcStopLoss(lower, s.cfg.StopLossPct)
	ratio := CalcRatio(lower, upper, s.cfg.Grids)

	state, ok := s.tracker.Get(symbol)
	if !ok {
		return
	}

	s.mu.Lock()
	state.LowerBound = lower
	state.UpperBound = upper
	state.StopLoss = sl
	state.Ratio = ratio
	state.Levels = gridLevels
	state.QtyPerLevel = qty
	state.StartedAt = time.Now()
	state.CurrentPrice = currentPrice
	state.SessionID = uuid.New()
	s.mu.Unlock()

	// Считаем сколько уровней buy будет размещено
	buyLevels := 0
	for _, p := range prices {
		if p < currentPrice {
			buyLevels++
		}
	}

	// Размещаем buy-ордера на всех уровнях ниже текущей цены
	PlaceAllBuyOrders(ctx, state, s.client, s.log)

	// Логируем итог размещения
	placed := 0
	for _, l := range state.Levels {
		if l.OrderID != "" {
			placed++
		}
	}
	s.log.Info("grid: initial orders placed",
		zap.String("symbol", symbol),
		zap.Int("placed", placed),
		zap.Int("expected", buyLevels),
	)

	msg := fmt.Sprintf(
		"🟩 <b>Grid запущена</b>\n"+
			"Биржа: %s | Символ: <b>%s</b>\n"+
			"Цена входа: %s\n"+
			"Диапазон: %s — %s\n"+
			"Стоп-лосс: %s (-%s%%)\n"+
			"Уровней: %d | Шаг: %.3f%%\n"+
			"Объём/уровень: %.8g (≈%s USDT)\n"+
			"Buy-ордеров размещено: %d / %d",
		s.cfg.Exchange, symbol,
		formatGridPrice(currentPrice),
		formatGridPrice(lower), formatGridPrice(upper),
		formatGridPrice(sl), fmt.Sprintf("%.1f", s.cfg.StopLossPct),
		s.cfg.Grids, (ratio-1)*100,
		qty, formatGridPrice(notional),
		placed, buyLevels,
	)
	go s.notifier.Send(ctx, msg)

	// Запускаем мониторинг сетки
	go s.watchGrid(ctx, symbol)
}

// watchGrid периодически синхронизирует ордера и обрабатывает исполнения.
func (s *Service) watchGrid(ctx context.Context, symbol string) {
	syncTicker := time.NewTicker(3 * time.Second)
	defer syncTicker.Stop()


	for {
		select {
		case <-ctx.Done():
			s.log.Error("grid: watchGrid stopped (context cancelled)", zap.String("symbol", symbol))
			return

		case <-syncTicker.C:
			state, ok := s.tracker.Get(symbol)
			if !ok || !state.Active {
				s.log.Info("grid: watchGrid stopped (grid inactive)", zap.String("symbol", symbol))
				return
			}

			filled := SyncOrders(ctx, state, s.client, s.log)
			if len(filled) > 0 {
				s.log.Info("grid: sync found filled orders",
					zap.String("symbol", symbol),
					zap.Int("count", len(filled)),
				)
			}
			for _, level := range filled {
				s.handleFilledOrder(ctx, state, level)
			}

			// Периодический статус активных ордеров
			active := 0
			for _, l := range state.Levels {
				if l.OrderID != "" {
					active++
				}
			}
			s.log.Debug("grid: status",
				zap.String("symbol", symbol),
				zap.Float64("price", state.CurrentPrice),
				zap.Int("active_orders", active),
				zap.Int("total_levels", len(state.Levels)),
				zap.Float64("lower", state.LowerBound),
				zap.Float64("upper", state.UpperBound),
				zap.Time("started_at", state.StartedAt),
			)
		}
	}
}

// handleFilledOrder обрабатывает исполненный ордер — ставит встречный.
func (s *Service) handleFilledOrder(ctx context.Context, state *GridState, level *GridLevel) {
	side := level.Side
	level.Filled = true
	level.FilledAt = time.Now()
	level.FilledOrderID = level.OrderID
	level.OrderID = ""

	pnl := 0.0
	if side == "sell" {
		// оценочный PnL одного цикла buy→sell
		buyPrice := level.Price / state.Ratio
		pnl = (level.Price - buyPrice) * state.QtyPerLevel
		state.TotalPnL += pnl
		state.FilledCycles++
	}

	s.log.Info("grid: order filled",
		zap.String("symbol", state.Symbol),
		zap.String("side", strings.ToUpper(side)),
		zap.Int("level", level.Index),
		zap.Float64("price", level.Price),
		zap.Float64("qty", state.QtyPerLevel),
		zap.Float64("cycle_pnl_usdt", pnl),
		zap.Float64("total_pnl_usdt", state.TotalPnL),
		zap.Int("filled_cycles", state.FilledCycles),
	)

	// Считаем активные ордера после исполнения
	activeOrders := 0
	for _, l := range state.Levels {
		if l.OrderID != "" {
			activeOrders++
		}
	}

	nextAction := ""
	switch side {
	case "buy":
		if level.Index+1 < len(state.Levels) {
			nextAction = fmt.Sprintf("→ sell на %s", formatGridPrice(state.Levels[level.Index+1].Price))
		}
	case "sell":
		if level.Index-1 >= 0 {
			nextAction = fmt.Sprintf("→ buy на %s", formatGridPrice(state.Levels[level.Index-1].Price))
		}
	}

	msg := fmt.Sprintf(
		"✅ <b>Grid ордер исполнен</b>\n"+
			"Символ: <b>%s</b>\n"+
			"Сторона: %s | Уровень: %d / %d\n"+
			"Цена: %s | Объём: %.8g\n"+
			"Активных ордеров: %d\n"+
			"%s",
		state.Symbol,
		strings.ToUpper(side), level.Index+1, len(state.Levels),
		formatGridPrice(level.Price), state.QtyPerLevel,
		activeOrders,
		nextAction,
	)
	if side == "sell" {
		pnlSign := "+"
		if pnl < 0 {
			pnlSign = ""
		}
		msg += fmt.Sprintf("\nЦикл PnL: %s%.4f USDT", pnlSign, pnl)
		msg += fmt.Sprintf("\nИтого за сессию: %s%.4f USDT (%d циклов)",
			func() string {
				if state.TotalPnL >= 0 {
					return "+"
				}
				return ""
			}(),
			state.TotalPnL, state.FilledCycles,
		)
	}
	go s.notifier.Send(ctx, msg)

	switch side {
	case "buy":
		PlaceSellOrder(ctx, state, level, s.client, s.log)
	case "sell":
		PlaceBuyOrder(ctx, state, level, s.client, s.log)
	}

	// Сохраняем исполненный ордер в БД асинхронно (ошибка не блокирует торговлю)
		rec := GridOrderRecord{
			SessionID:  state.SessionID,
			Symbol:     state.Symbol,
			Exchange:   s.cfg.Exchange,
			Side:       side,
			LevelIndex: level.Index,
			Price:      level.Price,
			Qty:        state.QtyPerLevel,
			OrderID:    level.FilledOrderID,
		}
		if side == "sell" {
			rec.CyclePnlUSDT = &pnl
		}
		go func(r GridOrderRecord) {
			if err := s.repo.SaveFilledOrder(context.Background(), r); err != nil {
				s.log.Warn("grid: failed to save order",
					zap.Error(err),
					zap.String("symbol", r.Symbol),
					zap.String("order_id", r.OrderID),
				)
			}
		}(rec)
}

// emergencyClose аварийно закрывает все позиции по стоп-лоссу.
func (s *Service) emergencyClose(ctx context.Context, symbol string, triggerPrice float64) {
	state, ok := s.tracker.Get(symbol)
	if !ok {
		return
	}

	s.mu.Lock()
	if !state.Active {
		s.mu.Unlock()
		return
	}
	state.Active = false
	s.mu.Unlock()

	s.log.Warn("grid: cancelling all orders before emergency close", zap.String("symbol", symbol))
	CancelAllOrders(ctx, state, s.client, s.log)

	// Продаём суммарный объём по рынку
	totalQty := state.QtyPerLevel * float64(s.cfg.Grids)
	if totalQty > 0 {
		s.log.Warn("grid: placing emergency market sell",
			zap.String("symbol", symbol),
			zap.Float64("qty", totalQty),
		)
		if _, err := s.client.PlaceMarketOrder(ctx, symbol, "Sell", totalQty); err != nil {
			s.log.Error("grid: emergency sell failed",
				zap.String("symbol", symbol),
				zap.Float64("qty", totalQty),
				zap.Error(err),
			)
		}
	}

	holdMin := time.Since(state.StartedAt).Minutes()
	s.log.Error("grid: emergency close executed",
		zap.String("symbol", symbol),
		zap.Float64("trigger_price", triggerPrice),
		zap.Float64("stop_loss", state.StopLoss),
		zap.Float64("lower_bound", state.LowerBound),
		zap.Float64("hold_minutes", holdMin),
		zap.Int("cooldown_sec", s.cfg.CooldownSec),
	)

	pnlSign := "+"
	if state.TotalPnL < 0 {
		pnlSign = ""
	}
	msg := fmt.Sprintf(
		"🛑 <b>Grid СТОП-ЛОСС</b>\n"+
			"Символ: <b>%s</b>\n"+
			"Цена: %s | СЛ: %s\n"+
			"Диапазон был: %s — %s\n"+
			"Продано: %.8g (рыночный ордер)\n"+
			"Работа: %.1f мин | Циклов: %d\n"+
			"PnL сессии: %s%.4f USDT\n"+
			"Перезапуск через %d сек",
		symbol,
		formatGridPrice(triggerPrice), formatGridPrice(state.StopLoss),
		formatGridPrice(state.LowerBound), formatGridPrice(state.UpperBound),
		totalQty,
		holdMin, state.FilledCycles,
		pnlSign, state.TotalPnL,
		s.cfg.CooldownSec,
	)
	go s.notifier.Send(ctx, msg)

	// Перезапускаем сетку через cooldown
	s.log.Info("grid: will restart after cooldown",
		zap.String("symbol", symbol),
		zap.Int("cooldown_sec", s.cfg.CooldownSec),
	)
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(s.cfg.CooldownSec) * time.Second):
		}
		currentPrice := state.CurrentPrice
		if currentPrice > 0 {
			s.log.Info("grid: restarting grid after cooldown",
				zap.String("symbol", symbol),
				zap.Float64("price", currentPrice),
			)
			go s.startGrid(ctx, symbol, currentPrice)
		}
	}()
}

// shiftGridUp сдвигает сетку вверх при пробое верхней границы.
func (s *Service) shiftGridUp(ctx context.Context, symbol string, currentPrice float64) {
	state, ok := s.tracker.Get(symbol)
	if !ok {
		return
	}

	s.mu.Lock()
	if !state.Active {
		s.mu.Unlock()
		return
	}
	state.Active = false
	oldLower := state.LowerBound
	oldUpper := state.UpperBound
	holdMin := time.Since(state.StartedAt).Minutes()
	s.mu.Unlock()

	s.log.Info("grid: cancelling all orders before shift", zap.String("symbol", symbol))
	CancelAllOrders(ctx, state, s.client, s.log)

	s.log.Info("grid: grid shifted up",
		zap.String("symbol", symbol),
		zap.Float64("old_lower", oldLower),
		zap.Float64("old_upper", oldUpper),
		zap.Float64("new_price", currentPrice),
		zap.Float64("price_gain_pct", (currentPrice/oldLower-1)*100),
		zap.Float64("hold_minutes", holdMin),
	)

	newLower := currentPrice * (1 - s.cfg.LowerBoundPct/100)
	newUpper := currentPrice * (1 + s.cfg.UpperBoundPct/100)

	pnlSign := "+"
	if state.TotalPnL < 0 {
		pnlSign = ""
	}
	msg := fmt.Sprintf(
		"📈 <b>Grid сдвиг вверх</b>\n"+
			"Символ: <b>%s</b>\n"+
			"Рост: %s → %s (+%.2f%%)\n"+
			"Старый диапазон: %s — %s\n"+
			"Новый диапазон: %s — %s\n"+
			"Работа: %.1f мин | Циклов: %d\n"+
			"PnL сессии: %s%.4f USDT",
		symbol,
		formatGridPrice(oldLower), formatGridPrice(currentPrice),
		(currentPrice/oldLower-1)*100,
		formatGridPrice(oldLower), formatGridPrice(oldUpper),
		formatGridPrice(newLower), formatGridPrice(newUpper),
		holdMin, state.FilledCycles,
		pnlSign, state.TotalPnL,
	)
	go s.notifier.Send(ctx, msg)

	go s.startGrid(ctx, symbol, currentPrice)
}

// formatGridPrice форматирует цену для уведомлений.
func formatGridPrice(p float64) string {
	if p >= 1000 {
		return fmt.Sprintf("%.2f", p)
	}
	if p >= 1 {
		return fmt.Sprintf("%.4f", p)
	}
	return fmt.Sprintf("%.8f", p)
}
