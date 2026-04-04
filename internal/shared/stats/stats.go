package stats

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// ExchangeStats хранит атомарные счётчики для одной биржи.
type ExchangeStats struct {
	updates uint64
	bytes   uint64
}

// Stats агрегирует статистику по всем биржам.
type Stats struct {
	exchanges map[string]*ExchangeStats
	startedAt time.Time
}

// New создаёт Stats с прединициализированными записями для каждой биржи.
func New(ctx context.Context, log *zap.Logger) *Stats {
	st := &Stats{
		startedAt: time.Now(),
		exchanges: map[string]*ExchangeStats{
			"binance": {},
			"gateio":  {},
			"bybit":   {},
			"okx":     {},
		},
	}

	st.LogPeriodically(ctx, time.Second*10, log)

	return st
}

// Record атомарно увеличивает счётчики для указанной биржи.
func (s *Stats) Record(exchange string, dataSize int) {
	e, ok := s.exchanges[exchange]
	if !ok {
		return
	}
	atomic.AddUint64(&e.updates, 1)
	atomic.AddUint64(&e.bytes, uint64(dataSize))
}

// LogPeriodically запускает горутину, которая каждые interval логирует счётчики.
func (s *Stats) LogPeriodically(ctx context.Context, interval time.Duration, log *zap.Logger) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				uptime := formatUptime(time.Since(s.startedAt))
				log.Info("uptime", zap.String("Runned time", uptime))
				for name, e := range s.exchanges {
					log.Info("stats",
						zap.String("exchange", name),
						zap.Uint64("updates", e.updates),
						zap.String("mb", fmt.Sprintf("%.3f", float64(e.bytes)/1_048_576)),
						zap.String("uptime", uptime),
					)
				}
			}
		}
	}()
}

// formatUptime форматирует длительность в виде "5d 3h 12m 23s".
func formatUptime(d time.Duration) string {
	d = d.Truncate(time.Second)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60
	return fmt.Sprintf("%dd %dh %dm %ds", days, hours, minutes, seconds)
}
