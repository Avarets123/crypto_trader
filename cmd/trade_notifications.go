package main

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/shared/telegram"
	"github.com/osman/bot-traider/internal/trade"
)

// tradeNotifier отправляет Telegram-уведомления при открытии и закрытии сделок.
type tradeNotifier struct {
	ctx      context.Context
	notifier *telegram.Notifier
	log      *zap.Logger
}

func newTradeNotifier(ctx context.Context, notifier *telegram.Notifier, log *zap.Logger) *tradeNotifier {
	return &tradeNotifier{ctx: ctx, notifier: notifier, log: log}
}

// OnTradeOpen — хук для trade.Service.WithOnTradeOpen.
func (n *tradeNotifier) OnTradeOpen(t *trade.Trade) {
	msg := formatTradeOpenMsg(t)
	n.log.Debug("trade notifier: sending open notification", zap.String("symbol", t.Symbol))
	n.notifier.Send(n.ctx, msg)
}

// OnTradeClose — хук для trade.Service.WithOnTradeClose.
func (n *tradeNotifier) OnTradeClose(t *trade.Trade) {
	msg := formatTradeCloseMsg(t)
	n.log.Debug("trade notifier: sending close notification", zap.String("symbol", t.Symbol))
	n.notifier.Send(n.ctx, msg)
}

func formatTradeOpenMsg(t *trade.Trade) string {
	var sb strings.Builder
	sb.WriteString("🟢 <b>Сделка открыта</b>\n")
	fmt.Fprintf(&sb, "Стратегия: %s\n", strategyLabel(t.Strategy))
	fmt.Fprintf(&sb, "Символ:   <b>%s</b>\n", t.Symbol)
	fmt.Fprintf(&sb, "Биржа:    %s [%s]\n", t.TradeExchange, t.Mode)
	fmt.Fprintf(&sb, "Цена входа: <b>%s</b>\n", formatPrice(t.EntryPrice))
	fmt.Fprintf(&sb, "Количество: %s\n", formatQty(t.Qty))
	if t.TargetPrice != nil {
		fmt.Fprintf(&sb, "Цель (TP): %s\n", formatPrice(*t.TargetPrice))
	}
	if t.StopLossPrice != nil {
		fmt.Fprintf(&sb, "Стоп (SL): %s\n", formatPrice(*t.StopLossPrice))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func formatTradeCloseMsg(t *trade.Trade) string {
	emoji, reason := exitLabel(t.ExitReason)
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s <b>Сделка закрыта — %s</b>\n", emoji, reason)
	fmt.Fprintf(&sb, "Стратегия: %s\n", strategyLabel(t.Strategy))
	fmt.Fprintf(&sb, "Символ:   <b>%s</b>\n", t.Symbol)
	fmt.Fprintf(&sb, "Биржа:    %s [%s]\n", t.TradeExchange, t.Mode)
	fmt.Fprintf(&sb, "Вход → Выход: %s → %s\n", formatPrice(t.EntryPrice), exitPrice(t))
	fmt.Fprintf(&sb, "Количество: %s\n", formatQty(t.Qty))
	if t.CommissionUSDT != nil {
		fmt.Fprintf(&sb, "Комиссия: %s USDT\n", formatPrice(*t.CommissionUSDT))
	}
	if t.PnlUSDT != nil {
		fmt.Fprintf(&sb, "PnL: <b>%s USDT</b>\n", formatPnl(*t.PnlUSDT))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func strategyLabel(strategy string) string {
	switch strategy {
	case "arb":
		return "⚡ Lead-Lag Arb"
	case "momentum", "pump":
		return "🚀 Momentum"
	default:
		return strategy
	}
}

func exitLabel(reason string) (emoji, label string) {
	switch reason {
	case "tp":
		return "✅", "TP"
	case "sl":
		return "🛑", "SL"
	case "trailing":
		return "📉", "Trailing Stop"
	case "crash":
		return "💥", "Crash Exit"
	case "timeout":
		return "⏱", "Timeout"
	default:
		return "🔴", strings.ToUpper(reason)
	}
}

func exitPrice(t *trade.Trade) string {
	if t.ExitPrice != nil {
		return formatPrice(*t.ExitPrice)
	}
	return "—"
}

func formatPrice(p float64) string {
	if p >= 1000 {
		return fmt.Sprintf("%.2f", p)
	}
	if p >= 1 {
		return fmt.Sprintf("%.4f", p)
	}
	return fmt.Sprintf("%.8f", p)
}

func formatQty(q float64) string {
	return fmt.Sprintf("%.8g", q)
}

func formatPnl(pnl float64) string {
	if pnl >= 0 {
		return fmt.Sprintf("+%.4f", pnl)
	}
	return fmt.Sprintf("%.4f", pnl)
}
