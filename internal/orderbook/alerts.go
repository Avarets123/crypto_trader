package orderbook

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/binance"
	exchange_orders "github.com/osman/bot-traider/internal/exchange_orders"
	"github.com/osman/bot-traider/internal/shared/telegram"
)

// topVolatileGetter — интерфейс для получения топ волатильных монет.
type topVolatileGetter interface {
	GetTopVolatile(ctx context.Context, limit int) ([]binance.VolatileTicker, error)
}

// bookSnapshot — снимок объёма и цены стакана в момент времени.
type bookSnapshot struct {
	VolumeUSDT float64
	MidPrice   float64
}

// AlertService мониторит изменения объёма стакана и список топ-10 монет.
type AlertService struct {
	mu          sync.Mutex
	symbols     []string
	snapshots   map[string]bookSnapshot
	bookSvc     *Service
	restClient  topVolatileGetter
	notifier    *telegram.Notifier
	threadID    int
	cfg         AlertsConfig
	log         *zap.Logger
	tradeAgg    *exchange_orders.TradeAggregator
}

// NewAlertService создаёт AlertService.
func NewAlertService(
	bookSvc *Service,
	restClient topVolatileGetter,
	notifier *telegram.Notifier,
	threadID int,
	cfg AlertsConfig,
	log *zap.Logger,
) *AlertService {
	return &AlertService{
		snapshots:  make(map[string]bookSnapshot),
		bookSvc:    bookSvc,
		restClient: restClient,
		notifier:   notifier,
		threadID:   threadID,
		cfg:        cfg,
		log:        log,
	}
}

// WithTradeAggregator подключает агрегатор сделок для включения в алерты.
func (s *AlertService) WithTradeAggregator(agg *exchange_orders.TradeAggregator) {
	s.tradeAgg = agg
}

// SetSymbols задаёт начальный список символов для мониторинга.
func (s *AlertService) SetSymbols(symbols []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.symbols = make([]string, len(symbols))
	copy(s.symbols, symbols)
}

// Start запускает мониторинг. Блокирует до ctx.Done().
func (s *AlertService) Start(ctx context.Context) {
	// Загружаем начальный список символов без уведомления
	s.loadInitialSymbols(ctx)

	checkTicker := time.NewTicker(time.Duration(s.cfg.CheckIntervalSec) * time.Second)
	refreshTicker := time.NewTicker(time.Duration(s.cfg.RefreshIntervalMin) * time.Minute)
	defer checkTicker.Stop()
	defer refreshTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-checkTicker.C:
			s.checkVolumes(ctx)
		case <-refreshTicker.C:
			s.refreshSymbols(ctx)
		}
	}
}

// loadInitialSymbols загружает начальный список символов без отправки уведомлений.
func (s *AlertService) loadInitialSymbols(ctx context.Context) {
	tickers, err := s.restClient.GetTopVolatile(ctx, 10)
	if err != nil {
		s.log.Warn("orderbook alerts: failed to load initial symbols", zap.Error(err))
		return
	}
	symbols := make([]string, len(tickers))
	for i, t := range tickers {
		symbols[i] = t.Symbol
	}
	s.SetSymbols(symbols)
	s.log.Info("orderbook alerts: initial symbols loaded", zap.Strings("symbols", symbols))
}

// checkVolumes проверяет изменение объёма стакана для каждого символа.
func (s *AlertService) checkVolumes(ctx context.Context) {
	s.mu.Lock()
	symbols := make([]string, len(s.symbols))
	copy(symbols, s.symbols)
	s.mu.Unlock()

	for _, sym := range symbols {
		ob, ok := s.bookSvc.GetBook(sym)
		if !ok {
			s.log.Warn("orderbook alerts: book not available", zap.String("symbol", sym))
			continue
		}

		// Суммарный объём в USDT = сумма по всем уровням bids + asks
		var vol float64
		for _, e := range ob.Bids {
			p, _ := strconv.ParseFloat(e.Price, 64)
			q, _ := strconv.ParseFloat(e.Qty, 64)
			vol += p * q
		}
		for _, e := range ob.Asks {
			p, _ := strconv.ParseFloat(e.Price, 64)
			q, _ := strconv.ParseFloat(e.Qty, 64)
			vol += p * q
		}

		// Mid-price = среднее лучшего бида и лучшего аска
		mid := 0.0
		if len(ob.Bids) > 0 && len(ob.Asks) > 0 {
			bp, _ := strconv.ParseFloat(ob.Bids[0].Price, 64)
			ap, _ := strconv.ParseFloat(ob.Asks[0].Price, 64)
			mid = (bp + ap) / 2
		}

		s.mu.Lock()
		prev, hasPrev := s.snapshots[sym]
		s.snapshots[sym] = bookSnapshot{VolumeUSDT: vol, MidPrice: mid}
		s.mu.Unlock()

		// Первый тик — сохраняем без уведомления
		if !hasPrev {
			s.log.Debug("orderbook alerts: first snapshot", zap.String("symbol", sym))
			continue
		}

		if prev.VolumeUSDT == 0 {
			continue
		}

		changePct := (vol - prev.VolumeUSDT) / prev.VolumeUSDT * 100
		if math.Abs(changePct) < s.cfg.VolumeChangePct {
			s.log.Debug("orderbook alerts: no significant change",
				zap.String("symbol", sym),
				zap.Float64("change_pct", changePct),
			)
			continue
		}

		s.log.Info("orderbook alerts: volume spike detected",
			zap.String("symbol", sym),
			zap.Float64("prev_vol", prev.VolumeUSDT),
			zap.Float64("curr_vol", vol),
			zap.Float64("change_pct", changePct),
		)

		var trades exchange_orders.TradeStats
		if s.tradeAgg != nil {
			trades = s.tradeAgg.Flush(sym)
		}

		msg := formatVolumeAlert(sym, prev.MidPrice, mid, prev.VolumeUSDT, vol, changePct, trades)
		s.notifier.SendToThread(ctx, msg, s.threadID)
	}
}

