// Package risk реализует общий risk-менеджер для всех торговых стратегий.
//
// Manager отслеживает:
//   - дневной PnL (сбрасывается в начале UTC-суток);
//   - общий PnL с момента запуска (используется как drawdown);
//   - exposure по каждой стратегии (сколько USDT занято открытыми позициями).
//
// При превышении дневного лимита убытков или общего drawdown — бот блокируется,
// CanOpen начинает возвращать false для всех стратегий до ручного Reset().
package risk

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/shared/telegram"
	"github.com/osman/bot-traider/internal/trade"
)

// Manager — централизованный учёт лимитов и PnL.
type Manager struct {
	mu  sync.Mutex
	cfg Config
	log *zap.Logger

	notifier       *telegram.Notifier
	notifyThreadID int

	dailyLossLimitUSDT float64
	maxDrawdownUSDT    float64

	dailyPnL     float64
	dailyResetAt time.Time
	totalPnL     float64

	used map[string]float64

	blocked     bool
	blockReason string
}

// New создаёт Manager и пересчитывает абсолютные лимиты из процентов.
func New(cfg Config, log *zap.Logger) *Manager {
	m := &Manager{
		cfg:                cfg,
		log:                log,
		dailyLossLimitUSDT: cfg.InitialCapital * cfg.DailyLossLimitPct / 100,
		maxDrawdownUSDT:    cfg.InitialCapital * cfg.MaxDrawdownPct / 100,
		dailyResetAt:       todayStartUTC(),
		used:               make(map[string]float64),
	}
	log.Info("risk: manager created",
		zap.Float64("initial_capital_usdt", cfg.InitialCapital),
		zap.Float64("daily_loss_limit_usdt", m.dailyLossLimitUSDT),
		zap.Float64("max_drawdown_usdt", m.maxDrawdownUSDT),
		zap.Any("per_strategy_cap", cfg.PerStrategyCap),
	)
	return m
}

// WithTelegram подключает Telegram-уведомления о срабатывании лимитов.
func (m *Manager) WithTelegram(n *telegram.Notifier, threadID int) {
	m.notifier = n
	m.notifyThreadID = threadID
}

// CanOpen проверяет, разрешено ли стратегии открыть позицию заданного размера в USDT.
// Возвращает (true, "") если можно, иначе (false, reason).
func (m *Manager) CanOpen(strategy string, amountUSDT float64) (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.checkDailyResetLocked()

	if m.blocked {
		return false, "risk blocked: " + m.blockReason
	}

	cap, ok := m.cfg.PerStrategyCap[strategy]
	if ok && cap > 0 {
		if m.used[strategy]+amountUSDT > cap {
			return false, fmt.Sprintf("strategy %q cap %.2f exceeded (used=%.2f, requested=%.2f)",
				strategy, cap, m.used[strategy], amountUSDT)
		}
	}

	return true, ""
}

// RecordOpen регистрирует открытие позиции — увеличивает used capital по стратегии.
func (m *Manager) RecordOpen(strategy string, amountUSDT float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.used[strategy] += amountUSDT
	m.log.Debug("risk: position opened",
		zap.String("strategy", strategy),
		zap.Float64("amount_usdt", amountUSDT),
		zap.Float64("used", m.used[strategy]),
	)
}

