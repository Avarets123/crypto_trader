package notifications

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/shared/exchange"
	"github.com/osman/bot-traider/internal/shared/telegram"
	"github.com/osman/bot-traider/internal/trade"
)

// RecoverTrades загружает открытые позиции из Redis, закрывает их рыночным ордером,
// сохраняет в БД и удаляет из Redis. Возвращает список закрытых сделок.
func RecoverTrades(
	ctx context.Context,
	redisRepo *trade.TradeRedisRepository,
	clients map[string]exchange.RestClient,
	repo *trade.TradeRepository,
	log *zap.Logger,
) []trade.Trade {
	trades, err := redisRepo.LoadAll(ctx)
	if err != nil {
		log.Error("recover trades: failed to load from redis", zap.Error(err))
		return nil
	}
	if len(trades) == 0 {
		return nil
	}

	log.Warn("recover trades: found open positions in redis, closing all",
		zap.Int("count", len(trades)),
	)

	var closed []trade.Trade
	for _, t := range trades {
		client, ok := clients[t.TradeExchange]
		if !ok {
			log.Error("recover trades: no rest client for exchange, skipping",
				zap.String("exchange", t.TradeExchange),
				zap.String("symbol", t.Symbol),
			)
			// Удаляем из Redis чтобы не зависло навсегда
			if delErr := redisRepo.Delete(ctx, t.ID); delErr != nil {
				log.Warn("recover trades: delete from redis failed", zap.Error(delErr))
			}
			continue
		}

		exitPrice := t.EntryPrice
		closeSide := "sell"
		if t.Side == "sell" {
			closeSide = "buy"
		}
		result, err := client.PlaceMarketOrder(ctx, t.Symbol, closeSide, t.Qty)
		if err != nil {
			log.Error("recover trades: close order failed",
				zap.String("symbol", t.Symbol),
				zap.String("exchange", t.TradeExchange),
				zap.Error(err),
			)
		} else {
			if result.Price > 0 {
				exitPrice = result.Price
			}
			log.Info("recover trades: position closed",
				zap.String("symbol", t.Symbol),
				zap.Float64("entry_price", t.EntryPrice),
				zap.Float64("exit_price", exitPrice),
			)
		}

		const commissionRate = 0.001
		commission := (t.EntryPrice + exitPrice) * t.Qty * commissionRate
		priceDelta := exitPrice - t.EntryPrice
		if t.Side == "sell" {
			priceDelta = t.EntryPrice - exitPrice
		}
		pnl := priceDelta*t.Qty - commission
		now := time.Now()

		t.ExitPrice = &exitPrice
		t.ExitReason = "recovery"
		t.CommissionUSDT = &commission
		t.PnlUSDT = &pnl
		t.ClosedAt = &now

		if saveErr := repo.SaveClosedTrade(ctx, t); saveErr != nil {
			log.Warn("recover trades: save to db failed",
				zap.String("symbol", t.Symbol),
				zap.Error(saveErr),
			)
		}

		if delErr := redisRepo.Delete(ctx, t.ID); delErr != nil {
			log.Warn("recover trades: delete from redis failed", zap.Int64("id", t.ID), zap.Error(delErr))
		}

		closed = append(closed, *t)
	}

	return closed
}

// SendRecoveryNotification отправляет одно Telegram-сообщение о всех закрытых при старте позициях.
func SendRecoveryNotification(ctx context.Context, notifier *telegram.Notifier, threadID int, trades []trade.Trade) {
	if len(trades) == 0 {
		return
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "♻️ <b>Восстановление при старте — закрыто позиций: %d</b>\n", len(trades))

	for _, t := range trades {
		holdMin := 0
		if t.ClosedAt != nil {
			holdMin = int(t.ClosedAt.Sub(t.OpenedAt).Minutes())
		}

		sb.WriteString("\n")
		fmt.Fprintf(&sb, "%s <b>%s</b> [%s] | %s\n",
			strategyEmoji(t.Strategy), t.Symbol, strategyLabel(t.Strategy), t.TradeExchange)
		fmt.Fprintf(&sb, "  Вход: <code>%s</code>", FormatPrice(t.EntryPrice))
		if t.ExitPrice != nil {
			changePct := (*t.ExitPrice - t.EntryPrice) / t.EntryPrice * 100
			fmt.Fprintf(&sb, " → Выход: <code>%s</code>  %s\n", FormatPrice(*t.ExitPrice), formatChangePct(changePct))
		} else {
			sb.WriteString("\n")
		}
		if t.PnlUSDT != nil {
			fmt.Fprintf(&sb, "  PnL: <b>%s USDT</b>", formatPnl(*t.PnlUSDT))
		}
		if t.CommissionUSDT != nil {
			fmt.Fprintf(&sb, " | Комиссия: %s USDT", FormatPrice(*t.CommissionUSDT))
		}
		sb.WriteString("\n")
		fmt.Fprintf(&sb, "  Держали: %d мин\n", holdMin)
	}

	notifier.SendToThread(ctx, strings.TrimRight(sb.String(), "\n"), threadID)
}
