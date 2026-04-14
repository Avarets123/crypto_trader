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
	var sb strings.Builder
	sb.WriteString("🟢 <b>Сделка открыта</b>\n")
	fmt.Fprintf(&sb, "Стратегия: %s\n", strategyLabel(t.Strategy))
	fmt.Fprintf(&sb, "Символ:   <b>%s</b>\n", t.Symbol)
	fmt.Fprintf(&sb, "Биржа:    %s [%s]\n", t.TradeExchange, t.Mode)
	fmt.Fprintf(&sb, "Цена входа: <b>%s</b>\n", FormatPrice(t.EntryPrice))
	fmt.Fprintf(&sb, "Количество: %s\n", formatQty(t.Qty))
	if t.TargetPrice != nil {
		fmt.Fprintf(&sb, "Цель (TP): %s\n", FormatPrice(*t.TargetPrice))
	}
	if t.StopLossPrice != nil {
		fmt.Fprintf(&sb, "Стоп (SL): %s\n", FormatPrice(*t.StopLossPrice))
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
	fmt.Fprintf(&sb, "Вход → Выход: %s → %s\n", FormatPrice(t.EntryPrice), exitPrice(t))
	fmt.Fprintf(&sb, "Количество: %s\n", formatQty(t.Qty))
	if t.CommissionUSDT != nil {
		fmt.Fprintf(&sb, "Комиссия: %s USDT\n", FormatPrice(*t.CommissionUSDT))
	}
	if t.PnlUSDT != nil {
		fmt.Fprintf(&sb, "PnL: <b>%s USDT</b>\n", formatPnl(*t.PnlUSDT))
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
