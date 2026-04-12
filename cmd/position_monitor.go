package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
	"github.com/osman/bot-traider/internal/shared/telegram"
	"github.com/osman/bot-traider/internal/ticker"
	"github.com/osman/bot-traider/internal/trade"
)

// positionMonitor периодически отправляет сводку открытых позиций в Telegram.
type positionMonitor struct {
	tradeSvc       *trade.Service
	notifier       *telegram.Notifier
	tradesThreadID int
	intervalSec    int
	log            *zap.Logger

	mu            sync.RWMutex
	currentPrices map[string]float64 // symbol -> последняя цена
}

func NewPositionMonitor(ctx context.Context, tradeSvc *trade.Service, tickerService *ticker.TickerService, notifier *telegram.Notifier, tradesThreadID int, log *zap.Logger) {
	posMonitorInterval := sharedconfig.GetEnvInt("POSITION_MONITOR_INTERVAL_SEC", 60)
	posMonitor := newPositionMonitor(tradeSvc, notifier, tradesThreadID, posMonitorInterval, log.With(zap.String("component", "position-monitor")))
	tickerService.WithOnSend(posMonitor.OnTicker)
	go posMonitor.Start(ctx)
	log.Info("position monitor started", zap.Int("interval_sec", posMonitorInterval))
}

func newPositionMonitor(
	tradeSvc *trade.Service,
	notifier *telegram.Notifier,
	tradesThreadID int,
	intervalSec int,
	log *zap.Logger,
) *positionMonitor {
	return &positionMonitor{
		tradeSvc:       tradeSvc,
		notifier:       notifier,
		tradesThreadID: tradesThreadID,
		intervalSec:    intervalSec,
		log:            log,
		currentPrices:  make(map[string]float64),
	}
}

// OnTicker обновляет кеш текущих цен. Подключается через tickerService.WithOnSend.
func (m *positionMonitor) OnTicker(t ticker.Ticker) {
	price, err := strconv.ParseFloat(t.Price, 64)
	if err != nil || price <= 0 {
		return
	}
	m.mu.Lock()
	m.currentPrices[t.Symbol] = price
	m.mu.Unlock()
}

// Start запускает цикл отправки сводки с интервалом intervalSec.
func (m *positionMonitor) Start(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(m.intervalSec) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.sendReport(ctx)
		}
	}
}

func (m *positionMonitor) sendReport(ctx context.Context) {
	trades := m.tradeSvc.GetOpenTrades()
	if len(trades) == 0 {
		return
	}

	m.mu.RLock()
	prices := make(map[string]float64, len(m.currentPrices))
	for k, v := range m.currentPrices {
		prices[k] = v
	}
	m.mu.RUnlock()

	var sb strings.Builder
	fmt.Fprintf(&sb, "📊 <b>Открытые позиции (%d)</b>\n", len(trades))

	for _, t := range trades {
		elapsed := time.Since(t.OpenedAt)
		minutes := int(elapsed.Minutes())

		currentPrice, hasCurrent := prices[t.Symbol]

		sb.WriteString("\n")
		fmt.Fprintf(&sb, "%s <b>%s</b> [%s] | %s\n",
			strategyEmoji(t.Strategy), t.Symbol, strategyLabel(t.Strategy), t.TradeExchange)
		fmt.Fprintf(&sb, "  Вход: <code>%s</code>", formatPrice(t.EntryPrice))

		if hasCurrent {
			changePct := (currentPrice - t.EntryPrice) / t.EntryPrice * 100
			unrealizedPnl := (currentPrice - t.EntryPrice) * t.Qty
			fmt.Fprintf(&sb, " → Текущая: <code>%s</code>\n", formatPrice(currentPrice))
			fmt.Fprintf(&sb, "  Изменение: <b>%s</b> | PnL: <b>%s USDT</b>\n",
				formatChangePct(changePct), formatPnl(unrealizedPnl))
		} else {
			sb.WriteString("\n")
		}

		if t.TargetPrice != nil {
			fmt.Fprintf(&sb, "  TP: %s", formatPrice(*t.TargetPrice))
			if t.StopLossPrice != nil {
				fmt.Fprintf(&sb, " | SL: %s", formatPrice(*t.StopLossPrice))
			}
			sb.WriteString("\n")
		} else if t.StopLossPrice != nil {
			fmt.Fprintf(&sb, "  SL: %s\n", formatPrice(*t.StopLossPrice))
		}

		fmt.Fprintf(&sb, "  ⏱ %d мин назад\n", minutes)
	}

	msg := strings.TrimRight(sb.String(), "\n")
	m.log.Debug("position monitor: sending report", zap.Int("positions", len(trades)))
	m.notifier.SendToThread(ctx, msg, m.tradesThreadID)
}

func strategyEmoji(strategy string) string {
	switch strategy {
	case "arb":
		return "⚡"
	case "momentum", "pump":
		return "🚀"
	case "volatile":
		return "🌊"
	default:
		return "🔵"
	}
}

func formatChangePct(pct float64) string {
	if pct >= 0 {
		return fmt.Sprintf("+%.2f%%", pct)
	}
	return fmt.Sprintf("%.2f%%", pct)
}
