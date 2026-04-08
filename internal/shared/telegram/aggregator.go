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
	EventPump        EventType = "pump"
	EventCrash       EventType = "crash"
	EventSpread      EventType = "spread"
	EventVolumeSpike EventType = "volume_spike"
)

// Event — одно событие для агрегации.
type Event struct {
	Type            EventType
	Symbol          string
	Exchange        string // для spread: биржа с высокой ценой
	Exchange2       string // для spread: биржа с низкой ценой
	ChangePct       float64
	WindowSec       int
	TickerChangePct string  // изменение за 24ч от биржи (строка)
	SpikeType       string  // для volume_spike: "A" | "B"
	SpikeRatio      float64 // для volume_spike: current/avg
	VolumeUSDT      float64 // для volume_spike: текущий объём в USDT
	AvgUSDT         float64 // для volume_spike: скользящее среднее в USDT
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

// OnVolumeSpike — хук для detector.VolumeDetector.WithOnVolumeSpike.
func (a *Aggregator) OnVolumeSpike(e detector.VolumeEvent) {
	a.add(Event{
		Type:       EventVolumeSpike,
		Symbol:     e.Symbol,
		Exchange:   e.Exchange,
		ChangePct:  e.ChangePct,
		SpikeType:  string(e.Type),
		SpikeRatio: e.SpikeRatio,
		VolumeUSDT: e.Volume,
		AvgUSDT:    e.AvgVolume,
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

// deduplicateVolumeSpikes оставляет для каждого spikeType|symbol|exchange только последнее событие.
// Типы A и B дедуплицируются независимо, чтобы не затирать друг друга.
func deduplicateVolumeSpikes(events []Event) []Event {
	seen := make(map[string]int) // key → последний индекс
	for i, e := range events {
		seen[e.SpikeType+"|"+e.Symbol+"|"+e.Exchange] = i
	}
	out := make([]Event, 0, len(seen))
	for i, e := range events {
		if seen[e.SpikeType+"|"+e.Symbol+"|"+e.Exchange] == i {
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
	volSpikes := deduplicateVolumeSpikes(filterEvents(events, EventVolumeSpike))

	total := len(pumps) + len(crashes) + len(spreads) + len(volSpikes)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 <b>Сводка</b> (%d событий)\n", total))

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

	if len(volSpikes) > 0 {
		sb.WriteString(fmt.Sprintf("\n📈 <b>Volume Spike</b> (%d):\n", len(volSpikes)))
		for _, e := range volSpikes {
			var icon string
			switch e.SpikeType {
			case "A":
				icon = "🔵" // накопление
			case "B":
				icon = "🔴" // распродажа
			default:
				icon = "⚪"
			}
			sb.WriteString(fmt.Sprintf("  %s [%s] %s | %s  <b>%.1fx</b>  ΔP: <b>%.2f%%</b>  Vol: %.0fM\n",
				icon, e.SpikeType, e.Symbol, e.Exchange,
				e.SpikeRatio, e.ChangePct, e.VolumeUSDT/1_000_000))
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
