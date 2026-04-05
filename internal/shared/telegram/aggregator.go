package telegram

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
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
	Type       EventType
	Symbol     string
	Exchange   string // для spread: биржа с высокой ценой
	Exchange2  string // для spread: биржа с низкой ценой
	ChangePct  float64
	WindowSec  int
}

// Aggregator собирает события за окно времени и отправляет одно сводное сообщение.
type Aggregator struct {
	mu         sync.Mutex
	notifier   *Notifier
	windowDur  time.Duration
	buf        []Event
	timer      *time.Timer
	log        *zap.Logger
}

// NewAggregator создаёт Aggregator с заданным окном агрегации.
func NewAggregator(n *Notifier, windowSec int, log *zap.Logger) *Aggregator {
	log.Info("telegram aggregator created", zap.Int("window_sec", windowSec))
	return &Aggregator{
		notifier:  n,
		windowDur: time.Duration(windowSec) * time.Second,
		log:       log,
	}
}

// Add добавляет событие в буфер. Если буфер был пустой — запускает таймер окна.
func (a *Aggregator) Add(ctx context.Context, e Event) {
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
		a.timer = time.AfterFunc(a.windowDur, func() {
			a.flush(ctx)
		})
	}
}

// flush отправляет накопленные события и сбрасывает буфер.
func (a *Aggregator) flush(ctx context.Context) {
	a.mu.Lock()
	events := a.buf
	a.buf = nil
	a.timer = nil
	a.mu.Unlock()

	if len(events) == 0 {
		return
	}

	a.log.Info("aggregator: flushing events", zap.Int("count", len(events)))

	msg := formatSummary(events)
	a.notifier.Send(ctx, msg)
}

// formatSummary формирует сводное сообщение по всем накопленным событиям.
func formatSummary(events []Event) string {
	pumps := filterEvents(events, EventPump)
	crashes := filterEvents(events, EventCrash)
	spreads := filterEvents(events, EventSpread)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 <b>Сводка</b> (%d событий)\n", len(events)))

	if len(pumps) > 0 {
		sb.WriteString(fmt.Sprintf("\n🚀 <b>PUMP</b> (%d):\n", len(pumps)))
		for _, e := range pumps {
			sb.WriteString(fmt.Sprintf("  • %s | %s  <b>+%.2f%%</b> за %dс\n",
				e.Symbol, e.Exchange, e.ChangePct, e.WindowSec))
		}
	}

	if len(crashes) > 0 {
		sb.WriteString(fmt.Sprintf("\n💥 <b>CRASH</b> (%d):\n", len(crashes)))
		for _, e := range crashes {
			sb.WriteString(fmt.Sprintf("  • %s | %s  <b>%.2f%%</b> за %dс\n",
				e.Symbol, e.Exchange, e.ChangePct, e.WindowSec))
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
