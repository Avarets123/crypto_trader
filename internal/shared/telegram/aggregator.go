package telegram

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/shared/comparator"
	"github.com/osman/bot-traider/internal/shared/detector"
)

// EventType — тип агрегируемого события.
type EventType string

const (
	EventPump   EventType = "pump"
	EventCrash  EventType = "crash"
	EventSpread EventType = "spread"
)

// Event — одно событие для агрегации.
type Event struct {
	Type            EventType
	Symbol          string
	Exchange        string // для spread: биржа с высокой ценой
	Exchange2       string // для spread: биржа с низкой ценой
	ChangePct       float64
	WindowSec       int
	TickerChangePct string // изменение за 24ч от биржи (строка)
}

// Aggregator собирает события за окно времени и отправляет одно сводное сообщение.
type Aggregator struct {
	mu        sync.Mutex
	ctx       context.Context
	notifier  *Notifier
	windowDur time.Duration
	buf       []Event
	timer     *time.Timer
	log       *zap.Logger
}

// NewAggregator создаёт Aggregator с заданным окном агрегации.
func NewAggregator(ctx context.Context, n *Notifier, windowSec int, log *zap.Logger) *Aggregator {
	log.Info("telegram aggregator created", zap.Int("window_sec", windowSec))
	return &Aggregator{
		ctx:       ctx,
		notifier:  n,
		windowDur: time.Duration(windowSec) * time.Second,
		log:       log,
	}
}

// OnPumpEvent — хук для detector.WithOnPumpEvent.
func (a *Aggregator) OnPumpEvent(e *detector.DetectorEvent) {
	a.add(Event{
		Type:            EventPump,
		Symbol:          e.Symbol,
		Exchange:        e.Exchange,
		ChangePct:       e.ChangePct,
		WindowSec:       e.WindowSec,
		TickerChangePct: e.TickerChangePct,
	})
}

// OnCrashEvent — хук для detector.WithOnCrashEvent.
func (a *Aggregator) OnCrashEvent(e *detector.DetectorEvent) {
	a.add(Event{
		Type:            EventCrash,
		Symbol:          e.Symbol,
		Exchange:        e.Exchange,
		ChangePct:       e.ChangePct,
		WindowSec:       e.WindowSec,
		TickerChangePct: e.TickerChangePct,
	})
}

// OnSpreadOpenEvent — хук для comparator.WithOnSpreadOpenEvent.
func (a *Aggregator) OnSpreadOpenEvent(e *comparator.SpreadEvent) {
	a.add(Event{
		Type:      EventSpread,
		Symbol:    e.Symbol,
		Exchange:  e.ExchangeHigh,
		Exchange2: e.ExchangeLow,
		ChangePct: e.MaxSpreadPct,
	})
}

// add добавляет событие в буфер. Если буфер был пустой — запускает таймер окна.
func (a *Aggregator) add(e Event) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.log.Debug("aggregator: event added",
		zap.String("type", string(e.Type)),
		zap.String("symbol", e.Symbol),
		zap.String("exchange", e.Exchange),
		zap.Float64("change_pct", e.ChangePct),
	)

	a.buf = append(a.buf, e)

	// таймер запускается только при первом событии в окне
	if len(a.buf) == 1 {
		a.log.Debug("aggregator: window started", zap.Duration("window", a.windowDur))
		a.timer = time.AfterFunc(a.windowDur, a.flush)
	}
}

// flush отправляет накопленные события и сбрасывает буфер.
func (a *Aggregator) flush() {
	a.mu.Lock()
	events := a.buf
	a.buf = nil
	a.timer = nil
	a.mu.Unlock()

	if len(events) == 0 {
		return
	}

	a.log.Info("aggregator: flushing events", zap.Int("count", len(events)))
	a.notifier.Send(a.ctx, formatSummary(events))
}

// deduplicateBySymbol оставляет для каждого symbol|exchange только последнее событие.
func deduplicateBySymbol(events []Event) []Event {
	seen := make(map[string]int) // key → последний индекс
	for i, e := range events {
		seen[e.Symbol+"|"+e.Exchange] = i
	}
	out := make([]Event, 0, len(seen))
	for i, e := range events {
		if seen[e.Symbol+"|"+e.Exchange] == i {
			out = append(out, e)
		}
	}
	return out
}

// formatSummary формирует сводное сообщение по всем накопленным событиям.
func formatSummary(events []Event) string {
	pumps := deduplicateBySymbol(filterEvents(events, EventPump))
	crashes := deduplicateBySymbol(filterEvents(events, EventCrash))
	spreads := filterEvents(events, EventSpread)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 <b>Сводка</b> (%d событий)\n", len(events)))

	if len(pumps) > 0 {
		sb.WriteString(fmt.Sprintf("\n🚀 <b>PUMP</b> (%d):\n", len(pumps)))
		for _, e := range pumps {
			line := fmt.Sprintf("  • %s | %s  <b>+%.2f%%</b> за %dс",
				e.Symbol, e.Exchange, e.ChangePct, e.WindowSec)
			if e.TickerChangePct != "" {
				line += fmt.Sprintf("  (24ч: %s)", e.TickerChangePct)
			}
			sb.WriteString(line + "\n")
		}
	}

	if len(crashes) > 0 {
		sb.WriteString(fmt.Sprintf("\n💥 <b>CRASH</b> (%d):\n", len(crashes)))
		for _, e := range crashes {
			line := fmt.Sprintf("  • %s | %s  <b>%.2f%%</b> за %dс",
				e.Symbol, e.Exchange, e.ChangePct, e.WindowSec)
			if e.TickerChangePct != "" {
				line += fmt.Sprintf("  (24ч: %s)", e.TickerChangePct)
			}
			sb.WriteString(line + "\n")
		}
	}

	if len(spreads) > 0 {
		sb.WriteString(fmt.Sprintf("\n⚡ <b>SPREAD</b> (%d):\n", len(spreads)))
		for _, e := range spreads {
			sb.WriteString(fmt.Sprintf("  • %s  <b>%.2f%%</b>  %s → %s\n",
				e.Symbol, e.ChangePct, e.Exchange, e.Exchange2))
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}

func filterEvents(events []Event, t EventType) []Event {
	var out []Event
	for _, e := range events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}
