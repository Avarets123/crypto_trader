package grid

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
)

// recordDigestEntry добавляет sell-цикл в digest-буфер символа.
// Вызывается из handleFilledOrder при каждом filled sell.
func (s *Service) recordDigestEntry(symbol string, pnl float64) {
	s.digestMu.Lock()
	defer s.digestMu.Unlock()

	b, ok := s.digestBuckets[symbol]
	if !ok {
		b = &digestBucket{
			pnlMin: pnl,
			pnlMax: pnl,
			since:  time.Now(),
		}
		s.digestBuckets[symbol] = b
	}
	b.cycles++
	b.pnlSum += pnl
	if pnl < b.pnlMin {
		b.pnlMin = pnl
	}
	if pnl > b.pnlMax {
		b.pnlMax = pnl
	}
}

// runDigestReporter периодически сбрасывает digest-буферы в TG-сообщение.
// Если за интервал не было sell-циклов ни на одном символе — ничего не отправляет.
func (s *Service) runDigestReporter(ctx context.Context) {
	interval := time.Duration(s.cfg.TGDigestIntervalMin) * time.Minute
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	s.log.Info("grid: digest reporter started",
		zap.Duration("interval", interval),
	)

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.flushDigest(ctx)
		}
	}
}

// flushDigest достаёт текущие buckets, очищает буфер и отправляет одно TG-сообщение.
// Если notifier не настроен — buckets просто очищаются, чтобы не накапливать память.
func (s *Service) flushDigest(ctx context.Context) {
	s.digestMu.Lock()
	if len(s.digestBuckets) == 0 {
		s.digestMu.Unlock()
		return
	}
	snapshot := s.digestBuckets
	s.digestBuckets = make(map[string]*digestBucket)
	s.digestMu.Unlock()

	if s.notifier == nil {
		return
	}

	// Сортируем символы по убыванию PnL (самые прибыльные сверху).
	type entry struct {
		symbol string
		b      *digestBucket
	}
	entries := make([]entry, 0, len(snapshot))
	for sym, b := range snapshot {
		entries = append(entries, entry{symbol: sym, b: b})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].b.pnlSum > entries[j].b.pnlSum
	})

	var sb strings.Builder
	sb.WriteString("📊 <b>Grid сводка</b>\n")
	totalCycles := 0
	totalPnL := 0.0
	for _, e := range entries {
		totalCycles += e.b.cycles
		totalPnL += e.b.pnlSum
		pnlSign := "+"
		if e.b.pnlSum < 0 {
			pnlSign = ""
		}
		sb.WriteString(fmt.Sprintf("\n<b>%s</b>: %d циклов | %s%.4f USDT (min %.4f / max %.4f)",
			e.symbol, e.b.cycles, pnlSign, e.b.pnlSum, e.b.pnlMin, e.b.pnlMax))
	}
	sb.WriteString("\n\n<b>Итого:</b>")
	pnlSign := "+"
	if totalPnL < 0 {
		pnlSign = ""
	}
	sb.WriteString(fmt.Sprintf(" %d циклов, %s%.4f USDT", totalCycles, pnlSign, totalPnL))

	go s.notifier.SendToThread(ctx, sb.String(), s.tradesThreadID)
}
