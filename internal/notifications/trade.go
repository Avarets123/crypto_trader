package notifications

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/shared/telegram"
	"github.com/osman/bot-traider/internal/trade"
)

// TradeNotifier отправляет Telegram-уведомления при открытии и закрытии сделок.
type TradeNotifier struct {
	ctx            context.Context
	notifier       *telegram.Notifier
	log            *zap.Logger
	tradesThreadID int
}

func NewTradeNotifier(ctx context.Context, notifier *telegram.Notifier, log *zap.Logger, tradesThreadID int) *TradeNotifier {
	return &TradeNotifier{ctx: ctx, notifier: notifier, log: log, tradesThreadID: tradesThreadID}
}

// OnTradeOpen — хук для trade.Service.WithOnTradeOpen.
func (n *TradeNotifier) OnTradeOpen(t *trade.Trade) {
	msg := formatTradeOpenMsg(t)
	n.log.Debug("trade notifier: sending open notification", zap.String("symbol", t.Symbol))
	n.notifier.SendToThread(n.ctx, msg, n.tradesThreadID)
}

// OnTradeClose — хук для trade.Service.WithOnTradeClose.
func (n *TradeNotifier) OnTradeClose(t *trade.Trade) {
	msg := formatTradeCloseMsg(t)
	n.log.Debug("trade notifier: sending close notification", zap.String("symbol", t.Symbol))
	n.notifier.SendToThread(n.ctx, msg, n.tradesThreadID)
}

// OnTradeCloseError — хук для trade.Service.WithOnTradeCloseError.
func (n *TradeNotifier) OnTradeCloseError(t *trade.Trade, err error) {
	msg := formatTradeCloseErrorMsg(t, err)
	n.log.Debug("trade notifier: sending close error notification", zap.String("symbol", t.Symbol), zap.Error(err))
	n.notifier.SendToThread(n.ctx, msg, n.tradesThreadID)
}

func formatTradeOpenMsg(t *trade.Trade) string {
	cur := CurrencyLabel(t.TradeExchange)
	var sb strings.Builder
	sb.WriteString("🟢 <b>Сделка открыта</b>\n")
	fmt.Fprintf(&sb, "Стратегия: %s\n", strategyLabel(t.Strategy))
	fmt.Fprintf(&sb, "Символ:   <b>%s</b>\n", t.Symbol)
	fmt.Fprintf(&sb, "Биржа:    %s [%s]\n", t.TradeExchange, t.Mode)
	fmt.Fprintf(&sb, "Цена входа: <b>%s %s</b>\n", FormatPrice(t.EntryPrice), cur)
	fmt.Fprintf(&sb, "Количество: %s\n", formatQty(t.Qty))
	fmt.Fprintf(&sb, "Сумма:      <b>%.2f %s</b>\n", t.EntryPrice*t.Qty, cur)
	if t.TargetPrice != nil {
		fmt.Fprintf(&sb, "Цель (TP): %s %s\n", FormatPrice(*t.TargetPrice), cur)
	}
	if t.StopLossPrice != nil {
		fmt.Fprintf(&sb, "Стоп (SL): %s %s\n", FormatPrice(*t.StopLossPrice), cur)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func formatTradeCloseMsg(t *trade.Trade) string {
	cur := CurrencyLabel(t.TradeExchange)
	emoji, reason := exitLabel(t.ExitReason)
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s <b>Сделка закрыта — %s</b>\n", emoji, reason)
	fmt.Fprintf(&sb, "Стратегия: %s\n", strategyLabel(t.Strategy))
	fmt.Fprintf(&sb, "Символ:    <b>%s</b>\n", t.Symbol)
	fmt.Fprintf(&sb, "Биржа:     %s [%s]\n", t.TradeExchange, t.Mode)
	fmt.Fprintf(&sb, "Открыта:   %s\n", t.OpenedAt.Format("15:04:05"))
	if t.ClosedAt != nil {
		fmt.Fprintf(&sb, "Удержание: %s\n", formatDuration(t.ClosedAt.Sub(t.OpenedAt)))
	}
	fmt.Fprintf(&sb, "Вход → Выход: %s → %s %s\n", FormatPrice(t.EntryPrice), exitPrice(t), cur)
	fmt.Fprintf(&sb, "Количество: %s\n", formatQty(t.Qty))
	if t.CommissionUSDT != nil {
		fmt.Fprintf(&sb, "Комиссия: %s %s\n", FormatPrice(*t.CommissionUSDT), cur)
	}
	if t.PnlUSDT != nil {
		fmt.Fprintf(&sb, "PnL: <b>%s %s</b>", formatPnl(*t.PnlUSDT), cur)
		invested := t.EntryPrice * t.Qty
		if invested > 0 {
			pnlPct := *t.PnlUSDT / invested * 100
			fmt.Fprintf(&sb, " (<b>%s</b>)", formatChangePct(pnlPct))
		}
		sb.WriteString("\n")
	}
	if t.ExitOrderID != "" {
		fmt.Fprintf(&sb, "Order ID:  <code>%s</code>\n", t.ExitOrderID)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func formatTradeCloseErrorMsg(t *trade.Trade, err error) string {
	var sb strings.Builder
	sb.WriteString("⚠️ <b>Ошибка закрытия ордера</b>\n")
	fmt.Fprintf(&sb, "Стратегия: %s\n", strategyLabel(t.Strategy))
	fmt.Fprintf(&sb, "Символ:   <b>%s</b>\n", t.Symbol)
	fmt.Fprintf(&sb, "Биржа:    %s [%s]\n", t.TradeExchange, t.Mode)
	fmt.Fprintf(&sb, "Количество: %s\n", formatQty(t.Qty))
	fmt.Fprintf(&sb, "Цена входа: %s\n", FormatPrice(t.EntryPrice))
	fmt.Fprintf(&sb, "Ошибка: <code>%s</code>\n", err.Error())
	return strings.TrimRight(sb.String(), "\n")
}
