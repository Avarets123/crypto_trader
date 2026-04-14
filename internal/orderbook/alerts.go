package orderbook

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	exchange_orders "github.com/osman/bot-traider/internal/exchange_orders"
	"github.com/osman/bot-traider/internal/shared/telegram"
	"github.com/osman/bot-traider/internal/shared/utils"
)

// bookSnapshot — снимок объёма и цены стакана в момент времени.
type bookSnapshot struct {
	BidVol   float64
	AskVol   float64
	MidPrice float64
	OBI      float64
}

// AlertService мониторит изменения объёма стакана и список топ-15 монет.
type AlertService struct {
	mu        sync.Mutex
	symbols   []string
	baselines map[string]bookSnapshot // базовый снимок с момента подписки на символ
	currents  map[string]bookSnapshot // последний снимок (для хранения текущих значений)
	bookSvc   *Service
	notifier  *telegram.Notifier
	threadID  int
	cfg       AlertsConfig
	log       *zap.Logger
	tradeAgg  *exchange_orders.TradeAggregator
}

// NewAlertService создаёт AlertService.
func NewAlertService(
	bookSvc *Service,
	notifier *telegram.Notifier,
	threadID int,
	cfg AlertsConfig,
	log *zap.Logger,
) *AlertService {
	return &AlertService{
		baselines: make(map[string]bookSnapshot),
		currents:  make(map[string]bookSnapshot),
		bookSvc:   bookSvc,
		notifier:  notifier,
		threadID:  threadID,
		cfg:       cfg,
		log:       log,
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

// Start запускает мониторинг объёма стакана. Блокирует до ctx.Done().
// Обновление списка символов происходит через OnSymbolsChanged, а не по таймеру.
func (s *AlertService) Start(ctx context.Context) {
	// Загружаем начальный список символов без уведомления
	s.loadInitialSymbols()

	checkTicker := time.NewTicker(time.Duration(s.cfg.CheckIntervalSec) * time.Second)
	defer checkTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-checkTicker.C:
			s.checkVolumes(ctx)
		}
	}
}

// OnSymbolsChanged вызывается TopVolatileProvider при изменении топ-листа.
// Обновляет внутренний список символов и отправляет уведомление в Telegram.
func (s *AlertService) OnSymbolsChanged(ctx context.Context, added, removed []string) {
	if len(added) == 0 && len(removed) == 0 {
		return
	}

	s.mu.Lock()
	newSymbols := make([]string, 0, len(s.symbols)+len(added))
	newSymbols = append(newSymbols, s.symbols...)

	// Удаляем выбывшие
	if len(removed) > 0 {
		removedSet := make(map[string]struct{}, len(removed))
		for _, sym := range removed {
			removedSet[sym] = struct{}{}
		}
		filtered := newSymbols[:0]
		for _, sym := range newSymbols {
			if _, ok := removedSet[sym]; !ok {
				filtered = append(filtered, sym)
			}
		}
		newSymbols = filtered
		for _, sym := range removed {
			delete(s.baselines, sym)
			delete(s.currents, sym)
			if s.tradeAgg != nil {
				s.tradeAgg.Reset(sym)
			}
		}
	}

	// Добавляем новые
	newSymbols = append(newSymbols, added...)
	s.symbols = newSymbols
	s.mu.Unlock()

	s.log.Info("orderbook alerts: symbol list updated",
		zap.Strings("added", added),
		zap.Strings("removed", removed),
	)

	msg := formatSymbolsUpdate(added, removed)
	s.notifier.SendToThread(ctx, msg, s.threadID)
}

// loadInitialSymbols логирует начальный список символов без отправки уведомлений.
func (s *AlertService) loadInitialSymbols() {
	s.mu.Lock()
	symbols := make([]string, len(s.symbols))
	copy(symbols, s.symbols)
	s.mu.Unlock()
	s.log.Info("orderbook alerts: initial symbols loaded", zap.Strings("symbols", symbols))
}

