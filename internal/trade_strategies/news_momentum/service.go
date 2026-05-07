// Package news_momentum реализует стратегию торговли по новостям с Ollama-классификацией.
//
// Жизненный цикл:
//  1. news.Service парсит RSS, Ollama классифицирует тикеры с confidence (high/medium/low).
//  2. news.Service эмитит NewsSignal в зарегистрированный SignalListener.
//  3. Service.OnNewsSignal проверяет фильтры (источник, символ, age, anti-frontrun, cooldown, cap).
//  4. При прохождении — открывает market buy через trade.Service.
//  5. watchTrade закрывает позицию по TP / SL / trailing / timeout.
//
// Стратегия торгует только high-confidence сигналы (если RequireHighConfidence=true)
// и только UP-направление (на споте шорты невозможны).
package news_momentum

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/news"
	"github.com/osman/bot-traider/internal/shared/exchange"
	"github.com/osman/bot-traider/internal/shared/indicators"
	"github.com/osman/bot-traider/internal/shared/risk"
	"github.com/osman/bot-traider/internal/ticker"
	"github.com/osman/bot-traider/internal/trade"
)

// StrategyName — идентификатор стратегии для записи в trade.Trade.Strategy и risk.Manager.
const StrategyName = "news_momentum"

// Service — сервис стратегии.
type Service struct {
	cfg           Config
	ctx           context.Context
	tradeSvc      *trade.Service
	klineProvider exchange.KlineProvider
	risk          *risk.Manager
	tracker       *Tracker

	mu        sync.Mutex
	cooldowns map[string]time.Time

	tier1Set     map[string]struct{}
	whitelistSet map[string]struct{}

	signalCounter int64

	log *zap.Logger
}

// New создаёт и стартует стратегию если NEWS_MOMENTUM_ENABLED=true.
// Возвращает nil если стратегия выключена.
func New(
	ctx context.Context,
	tradeSvc *trade.Service,
	klineProvider exchange.KlineProvider,
	tickerService *ticker.TickerService,
	riskMgr *risk.Manager,
	log *zap.Logger,
) *Service {
	cfg := LoadConfig()
	if !cfg.Enabled {
		log.Info("news_momentum: disabled (NEWS_MOMENTUM_ENABLED=false)")
		return nil
	}
	if tradeSvc == nil {
		log.Warn("news_momentum: tradeSvc is nil, strategy not started")
		return nil
	}
	if klineProvider == nil {
		log.Warn("news_momentum: klineProvider is nil, anti-frontrun filter will be skipped")
	}

	tier1 := make(map[string]struct{}, len(cfg.Tier1Sources))
	for _, s := range cfg.Tier1Sources {
		tier1[strings.ToLower(strings.TrimSpace(s))] = struct{}{}
	}
	whitelist := make(map[string]struct{}, len(cfg.WhitelistSymbols))
	for _, s := range cfg.WhitelistSymbols {
		whitelist[strings.ToUpper(strings.TrimSpace(s))] = struct{}{}
	}

	s := &Service{
		cfg:           cfg,
		ctx:           ctx,
		tradeSvc:      tradeSvc,
		klineProvider: klineProvider,
		risk:          riskMgr,
		tracker:       NewTracker(),
		cooldowns:     make(map[string]time.Time),
		tier1Set:      tier1,
		whitelistSet:  whitelist,
		log:           log.With(zap.String("component", "news_momentum")),
	}

	if tickerService != nil {
		tickerService.WithOnSend(s.OnTicker)
	}

	s.log.Info("news_momentum: enabled",
		zap.String("exchange", cfg.Exchange),
		zap.Float64("trade_amount_usdt", cfg.TradeAmountUSDT),
		zap.Int("max_positions", cfg.MaxPositions),
		zap.Float64("tp_pct", cfg.TPPct),
		zap.Float64("sl_pct", cfg.SLPct),
		zap.Int("timeout_min", cfg.TimeoutMin),
		zap.Int("max_news_age_min", cfg.MaxNewsAgeMin),
		zap.Float64("max_price_gain_1h_pct", cfg.MaxPriceGain1HPct),
		zap.Bool("require_high_confidence", cfg.RequireHighConfidence),
		zap.Strings("tier1_sources", cfg.Tier1Sources),
		zap.Int("whitelist_symbols", len(cfg.WhitelistSymbols)),
	)
	return s
}

