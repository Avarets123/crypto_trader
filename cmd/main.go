package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/osman/bot-traider/internal/binance"
	exchange_orders "github.com/osman/bot-traider/internal/exchange_orders"
	"github.com/osman/bot-traider/internal/news"
	"github.com/osman/bot-traider/internal/ollama"
	"github.com/osman/bot-traider/internal/orderbook"
	"github.com/osman/bot-traider/internal/shared/comparator"
	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
	"github.com/osman/bot-traider/internal/shared/db"
	"github.com/osman/bot-traider/internal/shared/detector"
	"github.com/osman/bot-traider/internal/shared/exchange"
	"github.com/osman/bot-traider/internal/shared/logger"
	redisclient "github.com/osman/bot-traider/internal/shared/redis"
	"github.com/osman/bot-traider/internal/shared/stats"
	"github.com/osman/bot-traider/internal/shared/telegram"
	"github.com/osman/bot-traider/internal/ticker"
	"github.com/osman/bot-traider/internal/trade"
	"github.com/osman/bot-traider/internal/trade_strategies/grid"
	"github.com/osman/bot-traider/internal/trade_strategies/momentum"
	"github.com/osman/bot-traider/internal/trade_strategies/volatile"
)

func main() {
	godotenv.Load()

	cfg := sharedconfig.LoadBase()
	log := logger.New(cfg.LogLevel)


	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	defer log.Sync() //nolint:errcheck

	pool, err := db.NewPool(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Fatal("db connect failed", zap.Error(err))
	}
	defer pool.Close()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		log.Info("shutting down")
		cancel()
	}()

	binanceRest := binance.NewRestClient(ctx, log.With(zap.String("component", "binance-rest")))

	tgNotifier, tgAgg := initTg(ctx, log)

	// --- Order Book ---
	obStore := orderbook.NewStore()
	obFetcher := orderbook.NewFetcher(
		binanceRest,
		sharedconfig.GetEnv("BINANCE_WS_URL", "wss://stream.binance.com:9443"),
		log.With(zap.String("component", "orderbook")),
	)
	obSvc := orderbook.NewService(obFetcher, obStore, log.With(zap.String("component", "orderbook")))

	// --- Top Volatile Provider — единственный источник топ-15 монет ---
	// Интервал обновления берётся из ORDERBOOK_REFRESH_INTERVAL_MIN (общий с orderbook-alerts).
	alertCfg := orderbook.LoadAlertsConfig()
	topProvider := binance.NewTopVolatileProvider(binanceRest, 15, log.With(zap.String("component", "top-volatile")))
	if err := topProvider.Fetch(ctx); err != nil {
		log.Fatal("top volatile: initial fetch failed", zap.Error(err))
	}

	go sendTopVolatile(ctx, log, topProvider.Tickers(), tgNotifier, obSvc)

	// --- Orderbook Alerts ---
	var alertSvc *orderbook.AlertService
	if alertCfg.Enabled {
		alertSvc = orderbook.NewAlertService(
			obSvc,
			tgNotifier,
			sharedconfig.GetEnvInt("TELEGRAM_NEWS_THREAD_ID", 0),
			alertCfg,
			log.With(zap.String("component", "orderbook-alerts")),
		)
		alertSvc.SetSymbols(topProvider.Symbols())
	}

	st := stats.New(ctx, log)

	// --- Exchange Orders ---
	// tradeAgg объявлен здесь чтобы volatile стратегия могла получить к нему доступ.
	var tradeAgg *exchange_orders.TradeAggregator
	var eoSvc *exchange_orders.Service
	if sharedconfig.GetEnvBool("EXCHANGE_ORDERS_ENABLED", false) {
		eoRepo := exchange_orders.NewRepository(pool, log.With(zap.String("component", "exchange-orders")))
		eoRepo.WithOnSaved(st.RecordExchangeOrders)
		eoFetcher := exchange_orders.NewFetcher(
			"binance",
			sharedconfig.GetEnv("BINANCE_WS_URL", "wss://stream.binance.com:9443"),
			eoRepo,
			log.With(zap.String("component", "exchange-orders")),
		)
		tradeAgg = exchange_orders.NewTradeAggregator()
		eoFetcher.WithOnTrade(tradeAgg.OnTrade)
		if alertSvc != nil {
			alertSvc.WithTradeAggregator(tradeAgg)
		}
		eoSvc = exchange_orders.NewService(eoFetcher, log.With(zap.String("component", "exchange-orders")))
		eoSvc.Start(ctx, topProvider.Symbols())
		log.Info("exchange_orders: enabled")
	} else {
		log.Info("exchange_orders: disabled (EXCHANGE_ORDERS_ENABLED=false)")
	}

	// Объявляем переменные заранее — хук захватит их по ссылке.
	// Сами сервисы создаются позже, после инициализации tickerService.
	var binanceTickerClient *binance.Client
	var volatileSvc *volatile.Service
	var tradeSvc *trade.Service

	// notifyExchanges отправляет обновлённый список символов биржевым клиентам.
	// К топ-15 добираются символы с открытыми позициями — чтобы не потерять тикеры
	// по валютам, которые выпали из топа пока по ним есть активная сделка.
	notifyExchanges := func() {
		all := mergeSymbols(topProvider.Symbols(), openTradeSymbols(tradeSvc))
		if binanceTickerClient != nil {
			binanceTickerClient.NotifySymbolsChanged(all)
		}
	}

	// Подписываемся на изменения топ-листа: обновляем стаканы, подписки exchange_orders и тикеры.
	// Хук регистрируется после создания всех зависимых сервисов.
	topProvider.WithOnSymbolsChanged(func(added, removed []string) {
		obSvc.OnSymbolsChanged(added, removed)
		if eoSvc != nil {
			eoSvc.OnSymbolsChanged(added, removed)
		}
		if alertSvc != nil {
			alertSvc.OnSymbolsChanged(ctx, added, removed)
		}
		if volatileSvc != nil {
			volatileSvc.OnSymbolsChanged(added, removed)
		}
		notifyExchanges()
	})
	// Запускаем фоновое обновление топ-листа с интервалом ORDERBOOK_REFRESH_INTERVAL_MIN.
	go topProvider.Run(ctx, time.Duration(alertCfg.RefreshIntervalMin)*time.Minute)

	// Запускаем алерт-сервис после инициализации exchange_orders
	if alertSvc != nil {
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			alertSvc.Start(ctx)
		}()
	}

	// Пересылаем ошибки в отдельный Telegram-топик
	errorsThreadID := sharedconfig.GetEnvInt("TELEGRAM_ERRORS_THREAD_ID", 0)
	if errorsThreadID > 0 {
		errCore := telegram.NewErrorCore(ctx, tgNotifier, errorsThreadID)
		log = log.WithOptions(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
			return zapcore.NewTee(core, errCore)
		}))
		log.Info("telegram error notifications enabled", zap.Int("thread_id", errorsThreadID))
	}

	// --- Ticker сервис ---
	repo := ticker.NewRepository(pool, log)
	tickerService := ticker.NewService(ctx, repo, log, ticker.LoadConfig())

	// --- Comparator (спред) ---
	spreadRepo := comparator.NewSpreadRepository(pool, log.With(zap.String("component", "comparator")))
	cmp := comparator.New(ctx, cfg.SpreadThresholdPct, spreadRepo, log.With(zap.String("component", "comparator")))

	// --- Detector (pump/crash) — только для Telegram уведомлений ---
	detectorRepo := detector.NewDetectorRepository(pool, log.With(zap.String("component", "detector")))
	det := detector.New(ctx, detector.LoadConfig(), detectorRepo, log.With(zap.String("component", "detector")))
	det.WithOnPumpEvent(func(e *detector.DetectorEvent) {
		st.RecordPump()
		tgAgg.OnPumpEvent(e)
	})
	det.WithOnCrashEvent(func(e *detector.DetectorEvent) {
		st.RecordCrash()
		tgAgg.OnCrashEvent(e)
	})

	tradeRepo := trade.NewRepo(pool, log.With(zap.String("component", "trade-repo")))

	restClients := map[string]exchange.RestClient{
		"binance": binanceRest,
	}

	tradeSvc = trade.NewService(
		cfg.DevMode,
		tradeRepo,
		restClients,
		log.With(zap.String("component", "order-manager")),
	)

	// --- Redis + персистентность открытых позиций ---
	redisCfg := redisclient.LoadConfig()
	rdb, err := redisclient.New(ctx, redisCfg, log.With(zap.String("component", "redis")))
	if err != nil {
		log.Fatal("redis connect failed", zap.Error(err))
	}
	tradeRedisRepo := trade.NewTradeRedisRepository(rdb, log.With(zap.String("component", "trade-redis")))
	tradeSvc.WithRedisRepo(tradeRedisRepo)

	// Восстанавливаем и закрываем все позиции, оставшиеся с предыдущего запуска
	tradesThreadID := sharedconfig.GetEnvInt("TELEGRAM_TRADES_THREAD_ID", 0)
	recoveredTrades := recoverTrades(ctx, tradeRedisRepo, restClients, tradeRepo, log.With(zap.String("component", "recover-trades")))
	if len(recoveredTrades) > 0 {
		log.Warn("recovered and closed positions from previous run", zap.Int("count", len(recoveredTrades)))
		sendRecoveryNotification(ctx, tgNotifier, tradesThreadID, recoveredTrades)
	}
	tradeNotif := newTradeNotifier(ctx, tgNotifier, log.With(zap.String("component", "trade-notifier")), tradesThreadID)
	tradeSvc.WithOnTradeOpen(tradeNotif.OnTradeOpen)
	tradeSvc.WithOnTradeClose(tradeNotif.OnTradeClose)
	tradeSvc.WithOnTradeCloseError(tradeNotif.OnTradeCloseError)
	// При закрытии позиции сразу обновляем стримы: если символ выпал из топа
	// пока сделка была открыта — он отпишется немедленно, не ждя следующего обновления топа.
	tradeSvc.WithOnTradeClose(func(_ *trade.Trade) { notifyExchanges() })

	// --- Position Monitor ---
	posMonitorInterval := sharedconfig.GetEnvInt("POSITION_MONITOR_INTERVAL_SEC", 60)
	posMonitor := newPositionMonitor(tradeSvc, tgNotifier, tradesThreadID, posMonitorInterval, log.With(zap.String("component", "position-monitor")))
	tickerService.WithOnSend(posMonitor.OnTicker)
	go posMonitor.Start(ctx)
	log.Info("position monitor started", zap.Int("interval_sec", posMonitorInterval))

	cmp.WithOnSpreadOpenEvent(func(_ *comparator.SpreadEvent) { st.RecordSpread() })
	cmp.WithOnSpreadOpenEvent(tgAgg.OnSpreadOpenEvent)
	tickerService.WithOnSend(det.Update)
	tickerService.WithOnSend(cmp.Update)


	// --- Volatile стратегия (собственный цикл, прямой доступ к стакану) ---
	volatileCfg := volatile.LoadConfig()
	if volatileCfg.Enabled {
		volatileSvc = volatile.New(ctx, volatileCfg, tradeSvc, obSvc, log.With(zap.String("component", "volatile")))
		if tradeAgg != nil {
			volatileSvc.WithTradeAggregator(tradeAgg)
		}
		volatileSvc.SetSymbols(topProvider.Symbols())
		det.WithOnCrashEvent(volatileSvc.OnCrashEvent)
		tickerService.WithOnSend(volatileSvc.OnTicker)
		go volatileSvc.Start(ctx)
		log.Info("volatile strategy enabled",
			zap.String("exchange", volatileCfg.Exchange),
			zap.Float64("bull_score_min", volatileCfg.BullScoreMin),
			zap.Int("check_interval_sec", volatileCfg.CheckIntervalSec),
		)
	} else {
		log.Info("volatile strategy disabled (VOLATILE_ENABLED=false)")
	}

	// --- Momentum стратегия ---
	momentumCfg := momentum.LoadConfig()
	if momentumCfg.Enabled {
		momentumSvc := momentum.New(ctx, momentumCfg, tradeSvc, log.With(zap.String("component", "momentum")))
		det.WithOnPumpEvent(momentumSvc.OnPumpEvent)
		det.WithOnCrashEvent(momentumSvc.OnCrashEvent)
		tickerService.WithOnSend(momentumSvc.OnTicker)
		log.Info("momentum strategy enabled",
			zap.String("signal_exchange", momentumCfg.SignalExchange),
		)
	} else {
		log.Info("momentum strategy disabled (MOMENTUM_ENABLED=false)")
	}

	// --- Grid стратегия ---
	gridCfg := grid.LoadConfig()
	if gridCfg.Enabled {
		gridClient, ok := restClients[gridCfg.Exchange]
		if !ok {
			log.Fatal("grid: unknown exchange in GRID_EXCHANGE",
				zap.String("exchange", gridCfg.Exchange))
		}
		gridSvc := grid.NewService(gridCfg, topProvider.Symbols(), gridClient, log.With(zap.String("component", "grid")), tgNotifier, tradesThreadID)
		gridSvc.WithRepository(grid.NewGridRepository(pool))
		gridSvc.Start(ctx, topProvider.Symbols())
		tickerService.WithOnSend(gridSvc.OnTicker)
		log.Info("grid strategy enabled", zap.Strings("symbols", gridCfg.Symbols))
	} else {
		log.Info("grid strategy disabled (GRID_ENABLED=false)")
	}

	// --- RSS News ---
	newsEnabled := sharedconfig.GetEnvBool("NEWS_ENABLED", false)
	if newsEnabled {
		newsRepo := news.NewRepository(pool, log.With(zap.String("component", "news")))
		newsSvc := news.NewService(newsRepo, log.With(zap.String("component", "news")), sharedconfig.GetEnvInt("NEWS_FETCH_INTERVAL_MIN", 30))
		newsThreadID := sharedconfig.GetEnvInt("TELEGRAM_NEWS_THREAD_ID", 0)
		newsSvc.WithTelegramNotifier(tgNotifier, newsThreadID)
		ollamaClient := ollama.NewClient(ollama.LoadConfig())
		newsSvc.WithSummarizer(ollamaClient)
		log.Info("news: Ollama summarizer enabled",
			zap.String("url", ollama.LoadConfig().URL),
			zap.String("model", ollama.LoadConfig().Model),
		)
		go newsSvc.Start(ctx)
		log.Info("news: RSS parser enabled", zap.Int("news_thread_id", newsThreadID))
	} else {
		log.Info("news: RSS parser disabled (NEWS_ENABLED=false)")
	}

	// --- Volume Spike детектор ---
	volDetector := detector.NewVolumeDetector(ctx, detectorRepo, log.With(zap.String("component", "volume-detector")))
	volDetector.WithOnVolumeSpike(func(e detector.VolumeEvent) {
		if e.Type == detector.SpikeTypeC {
			// тип C — памп, обрабатывается momentum стратегией
			return
		}
		st.RecordVolumeSpike()
		tgAgg.OnVolumeSpike(e)
	})
	tickerService.WithOnSend(volDetector.OnTicker)


	// --- Запуск бирж ---
	// Клиенты создаются здесь, после tickerService, и назначаются в переменные объявленные выше.
	// Это позволяет хуку topProvider.OnSymbolsChanged вызывать NotifySymbolsChanged на них.
	topVolatileFetcher := topProvider.FetchSymbols

	binanceCfg := binance.LoadConfig()
	log.Info("binance config loaded", zap.Bool("enabled", binanceCfg.Enabled))
	if !binanceCfg.Enabled {
		log.Warn("binance disabled, bot will do nothing")
		<-ctx.Done()
		return
	}
	log.Info("starting binance")
	binanceTickerClient = binance.NewClient(binanceCfg, log.With(zap.String("market", "binance")), st, tickerService)
	binanceTickerClient.WithSymbolFetcher(topVolatileFetcher)
	if err := binanceTickerClient.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("binance client stopped", zap.Error(err))
	}
}