// volumeAlertEntry — данные одного символа для сводного алерта.
type volumeAlertEntry struct {
	symbol      string
	prevPrice   float64
	currPrice   float64
	prevBidVol  float64
	currBidVol  float64
	prevAskVol  float64
	currAskVol  float64
	changePct   float64
	prevOBI     float64
	currOBI     float64
	bullScore   float64
	trades      exchange_orders.TradeStats
}

// checkVolumes проверяет изменение объёма стакана для каждого символа
// и отправляет одно сводное сообщение по всем изменениям.
func (s *AlertService) checkVolumes(ctx context.Context) {
	defer utils.TimeTracker(s.log, "checkVolume")()
	s.mu.Lock()
	symbols := make([]string, len(s.symbols))
	copy(symbols, s.symbols)
	s.mu.Unlock()

	var entries []volumeAlertEntry

	for _, sym := range symbols {
		ob, ok := s.bookSvc.GetBook(sym)
		if !ok {
			s.log.Warn("orderbook alerts: book not available", zap.String("symbol", sym))
			continue
		}

		// Объём покупок (bids) и продаж (asks) в USDT.
		// Используем только топ-50 уровней: они несут реальное торговое давление,
		// дальние ордера только размывают OBI-сигнал.
		const obiDepth = 20
		var bidVol, askVol float64
		for i, e := range ob.Bids {
			if i >= obiDepth {
				break
			}
			p, _ := strconv.ParseFloat(e.Price, 64)
			q, _ := strconv.ParseFloat(e.Qty, 64)
			bidVol += p * q
		}
		for i, e := range ob.Asks {
			if i >= obiDepth {
				break
			}
			p, _ := strconv.ParseFloat(e.Price, 64)
			q, _ := strconv.ParseFloat(e.Qty, 64)
			askVol += p * q
		}
		vol := bidVol + askVol

		// Mid-price = среднее лучшего бида и лучшего аска
		mid := 0.0
		if len(ob.Bids) > 0 && len(ob.Asks) > 0 {
			bp, _ := strconv.ParseFloat(ob.Bids[0].Price, 64)
			ap, _ := strconv.ParseFloat(ob.Asks[0].Price, 64)
			mid = (bp + ap) / 2
		}

		obi := CalcOBI(bidVol, askVol)
		curr := bookSnapshot{BidVol: bidVol, AskVol: askVol, MidPrice: mid, OBI: obi}

		s.mu.Lock()
		baseline, hasBaseline := s.baselines[sym]
		if !hasBaseline {
			// Первый тик — фиксируем baseline, алерт не отправляем
			s.baselines[sym] = curr
			s.currents[sym] = curr
			s.mu.Unlock()
			s.log.Debug("orderbook alerts: baseline set", zap.String("symbol", sym))
			continue
		}
		s.currents[sym] = curr
		s.mu.Unlock()

		prevVol := baseline.BidVol + baseline.AskVol
		changePct := 0.0
		if prevVol > 0 {
			changePct = (vol - prevVol) / prevVol * 100
		}

		s.log.Debug("orderbook alerts: snapshot",
			zap.String("symbol", sym),
			zap.Float64("baseline_vol", prevVol),
			zap.Float64("curr_vol", vol),
			zap.Float64("change_pct", changePct),
		)

		// Суммарная статистика сделок с baseline — для отображения в алерте.
		var trades exchange_orders.TradeStats
		// Статистика за скользящее окно — для сигналов (агрессивность, TFI, VolImb).
		var win exchange_orders.TradeStats
		if s.tradeAgg != nil {
			trades = s.tradeAgg.Get(sym, s.log)
			win = s.tradeAgg.GetWindow(sym, time.Duration(s.cfg.TradeWindowSec)*time.Second, s.log)
		}

		// Trade Flow Imbalance: реальные исполненные сделки за окно, не спуфится.
		tfi := 0.0
		if tc := float64(win.BuyCount + win.SellCount); tc > 0 {
			tfi = float64(win.BuyCount-win.SellCount) / tc
		}
		// Volume Imbalance: объём реальных сделок за окно.
		volImb := 0.0
		if tv := win.BuyVolume + win.SellVolume; tv > 0 {
			volImb = (win.BuyVolume - win.SellVolume) / tv
		}
		// AskDrain: продавцы уходят из стакана → bullish (>0) или приходят → bearish (<0).
		askDrain := 0.0
		if baseline.AskVol > 0 {
			askDrain = clampF((baseline.AskVol-askVol)/baseline.AskVol, -1, 1)
		}
		// AggrRatio: средний размер покупки vs продажи за окно.
		// Крупные покупки при малом количестве → кит набирает позицию.
		aggrRatio := 0.0
		if win.BuyCount > 0 && win.SellCount > 0 {
			avgBuy := win.BuyVolume / float64(win.BuyCount)
			avgSell := win.SellVolume / float64(win.SellCount)
			if total := avgBuy + avgSell; total > 0 {
				aggrRatio = (avgBuy - avgSell) / total
			}
		} else if win.BuyCount > 0 {
			aggrRatio = 1.0
		} else if win.SellCount > 0 {
			aggrRatio = -1.0
		}

		bullScore := CalcBullScore(obi, obi-baseline.OBI, tfi, volImb, askDrain, aggrRatio)

		entries = append(entries, volumeAlertEntry{
			symbol:     sym,
			prevPrice:  baseline.MidPrice,
			currPrice:  mid,
			prevBidVol: baseline.BidVol,
			currBidVol: bidVol,
			prevAskVol: baseline.AskVol,
			currAskVol: askVol,
			changePct:  changePct,
			prevOBI:    baseline.OBI,
			currOBI:    obi,
			bullScore:  bullScore,
			trades:     trades,
		})
	}

	if len(entries) == 0 {
		return
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].bullScore > entries[j].bullScore
	})

	msg := formatVolumeAlertBatch(entries)
	s.notifier.SendToThread(ctx, msg, s.threadID)
}