// OnNewsSignal — листенер для news.Service.WithSignalListener.
// Запускается асинхронно из news.Service, обрабатывает сигнал и при прохождении
// фильтров открывает позицию.
func (s *Service) OnNewsSignal(signal news.NewsSignal) {
	atomic.AddInt64(&s.signalCounter, 1)
	log := s.log.With(
		zap.String("guid", signal.GUID),
		zap.String("source", signal.Source),
		zap.String("title", truncate(signal.Title, 80)),
	)

	// 1. Источник в tier-1?
	if _, ok := s.tier1Set[strings.ToLower(signal.Source)]; !ok {
		log.Debug("news_momentum: source not in tier1, skipping",
			zap.String("source", signal.Source),
		)
		return
	}

	// 2. Возраст новости.
	if signal.PublishedAt != nil && s.cfg.MaxNewsAgeMin > 0 {
		age := time.Since(*signal.PublishedAt)
		if age > time.Duration(s.cfg.MaxNewsAgeMin)*time.Minute {
			log.Info("news_momentum: news too old, skipping",
				zap.Duration("age", age),
				zap.Int("max_age_min", s.cfg.MaxNewsAgeMin),
			)
			return
		}
	}

	// 3. Отбираем UP-тикеры с нужным confidence.
	tickers := signal.Tickers
	if s.cfg.RequireHighConfidence {
		tickers = signal.HighConfidenceTickers()
	}
	upTickers := make([]news.TickerSignal, 0, len(tickers))
	for _, t := range tickers {
		if t.Direction == news.DirectionUp {
			upTickers = append(upTickers, t)
		}
	}
	if len(upTickers) == 0 {
		log.Debug("news_momentum: no UP tickers passing confidence filter",
			zap.String("raw_signal", signal.RawSignal),
		)
		return
	}

	log.Info("news_momentum: signal received, processing",
		zap.Int("up_tickers", len(upTickers)),
		zap.String("raw_signal", signal.RawSignal),
	)

	// 4. Для каждого тикера — асинхронно проверяем фильтры и открываем.
	for _, t := range upTickers {
		t := t
		go s.tryOpenForTicker(t, signal)
	}
}

// tryOpenForTicker применяет per-ticker фильтры и открывает сделку при их прохождении.
func (s *Service) tryOpenForTicker(t news.TickerSignal, signal news.NewsSignal) {
	symbol := normalizeSymbol(t.Symbol)
	log := s.log.With(
		zap.String("symbol", symbol),
		zap.String("source", signal.Source),
		zap.String("confidence", string(t.Confidence)),
	)

	// 1. Whitelist символов.
	if _, ok := s.whitelistSet[symbol]; !ok {
		log.Info("news_momentum: symbol not in whitelist, skipping")
		return
	}

	// 2. Уже есть открытая позиция?
	if s.tracker.Has(symbol) {
		log.Info("news_momentum: position already open for symbol, skipping")
		return
	}

	// 3. Cooldown.
	s.mu.Lock()
	if last, ok := s.cooldowns[symbol]; ok {
		dur := time.Duration(s.cfg.CooldownPerSymbolH) * time.Hour
		if time.Since(last) < dur {
			remaining := dur - time.Since(last)
			s.mu.Unlock()
			log.Info("news_momentum: cooldown active for symbol",
				zap.Duration("remaining", remaining),
			)
			return
		}
	}
	s.mu.Unlock()

	// 4. Лимит одновременных позиций.
	if s.tracker.Count() >= s.cfg.MaxPositions {
		log.Warn("news_momentum: max positions reached",
			zap.Int("current", s.tracker.Count()),
			zap.Int("max", s.cfg.MaxPositions),
		)
		return
	}

	// 5. Risk-manager.
	if s.risk != nil {
		if ok, reason := s.risk.CanOpen(StrategyName, s.cfg.TradeAmountUSDT); !ok {
			log.Warn("news_momentum: risk manager blocked open",
				zap.String("reason", reason),
			)
			return
		}
	}

	// 6. Anti-frontrun: цена не должна быть уже улетевшей.
	if s.klineProvider != nil && s.cfg.MaxPriceGain1HPct > 0 {
		change, currentPrice, err := s.priceChange1h(symbol)
		if err != nil {
			log.Warn("news_momentum: anti-frontrun check failed, skipping",
				zap.Error(err),
			)
			return
		}
		if change > s.cfg.MaxPriceGain1HPct {
			log.Info("news_momentum: price already moved, skipping (anti-frontrun)",
				zap.Float64("change_1h_pct", change),
				zap.Float64("max_allowed_pct", s.cfg.MaxPriceGain1HPct),
				zap.Float64("current_price", currentPrice),
			)
			return
		}
		log.Info("news_momentum: anti-frontrun passed",
			zap.Float64("change_1h_pct", change),
			zap.Float64("current_price", currentPrice),
		)
	}

	// 7. Открываем позицию.
	s.openTrade(symbol, signal, t)
}