func sendTopVolatile(ctx context.Context, log *zap.Logger, tickers []binance.VolatileTicker, tg *telegram.Notifier, obSvc *orderbook.Service) {

	symbols := make([]string, len(tickers))
	for i, t := range tickers {
		symbols[i] = t.Symbol
	}

	// Запускаем diff-стримы: REST-снимок 5000 уровней + WS дельты в фоне
	obSvc.Init(ctx, symbols)

	// Ждём инициализации стаканов (REST-запросы выполняются в фоне)
	// Небольшая пауза чтобы первые снапшоты успели загрузиться
	select {
	case <-ctx.Done():
		return
	case <-time.After(3 * time.Second):
	}

	newsThreadID := sharedconfig.GetEnvInt("TELEGRAM_NEWS_THREAD_ID", 0)

	msg := "🔥 <b>Топ-15 волатильных криптовалют (Binance, 24ч)</b>\n\n"
	for i, t := range tickers {
		arrow := "📈"
		if t.PriceChangePercent < 0 {
			arrow = "📉"
		}
		sign := "+"
		if t.PriceChangePercent < 0 {
			sign = ""
		}
		msg += fmt.Sprintf("%s <b>%s</b>  %s%.2f%%  <code>$%s</code>\n",
			arrow, t.Symbol, sign, t.PriceChangePercent, formatPrice(t.LastPrice))

		ob, ok := obSvc.GetBook(t.Symbol)
		if !ok {
			log.Warn("orderbook unavailable", zap.String("symbol", t.Symbol))
		} else {
			msg += formatOrderBook(ob)
		}

		if i < len(tickers)-1 {
			msg += "\n"
		}
	}

	tg.SendToThread(ctx, msg, newsThreadID)
	log.Info("top volatile sent to telegram")
}

