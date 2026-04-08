package detector

import (
	"context"
	"math"
	"strconv"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/ticker"
)

// SpikeType — тип Volume Spike события.
type SpikeType string

const (
	SpikeTypeA SpikeType = "A" // накопление (volume↑↑, price≈)
	SpikeTypeB SpikeType = "B" // распродажа (volume↑↑, price↓↓)
	SpikeTypeC SpikeType = "C" // памп       (volume↑↑, price↑↑)
)

// VolumeEvent описывает обнаруженный аномальный объём торгов.
type VolumeEvent struct {
	Symbol     string
	Exchange   string
	Type       SpikeType
	SpikeRatio float64 // currentVolumeUSDT / avgVolumeUSDT
	Volume     float64 // текущий объём в USDT
	AvgVolume  float64 // скользящее среднее объёма в USDT
	Price      float64
	ChangePct  float64
}

// volumeState хранит скользящее окно объёмов для одного символа на одной бирже.
// Отдельная структура от symbolState (та хранит цены для pump/crash детектора).
type volumeState struct {
	mu      sync.Mutex
	volumes []float64 // последние VolumeWindowSize значений volumeUSDT
}

// VolumeDetector обнаруживает аномальный объём торгов.
type VolumeDetector struct {
	cfg    Config
	log    *zap.Logger
	ctx    context.Context
	repo   *DetectorRepository
	ch     chan VolumeEvent
	states map[string]*volumeState // ключ: "SYMBOL|EXCHANGE"
	mu     sync.RWMutex
	hooks  []func(VolumeEvent)
}

// NewVolumeDetector создаёт VolumeDetector и запускает горутину обработки событий.
func NewVolumeDetector(ctx context.Context, repo *DetectorRepository, log *zap.Logger) *VolumeDetector {
	cfg := LoadConfig()
	d := &VolumeDetector{
		cfg:    cfg,
		log:    log,
		ctx:    ctx,
		repo:   repo,
		ch:     make(chan VolumeEvent, eventsBuffer),
		states: make(map[string]*volumeState),
	}
	log.Info("volume spike detector created",
		zap.Float64("spike_ratio", cfg.VolumeSpikeRatio),
		zap.Int("window_size", cfg.VolumeWindowSize),
		zap.Float64("min_usdt", cfg.VolumeMinUSDT),
		zap.Float64("price_change_pct", cfg.VolumePriceChangePct),
	)
	go d.run()
	return d
}

// WithOnVolumeSpike добавляет хук, вызываемый при обнаружении аномального объёма.
func (d *VolumeDetector) WithOnVolumeSpike(fn func(VolumeEvent)) *VolumeDetector {
	d.hooks = append(d.hooks, fn)
	return d
}

// OnTicker — точка входа из TickerService.WithOnSend.
func (d *VolumeDetector) OnTicker(t ticker.Ticker) {
	d.Update(t)
}

// getOrCreateVolume возвращает существующий volumeState или создаёт новый.
func (d *VolumeDetector) getOrCreateVolume(key string) *volumeState {
	d.mu.RLock()
	s, ok := d.states[key]
	d.mu.RUnlock()
	if ok {
		return s
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if s, ok = d.states[key]; ok {
		return s
	}
	s = &volumeState{}
	d.states[key] = s
	return s
}

// Update обновляет скользящее окно объёмов и проверяет на аномальность.
func (d *VolumeDetector) Update(t ticker.Ticker) {
	price, err := strconv.ParseFloat(t.Price, 64)
	if err != nil {
		d.log.Warn("volume detector: failed to parse price",
			zap.String("symbol", t.Symbol),
			zap.String("price", t.Price),
			zap.Error(err),
		)
		return
	}
	if price <= 0 {
		return
	}

	baseVolume, err := strconv.ParseFloat(t.Volume24h, 64)
	if err != nil {
		d.log.Warn("volume detector: failed to parse volume24h",
			zap.String("symbol", t.Symbol),
			zap.String("volume24h", t.Volume24h),
			zap.Error(err),
		)
		return
	}
	if baseVolume <= 0 {
		// нулевой объём — монета не торгуется, пропускаем без лишнего шума
		return
	}

	volumeUSDT := baseVolume * price

	if volumeUSDT < d.cfg.VolumeMinUSDT {
		return
	}

	key := t.Symbol + "|" + t.Exchange
	s := d.getOrCreateVolume(key)

	s.mu.Lock()
	s.volumes = append(s.volumes, volumeUSDT)
	if len(s.volumes) > d.cfg.VolumeWindowSize {
		s.volumes = s.volumes[len(s.volumes)-d.cfg.VolumeWindowSize:]
	}
	n := len(s.volumes)
	volumesCopy := make([]float64, n)
	copy(volumesCopy, s.volumes)
	s.mu.Unlock()

	if n < 2 {
		return
	}

	// среднее по предыдущим точкам (без текущей)
	prev := volumesCopy[:n-1]
	var sum float64
	for _, v := range prev {
		sum += v
	}
	avgVolume := sum / float64(len(prev))

	if avgVolume <= 0 {
		return
	}

	spikeRatio := volumeUSDT / avgVolume


	if spikeRatio < d.cfg.VolumeSpikeRatio {

		return
	}

	// парсим ChangePct: убираем '+' и '%'
	changePctStr := strings.ReplaceAll(t.ChangePct, "+", "")
	changePctStr = strings.ReplaceAll(changePctStr, "%", "")
	changePct, err := strconv.ParseFloat(strings.TrimSpace(changePctStr), 64)
	if err != nil {
		d.log.Warn("volume detector: failed to parse change_pct",
			zap.String("symbol", t.Symbol),
			zap.String("change_pct", t.ChangePct),
			zap.Error(err),
		)
		changePct = 0
	}

	var spikeType SpikeType
	switch {
	case math.Abs(changePct) <= d.cfg.VolumePriceChangePct:
		spikeType = SpikeTypeA
	case changePct < -d.cfg.VolumePriceChangePct:
		spikeType = SpikeTypeB
	default:
		spikeType = SpikeTypeC
	}

	d.log.Info("volume spike detected",
		zap.String("symbol", t.Symbol),
		zap.String("exchange", t.Exchange),
		zap.String("type", string(spikeType)),
		zap.Float64("spike_ratio", spikeRatio),
		zap.Float64("change_pct", changePct),
		zap.Float64("volume_usdt", volumeUSDT),
		zap.Float64("avg_volume_usdt", avgVolume),
	)

	e := VolumeEvent{
		Symbol:     t.Symbol,
		Exchange:   t.Exchange,
		Type:       spikeType,
		SpikeRatio: spikeRatio,
		Volume:     volumeUSDT,
		AvgVolume:  avgVolume,
		Price:      price,
		ChangePct:  changePct,
	}

	select {
	case d.ch <- e:
	default:
		d.log.Warn("volume detector: events queue full, event dropped",
			zap.String("symbol", t.Symbol),
			zap.String("exchange", t.Exchange),
		)
	}
}

// run читает события из канала, сохраняет в БД и вызывает все зарегистрированные хуки.
func (d *VolumeDetector) run() {
	for e := range d.ch {
		d.log.Debug("volume spike: saving to db",
			zap.String("symbol", e.Symbol),
			zap.String("exchange", e.Exchange),
			zap.String("type", string(e.Type)),
		)
		d.repo.SaveVolumeSpike(d.ctx, e)
		for _, fn := range d.hooks {
			fn(e)
		}
	}
}