// priceChange1h возвращает изменение цены за 1 час и текущую цену.
func (s *Service) priceChange1h(symbol string) (changePct, currentPrice float64, err error) {
	klines, err := s.klineProvider.GetKlines(s.ctx, symbol, "1h", 2)
	if err != nil {
		return 0, 0, fmt.Errorf("get klines: %w", err)
	}
	if len(klines) < 2 {
		return 0, 0, fmt.Errorf("not enough klines (got %d, need 2)", len(klines))
	}
	prev := klines[0].Close
	curr := klines[len(klines)-1].Close
	if prev <= 0 {
		return 0, 0, fmt.Errorf("invalid prev close: %f", prev)
	}
	return indicators.PriceChangePct(klines), curr, nil
}

// openTrade открывает позицию через trade.Service и регистрирует watch-горутину.
func (s *Service) openTrade(symbol string, signal news.NewsSignal, t news.TickerSignal) {
	log := s.log.With(zap.String("symbol", symbol), zap.String("source", signal.Source))

	// Получаем актуальную цену через klines (последний close); fallback — без цены.
	var entryPrice float64
	if s.klineProvider != nil {
		klines, err := s.klineProvider.GetKlines(s.ctx, symbol, "1m", 1)
		if err == nil && len(klines) > 0 {
			entryPrice = klines[len(klines)-1].Close
		} else if err != nil {
			log.Warn("news_momentum: failed to get entry price via klines, using market order without price hint", zap.Error(err))
		}
	}
	if entryPrice <= 0 {
		log.Warn("news_momentum: entry price unknown, aborting open")
		return
	}

	qty := s.cfg.TradeAmountUSDT / entryPrice
	if qty <= 0 {
		log.Warn("news_momentum: invalid qty calculated", zap.Float64("qty", qty))
		return
	}

	tpPrice := entryPrice * (1 + s.cfg.TPPct/100)
	slPrice := entryPrice * (1 - s.cfg.SLPct/100)

	log.Info("news_momentum: opening trade",
		zap.Float64("entry_price", entryPrice),
		zap.Float64("qty", qty),
		zap.Float64("tp_price", tpPrice),
		zap.Float64("sl_price", slPrice),
		zap.String("confidence", string(t.Confidence)),
	)

	id, err := s.tradeSvc.OpenTrade(s.ctx, trade.Trade{
		Strategy:      StrategyName,
		TradeExchange: s.cfg.Exchange,
		Symbol:        symbol,
		Side:          "buy",
		Qty:           qty,
		EntryPrice:    entryPrice,
		TargetPrice:   &tpPrice,
		StopLossPrice: &slPrice,
	})
	if err != nil {
		log.Error("news_momentum: open trade failed", zap.Error(err))
		// Cooldown даже на ошибку, чтобы не штурмовать API
		s.mu.Lock()
		s.cooldowns[symbol] = time.Now()
		s.mu.Unlock()
		return
	}

	if s.risk != nil {
		s.risk.RecordOpen(StrategyName, s.cfg.TradeAmountUSDT)
	}

	mt := &MomentumTrade{
		ID:           id,
		Symbol:       symbol,
		Source:       signal.Source,
		NewsGUID:     signal.GUID,
		EntryPrice:   entryPrice,
		TPPrice:      tpPrice,
		SLPrice:      slPrice,
		Qty:          qty,
		OpenedAt:     time.Now(),
		HighestPrice: entryPrice,
		PriceCh:      make(chan float64, 256),
		StopCh:       make(chan struct{}, 1),
	}
	s.tracker.Add(mt)

	s.mu.Lock()
	s.cooldowns[symbol] = time.Now()
	s.mu.Unlock()

	go s.watchTrade(mt)
}

// OnTicker — хук для tickerService, прокидывает цену в активные позиции.
func (s *Service) OnTicker(t ticker.Ticker) {
	if !strings.EqualFold(t.Exchange, s.cfg.Exchange) {
		return
	}
	mt, ok := s.tracker.Get(t.Symbol)
	if !ok {
		return
	}
	price, err := strconv.ParseFloat(t.Price, 64)
	if err != nil || price <= 0 {
		return
	}
	select {
	case mt.PriceCh <- price:
	default:
		// канал переполнен — пропускаем тик
	}
}

