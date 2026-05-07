package grid

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/shared/exchange"
	"github.com/osman/bot-traider/internal/shared/indicators"
)

// calcATRBounds вычисляет границы grid через ATR(period_h, 1h).
// Возвращает (lower, upper, ok). Если klineProvider недоступен или мало данных — ok=false.
func calcATRBounds(
	ctx context.Context,
	kp exchange.KlineProvider,
	symbol string,
	currentPrice float64,
	periodHours int,
	multiplier float64,
	log *zap.Logger,
) (lower, upper float64, ok bool) {
	if kp == nil || periodHours < 2 || multiplier <= 0 || currentPrice <= 0 {
		return 0, 0, false
	}
	limit := periodHours + 1 // +1 чтобы было на одну больше для расчёта TR
	klines, err := kp.GetKlines(ctx, symbol, "1h", limit)
	if err != nil {
		log.Warn("grid: ATR klines fetch failed",
			zap.String("symbol", symbol),
			zap.Error(err),
		)
		return 0, 0, false
	}
	if len(klines) < periodHours+1 {
		log.Warn("grid: not enough klines for ATR",
			zap.String("symbol", symbol),
			zap.Int("got", len(klines)),
			zap.Int("need", periodHours+1),
		)
		return 0, 0, false
	}
	atr := indicators.ATR(klines, periodHours)
	if atr <= 0 {
		log.Warn("grid: ATR returned 0",
			zap.String("symbol", symbol),
			zap.Int("klines", len(klines)),
		)
		return 0, 0, false
	}
	lower = currentPrice - multiplier*atr
	upper = currentPrice + multiplier*atr
	if lower <= 0 {
		log.Warn("grid: ATR lower bound is non-positive",
			zap.String("symbol", symbol),
			zap.Float64("price", currentPrice),
			zap.Float64("atr", atr),
		)
		return 0, 0, false
	}
	log.Info("grid: ATR-based bounds calculated",
		zap.String("symbol", symbol),
		zap.Float64("price", currentPrice),
		zap.Float64("atr", atr),
		zap.Float64("multiplier", multiplier),
		zap.Float64("lower", lower),
		zap.Float64("upper", upper),
		zap.Float64("range_pct", (upper-lower)/currentPrice*100),
	)
	return lower, upper, true
}

// fetchADX возвращает текущее ADX(period, 1h) для символа.
func fetchADX(
	ctx context.Context,
	kp exchange.KlineProvider,
	symbol string,
	period int,
) (float64, error) {
	if kp == nil {
		return 0, fmt.Errorf("klineProvider is nil")
	}
	if period < 2 {
		period = 14
	}
	// ADX требует минимум 2*period+1 свечей; берём с запасом.
	limit := 3 * period
	klines, err := kp.GetKlines(ctx, symbol, "1h", limit)
	if err != nil {
		return 0, fmt.Errorf("get klines: %w", err)
	}
	if len(klines) < 2*period+1 {
		return 0, fmt.Errorf("not enough klines (got %d, need %d)", len(klines), 2*period+1)
	}
	return indicators.ADX(klines, period), nil
}

// runADXMonitor периодически проверяет ADX по всем символам сетки и переключает
// флаг paused. При pause — отменяет все ордера и не даёт стартовать новой сетке.
// При resume — стартует grid заново от текущей цены.
func (s *Service) runADXMonitor(ctx context.Context) {
	if !s.cfg.UseADXFilter || s.klineProvider == nil {
		return
	}
	interval := time.Duration(s.cfg.ADXCheckIntervalMin) * time.Minute
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	s.log.Info("grid: ADX monitor started",
		zap.Duration("check_interval", interval),
		zap.Float64("pause_threshold", s.cfg.ADXPauseThreshold),
		zap.Float64("resume_threshold", s.cfg.ADXResumeThreshold),
	)

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.evaluateADXForAllSymbols(ctx)
		}
	}
}

func (s *Service) evaluateADXForAllSymbols(ctx context.Context) {
	for _, sym := range s.cfg.Symbols {
		adx, err := fetchADX(ctx, s.klineProvider, sym, s.cfg.ADXPeriod)
		if err != nil {
			s.log.Warn("grid: ADX fetch failed",
				zap.String("symbol", sym),
				zap.Error(err),
			)
			continue
		}

		state, ok := s.tracker.Get(sym)
		if !ok {
			continue
		}

		s.log.Info("grid: ADX measured",
			zap.String("symbol", sym),
			zap.Float64("adx", adx),
			zap.Bool("paused", state.Paused),
			zap.Bool("active", state.Active),
		)

		// Pause: ADX превышает порог, сетка активна → отменяем ордера, ставим paused.
		if adx >= s.cfg.ADXPauseThreshold && state.Active && !state.Paused {
			s.log.Warn("grid: ADX above pause threshold, cancelling orders",
				zap.String("symbol", sym),
				zap.Float64("adx", adx),
				zap.Float64("threshold", s.cfg.ADXPauseThreshold),
			)
			s.pauseSymbol(ctx, state, sym, adx)
			continue
		}

		// Resume: ADX ниже порога, сетка на паузе → перезапускаем.
		if adx <= s.cfg.ADXResumeThreshold && state.Paused {
			s.log.Info("grid: ADX below resume threshold, restarting grid",
				zap.String("symbol", sym),
				zap.Float64("adx", adx),
				zap.Float64("threshold", s.cfg.ADXResumeThreshold),
			)
			s.resumeSymbol(ctx, state, sym)
		}
	}
}

func (s *Service) pauseSymbol(ctx context.Context, state *GridState, symbol string, adx float64) {
	s.mu.Lock()
	state.Active = false
	state.Paused = true
	s.mu.Unlock()

	CancelAllOrders(ctx, state, s.client, s.log)

	if s.notifier != nil {
		msg := fmt.Sprintf(
			"⏸ <b>Grid PAUSED (ADX trend filter)</b>\n"+
				"Символ: <b>%s</b>\n"+
				"ADX: %.1f (threshold pause: %.1f)\n"+
				"Все ордера отменены. Resume при ADX < %.1f.",
			symbol, adx, s.cfg.ADXPauseThreshold, s.cfg.ADXResumeThreshold,
		)
		go s.notifier.SendToThread(ctx, msg, s.tradesThreadID)
	}
}

func (s *Service) resumeSymbol(ctx context.Context, state *GridState, symbol string) {
	s.mu.Lock()
	state.Paused = false
	currentPrice := state.CurrentPrice
	s.mu.Unlock()

	if currentPrice <= 0 {
		s.log.Warn("grid: cannot resume — currentPrice is 0", zap.String("symbol", symbol))
		return
	}

	if s.notifier != nil {
		msg := fmt.Sprintf(
			"▶️ <b>Grid RESUMED</b>\n"+
				"Символ: <b>%s</b>\n"+
				"Перезапуск от цены %s",
			symbol, formatGridPrice(currentPrice),
		)
		go s.notifier.SendToThread(ctx, msg, s.tradesThreadID)
	}

	go s.startGrid(ctx, symbol, currentPrice)
}