// OnTradeClose — хук для tradeSvc.WithOnTradeClose.
// Учитывает PnL в дневной/общий счётчик и освобождает exposure.
// При превышении дневного лимита убытков или max drawdown — блокирует бота.
func (m *Manager) OnTradeClose(t *trade.Trade) {
	if t == nil || t.PnlUSDT == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.checkDailyResetLocked()

	pnl := *t.PnlUSDT
	m.dailyPnL += pnl
	m.totalPnL += pnl

	amount := t.EntryPrice * t.Qty
	m.used[t.Strategy] -= amount
	if m.used[t.Strategy] < 0 {
		m.used[t.Strategy] = 0
	}

	m.log.Info("risk: trade closed, PnL updated",
		zap.String("strategy", t.Strategy),
		zap.String("symbol", t.Symbol),
		zap.Float64("trade_pnl", pnl),
		zap.Float64("daily_pnl", m.dailyPnL),
		zap.Float64("total_pnl", m.totalPnL),
		zap.Float64("daily_loss_limit", m.dailyLossLimitUSDT),
		zap.Float64("max_drawdown_limit", m.maxDrawdownUSDT),
		zap.Float64("strategy_used", m.used[t.Strategy]),
	)

	if !m.blocked && m.dailyPnL <= -m.dailyLossLimitUSDT {
		m.blockLocked(fmt.Sprintf("daily loss -%.2f USDT reached (current=%.2f)",
			m.dailyLossLimitUSDT, m.dailyPnL))
	}
	if !m.blocked && m.totalPnL <= -m.maxDrawdownUSDT {
		m.blockLocked(fmt.Sprintf("max drawdown -%.2f USDT reached (current=%.2f)",
			m.maxDrawdownUSDT, m.totalPnL))
	}
}

// RecordPnL — прямое внесение PnL по стратегии без привязки к trade.Trade.
// Используется стратегиями вроде Grid, которые не открывают позиции через trade.Service
// на каждый цикл (PnL фиксируется при каждом завершённом buy→sell цикле).
// При превышении дневного лимита или max drawdown — блокирует бота.
func (m *Manager) RecordPnL(strategy string, pnl float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.checkDailyResetLocked()

	m.dailyPnL += pnl
	m.totalPnL += pnl

	m.log.Info("risk: PnL recorded",
		zap.String("strategy", strategy),
		zap.Float64("pnl", pnl),
		zap.Float64("daily_pnl", m.dailyPnL),
		zap.Float64("total_pnl", m.totalPnL),
	)

	if !m.blocked && m.dailyPnL <= -m.dailyLossLimitUSDT {
		m.blockLocked(fmt.Sprintf("daily loss -%.2f USDT reached (current=%.2f)",
			m.dailyLossLimitUSDT, m.dailyPnL))
	}
	if !m.blocked && m.totalPnL <= -m.maxDrawdownUSDT {
		m.blockLocked(fmt.Sprintf("max drawdown -%.2f USDT reached (current=%.2f)",
			m.maxDrawdownUSDT, m.totalPnL))
	}
}

// IsBlocked возвращает true если бот заблокирован (для проверок снаружи).
func (m *Manager) IsBlocked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.blocked
}

// Reset снимает блокировку (вручную, например после анализа причины).
func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blocked = false
	m.blockReason = ""
	m.log.Warn("risk: manual reset, blocking lifted")
}

// Stats возвращает текущие показатели.
func (m *Manager) Stats() (dailyPnL, totalPnL float64, blocked bool, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dailyPnL, m.totalPnL, m.blocked, m.blockReason
}

// blockLocked — внутренний метод, должен вызываться под mu.
func (m *Manager) blockLocked(reason string) {
	m.blocked = true
	m.blockReason = reason
	m.log.Error("risk: BOT BLOCKED", zap.String("reason", reason))
	if m.notifier != nil {
		msg := fmt.Sprintf("🛑 <b>RISK MANAGER: BOT BLOCKED</b>\n%s\n\nDaily PnL: %.2f USDT\nTotal PnL: %.2f USDT",
			reason, m.dailyPnL, m.totalPnL)
		go m.notifier.SendToThread(context.Background(), msg, m.notifyThreadID)
	}
}

// checkDailyResetLocked сбрасывает дневной счётчик при пересечении UTC-полночи.
func (m *Manager) checkDailyResetLocked() {
	today := todayStartUTC()
	if today.After(m.dailyResetAt) {
		m.log.Info("risk: daily reset",
			zap.Float64("yesterday_pnl", m.dailyPnL),
			zap.Time("new_period_start", today),
		)
		m.dailyPnL = 0
		m.dailyResetAt = today
	}
}

func todayStartUTC() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}