// watchTrade держит позицию открытой до выхода по одному из условий:
//   - TP: цена достигла tpPrice
//   - SL: цена опустилась ниже slPrice (или trail SL если активен)
//   - Trailing: при росте на TrailActivationPct% активируем, отступ TrailPct% от пика
//   - Timeout: TimeoutMin минут с момента открытия
func (s *Service) watchTrade(mt *MomentumTrade) {
	timeout := time.NewTimer(time.Duration(s.cfg.TimeoutMin) * time.Minute)
	defer timeout.Stop()

	log := s.log.With(
		zap.Int64("id", mt.ID),
		zap.String("symbol", mt.Symbol),
	)

	lastPrice := mt.EntryPrice
	for {
		select {
		case <-s.ctx.Done():
			return

		case <-timeout.C:
			log.Warn("news_momentum: timeout, force closing",
				zap.Float64("last_price", lastPrice),
				zap.Int("hold_min", int(time.Since(mt.OpenedAt).Minutes())),
			)
			s.closeTrade(mt, lastPrice, "timeout")
			return

		case <-mt.StopCh:
			return

		case price := <-mt.PriceCh:
			lastPrice = price

			if price > mt.HighestPrice {
				mt.HighestPrice = price
			}

			// Trailing stop активация.
			trailActivation := mt.EntryPrice * (1 + s.cfg.TrailActivationPct/100)
			if price >= trailActivation {
				newTrailSL := mt.HighestPrice * (1 - s.cfg.TrailPct/100)
				if !mt.TrailActive {
					mt.TrailActive = true
					mt.TrailSL = newTrailSL
					log.Info("news_momentum: trailing stop activated",
						zap.Float64("price", price),
						zap.Float64("trail_sl", mt.TrailSL),
					)
				} else if newTrailSL > mt.TrailSL {
					mt.TrailSL = newTrailSL
				}
			}

			// Trailing SL hit.
			if mt.TrailActive && price <= mt.TrailSL {
				log.Info("news_momentum: trailing SL hit",
					zap.Float64("price", price),
					zap.Float64("trail_sl", mt.TrailSL),
					zap.Float64("highest", mt.HighestPrice),
				)
				s.closeTrade(mt, price, "trail_sl")
				return
			}

			// Hard SL.
			if price <= mt.SLPrice {
				log.Warn("news_momentum: SL hit",
					zap.Float64("price", price),
					zap.Float64("sl_price", mt.SLPrice),
				)
				s.closeTrade(mt, price, "sl")
				return
			}

			// TP.
			if price >= mt.TPPrice {
				log.Info("news_momentum: TP hit",
					zap.Float64("price", price),
					zap.Float64("tp_price", mt.TPPrice),
				)
				s.closeTrade(mt, price, "tp")
				return
			}
		}
	}
}

func (s *Service) closeTrade(mt *MomentumTrade, exitPrice float64, reason string) {
	defer s.tracker.Remove(mt.Symbol)

	if err := s.tradeSvc.CloseTrade(s.ctx, mt.ID, exitPrice, reason); err != nil {
		s.log.Error("news_momentum: close trade failed",
			zap.Int64("id", mt.ID),
			zap.String("symbol", mt.Symbol),
			zap.String("reason", reason),
			zap.Error(err),
		)
		return
	}

	pnl := (exitPrice - mt.EntryPrice) * mt.Qty
	s.log.Info("news_momentum: trade closed",
		zap.Int64("id", mt.ID),
		zap.String("symbol", mt.Symbol),
		zap.String("reason", reason),
		zap.Float64("entry_price", mt.EntryPrice),
		zap.Float64("exit_price", exitPrice),
		zap.Float64("pnl_usdt", pnl),
		zap.Int("hold_min", int(time.Since(mt.OpenedAt).Minutes())),
	)
}

// normalizeSymbol преобразует тикер из новости (BTC, eth, Sol) в торговую пару (BTCUSDT).
// Если уже содержит USDT-суффикс — возвращает как есть.
func normalizeSymbol(ticker string) string {
	t := strings.ToUpper(strings.TrimSpace(ticker))
	if strings.HasSuffix(t, "USDT") || strings.HasSuffix(t, "USDC") || strings.HasSuffix(t, "BUSD") {
		return t
	}
	return t + "USDT"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
