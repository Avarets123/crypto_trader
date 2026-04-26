package microscalping

import (
	"context"
	"sort"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// Имена этапов воронки сигналов. Используются как ключи в FilterCounters.
// Названия выбраны так, чтобы их удобно было грепать в логах.
const (
	// Лонг
	StageLongTotal       = "long_total"           // всего входящих buy-сделок
	StageLongWarmup      = "long_warmup"          // отсечён прогревом
	StageLongNoTradeWin  = "long_no_trade_window" // отсечён временным окном МСК
	StageLongEMABlock    = "long_ema_block"       // price <= EMA
	StageLongNoWall      = "long_no_absorbing_wall"
	StageLongNoBreakout  = "long_no_breakout"
	StageLongNotWhale    = "long_not_whale"
	StageLongLowVC       = "long_low_volume_cluster"
	StageLongPassed      = "long_passed_to_entry"

	// Шорт
	StageShortTotal      = "short_total"
	StageShortDisabled   = "short_disabled"
	StageShortWarmup     = "short_warmup"
	StageShortNoTradeWin = "short_no_trade_window"
	StageShortEMABlock   = "short_ema_block"
	StageShortNoPump     = "short_no_pump"
	StageShortNoNewWall  = "short_no_new_wall"
	StageShortCDPositive = "short_cd_positive"
	StageShortNotWhale   = "short_not_whale"
	StageShortNoDiverg   = "short_no_divergence"
	StageShortPassed     = "short_passed_to_entry"

	// Прочее
	StageWallNew = "wall_new_ask" // сколько раз вообще появлялась новая Ask-стена
)

// FilterCounters — потокобезопасные счётчики отсева сигналов по этапам.
// Все операции — атомарные инкременты, чтобы не блокироваться на высоком потоке сделок.
type FilterCounters struct {
	counters map[string]*int64
}

func newFilterCounters() *FilterCounters {
	keys := []string{
		StageLongTotal, StageLongWarmup, StageLongNoTradeWin, StageLongEMABlock,
		StageLongNoWall, StageLongNoBreakout, StageLongNotWhale, StageLongLowVC, StageLongPassed,
		StageShortTotal, StageShortDisabled, StageShortWarmup, StageShortNoTradeWin,
		StageShortEMABlock, StageShortNoPump, StageShortNoNewWall, StageShortCDPositive,
		StageShortNotWhale, StageShortNoDiverg, StageShortPassed,
		StageWallNew,
	}
	m := make(map[string]*int64, len(keys))
	for _, k := range keys {
		var v int64
		m[k] = &v
	}
	return &FilterCounters{counters: m}
}

// Inc увеличивает счётчик. Если ключ неизвестен — игнорирует (не паникует).
func (fc *FilterCounters) Inc(stage string) {
	if p, ok := fc.counters[stage]; ok {
		atomic.AddInt64(p, 1)
	}
}

// snapshot возвращает копию текущих значений и обнуляет счётчики.
func (fc *FilterCounters) snapshot() map[string]int64 {
	out := make(map[string]int64, len(fc.counters))
	for k, p := range fc.counters {
		out[k] = atomic.SwapInt64(p, 0)
	}
	return out
}

// dump формирует и логирует сводку. Пустая сводка (всё по нулям) логируется как warn —
// явный сигнал что поток сделок не доходит или фильтры абсолютно непроходимы.
func (fc *FilterCounters) dump(log *zap.Logger, period time.Duration) {
	snap := fc.snapshot()

	// Сортируем для стабильного вывода
	keys := make([]string, 0, len(snap))
	for k := range snap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fields := make([]zap.Field, 0, len(snap)+1)
	fields = append(fields, zap.Duration("period", period))
	var totalLong, totalShort int64
	for _, k := range keys {
		fields = append(fields, zap.Int64(k, snap[k]))
	}
	totalLong = snap[StageLongTotal]
	totalShort = snap[StageShortTotal]

	if totalLong == 0 && totalShort == 0 {
		log.Warn("microscalping filters: NO trades reached strategy in period — check exchange_orders pipeline", fields...)
		return
	}
	log.Info("microscalping filters: funnel snapshot", fields...)
}

// runReporter периодически дампит счётчики в лог. Завершается при ctx.Done().
func (s *Service) runReporter(ctx context.Context, period time.Duration) {
	if period <= 0 {
		return
	}
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.counters.dump(s.log, period)
		}
	}
}