// refreshSymbols обновляет список топ-10 волатильных монет.
func (s *AlertService) refreshSymbols(ctx context.Context) {
	tickers, err := s.restClient.GetTopVolatile(ctx, 10)
	if err != nil {
		s.log.Warn("orderbook alerts: failed to refresh symbols", zap.Error(err))
		return
	}

	newSymbols := make([]string, len(tickers))
	for i, t := range tickers {
		newSymbols[i] = t.Symbol
	}

	s.mu.Lock()
	oldSet := make(map[string]struct{}, len(s.symbols))
	for _, sym := range s.symbols {
		oldSet[sym] = struct{}{}
	}
	newSet := make(map[string]struct{}, len(newSymbols))
	for _, sym := range newSymbols {
		newSet[sym] = struct{}{}
	}

	var added, removed []string
	for sym := range newSet {
		if _, ok := oldSet[sym]; !ok {
			added = append(added, sym)
		}
	}
	for sym := range oldSet {
		if _, ok := newSet[sym]; !ok {
			removed = append(removed, sym)
		}
	}

	s.symbols = newSymbols
	for _, sym := range removed {
		delete(s.snapshots, sym)
	}
	s.mu.Unlock()

	if len(added) == 0 && len(removed) == 0 {
		s.log.Debug("orderbook alerts: symbol list unchanged")
		return
	}

	s.log.Info("orderbook alerts: symbol list updated",
		zap.Strings("added", added),
		zap.Strings("removed", removed),
	)

	msg := formatSymbolsUpdate(added, removed)
	s.notifier.SendToThread(ctx, msg, s.threadID)
}

func formatVolumeAlert(symbol string, prevPrice, currPrice, prevVol, currVol, changePct float64, trades exchange_orders.TradeStats) string {
	priceChangePct := 0.0
	if prevPrice > 0 {
		priceChangePct = (currPrice - prevPrice) / prevPrice * 100
	}
	priceSign := "+"
	if priceChangePct < 0 {
		priceSign = ""
	}
	volSign := "+"
	if changePct < 0 {
		volSign = ""
	}
	msg := fmt.Sprintf(
		"📊 <b>%s</b> — объём стакана изменился\n\nЦена:   $%.2f → $%.2f  (%s%.2f%%)\nОбъём:  %s → %s  (%s%.1f%%)",
		symbol,
		prevPrice, currPrice, priceSign, priceChangePct,
		formatAlertVol(prevVol), formatAlertVol(currVol), volSign, changePct,
	)
	if trades.BuyCount > 0 || trades.SellCount > 0 {
		msg += fmt.Sprintf(
			"\n\nСделки за интервал:\n🟢 Покупки:  %s  (%d сд.)\n🔴 Продажи: %s  (%d сд.)",
			formatAlertVol(trades.BuyVolume), trades.BuyCount,
			formatAlertVol(trades.SellVolume), trades.SellCount,
		)
	}
	return msg
}

func formatAlertVol(v float64) string {
	switch {
	case v >= 1_000_000:
		return fmt.Sprintf("$%.2fM", v/1_000_000)
	case v >= 1_000:
		return fmt.Sprintf("$%.1fK", v/1_000)
	default:
		return fmt.Sprintf("$%.0f", v)
	}
}

func formatSymbolsUpdate(added, removed []string) string {
	msg := "🔄 <b>Топ волатильных монет обновлён</b>\n"
	if len(added) > 0 {
		msg += "\n➕ Добавлены:  " + strings.Join(added, ", ")
	}
	if len(removed) > 0 {
		msg += "\n➖ Убраны:     " + strings.Join(removed, ", ")
	}
	return msg
}