func formatVolumeAlertBatch(entries []volumeAlertEntry) string {
	var sb strings.Builder
	sb.WriteString("📊 <b>Изменения объёма стакана</b>\n")

	for _, e := range entries {
		priceChangePct := 0.0
		if e.prevPrice > 0 {
			priceChangePct = (e.currPrice - e.prevPrice) / e.prevPrice * 100
		}
		priceSign := "+"
		if priceChangePct < 0 {
			priceSign = ""
		}
		totalSign := "+"
		if e.changePct < 0 {
			totalSign = ""
		}

		obiSign := ""
		if e.currOBI >= 0 {
			obiSign = "+"
		}
		bullSign := ""
		if e.bullScore >= 0 {
			bullSign = "+"
		}
		sb.WriteString(fmt.Sprintf(
			"\n<b>%s</b>\nЦена:    $%.2f → $%.2f  (%s%.2f%%)\nПокупка: %s → %s\nПродажа: %s → %s\nИтого:   %s → %s  (%s%.1f%%)\nOBI:     %s→ %s%s%.2f\nСигнал:  %s %s%.2f",
			e.symbol,
			e.prevPrice, e.currPrice, priceSign, priceChangePct,
			formatAlertVol(e.prevBidVol), formatAlertVol(e.currBidVol),
			formatAlertVol(e.prevAskVol), formatAlertVol(e.currAskVol),
			formatAlertVol(e.prevBidVol+e.prevAskVol), formatAlertVol(e.currBidVol+e.currAskVol), totalSign, e.changePct,
			formatOBI(e.prevOBI), OBISignal(e.currOBI), obiSign, e.currOBI,
			BullSignal(e.bullScore), bullSign, e.bullScore,
		))
		if e.trades.BuyCount > 0 || e.trades.SellCount > 0 {
			sb.WriteString(fmt.Sprintf(
				"\n🟢 %s (%d сд.)  🔴 %s (%d сд.)",
				formatAlertVol(e.trades.BuyVolume), e.trades.BuyCount,
				formatAlertVol(e.trades.SellVolume), e.trades.SellCount,
			))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// formatOBI возвращает строку вида "+0.42" или "-0.15" (без эмодзи).
func formatOBI(obi float64) string {
	if obi >= 0 {
		return fmt.Sprintf("+%.2f ", obi)
	}
	return fmt.Sprintf("%.2f ", obi)
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
