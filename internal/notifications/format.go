package notifications

import (
	"fmt"
	"strings"

	"github.com/osman/bot-traider/internal/trade"
)

func strategyEmoji(strategy string) string {
	switch strategy {
	case "arb":
		return "⚡"
	case "momentum", "pump":
		return "🚀"
	case "microscalping":
		return "🌊"
	default:
		return "🔵"
	}
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
		return FormatPrice(*t.ExitPrice)
	}
	return "—"
}

// FormatPrice форматирует цену в зависимости от величины.
func FormatPrice(p float64) string {
	if p >= 1000 {
		return fmt.Sprintf("%.2f", p)
	}
	if p >= 1 {
		return fmt.Sprintf("%.4f", p)
	}
	return fmt.Sprintf("%.8f", p)
}

func formatChangePct(pct float64) string {
	if pct >= 0 {
		return fmt.Sprintf("+%.2f%%", pct)
	}
	return fmt.Sprintf("%.2f%%", pct)
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