func formatOrderBook(ob *orderbook.OrderBook) string {
	bid, ask := "—", "—"
	var bidPrice, askPrice float64

	if len(ob.Bids) > 0 {
		bid = ob.Bids[0].Price
		bidPrice, _ = strconv.ParseFloat(bid, 64)
	}
	if len(ob.Asks) > 0 {
		ask = ob.Asks[0].Price
		askPrice, _ = strconv.ParseFloat(ask, 64)
	}

	spreadStr := "—"
	if bidPrice > 0 && askPrice > 0 {
		spreadPct := (askPrice - bidPrice) / bidPrice * 100
		spreadStr = fmt.Sprintf("%.3f%%", spreadPct)
	}

	var buyVol, sellVol float64
	for _, e := range ob.Bids {
		p, _ := strconv.ParseFloat(e.Price, 64)
		q, _ := strconv.ParseFloat(e.Qty, 64)
		buyVol += p * q
	}
	for _, e := range ob.Asks {
		p, _ := strconv.ParseFloat(e.Price, 64)
		q, _ := strconv.ParseFloat(e.Qty, 64)
		sellVol += p * q
	}

	return fmt.Sprintf("  Покупка %s / Продажа %s | Спред %s\n  Покупают: $%s / Продают: $%s\n",
		bid, ask, spreadStr, formatVolume(buyVol), formatVolume(sellVol))
}

func formatVolume(v float64) string {
	switch {
	case v >= 1_000_000:
		return fmt.Sprintf("%.2fM", v/1_000_000)
	case v >= 1_000:
		return fmt.Sprintf("%.1fK", v/1_000)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}


func initTg(ctx context.Context, log *zap.Logger) (*telegram.Notifier, *telegram.Aggregator) {
	tg := telegram.New(
		sharedconfig.GetEnv("TELEGRAM_BOT_TOKEN", ""),
		sharedconfig.GetEnv("TELEGRAM_CHAT_ID", ""),
		log.With(zap.String("component", "telegram")),
	)
	agg := telegram.NewAggregator(
		ctx,
		tg,
		sharedconfig.GetEnvInt("TELEGRAM_AGGREGATE_SEC", 30),
		sharedconfig.GetEnvInt("TELEGRAM_NEWS_THREAD_ID", 0),
		log.With(zap.String("component", "telegram")),
	)
	return tg, agg
}
