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
	"github.com/osman/bot-traider/internal/bybit"
	exchange_orders "github.com/osman/bot-traider/internal/exchange_orders"
	"github.com/osman/bot-traider/internal/orderbook"
	"github.com/osman/bot-traider/internal/shared/comparator"
	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
	"github.com/osman/bot-traider/internal/shared/db"
	"github.com/osman/bot-traider/internal/shared/detector"
	"github.com/osman/bot-traider/internal/shared/exchange"
	"github.com/osman/bot-traider/internal/shared/logger"
	"github.com/osman/bot-traider/internal/shared/stats"
	"github.com/osman/bot-traider/internal/shared/telegram"
	"github.com/osman/bot-traider/internal/ticker"
	"github.com/osman/bot-traider/internal/trade"
	"github.com/osman/bot-traider/internal/trade_strategies/arbitration"
	"github.com/osman/bot-traider/internal/news"
	"github.com/osman/bot-traider/internal/ollama"
	"github.com/osman/bot-traider/internal/trade_strategies/grid"
	"github.com/osman/bot-traider/internal/trade_strategies/momentum"
	"github.com/osman/bot-traider/internal/trade_strategies/scalping"
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

	binanceRest := binance.NewRestClient(ctx,log.With(zap.String("component", "binance-rest")))
	bybitRest := bybit.NewRestClient(ctx,log.With(zap.String("component", "bybit-rest")))



	tgNotifier, tgAgg := initTg(ctx, log)

	// --- Order Book ---
	obStore := orderbook.NewStore()
	obFetcher := orderbook.NewFetcher(
		binanceRest,
		sharedconfig.GetEnv("BINANCE_WS_URL", "wss://stream.binance.com:9443"),
		log.With(zap.String("component", "orderbook")),
	)
	obSvc := orderbook.NewService(obFetcher, obStore, log.With(zap.String("component", "orderbook")))

	go sendTopVolatile(ctx, log, binanceRest, tgNotifier, obSvc)

	st := stats.New(ctx, log)

	// --- Exchange Orders ---
	if sharedconfig.GetEnvBool("EXCHANGE_ORDERS_ENABLED", false) {
		eoRepo := exchange_orders.NewRepository(pool, log.With(zap.String("component", "exchange-orders")))
		eoRepo.WithOnSaved(st.RecordExchangeOrders)
		eoFetcher := exchange_orders.NewFetcher(
			"binance",
			sharedconfig.GetEnv("BINANCE_WS_URL", "wss://stream.binance.com:9443"),
			eoRepo,
			log.With(zap.String("component", "exchange-orders")),
		)
		eoSvc := exchange_orders.NewService(eoFetcher, log.With(zap.String("component", "exchange-orders")))
		tickers, err := binanceRest.GetTopVolatile(ctx, 10)
		if err != nil {
			log.Error("exchange_orders: failed to get top volatile", zap.Error(err))
		} else {
			symbols := make([]string, len(tickers))
			for i, t := range tickers {
				symbols[i] = t.Symbol
			}
			eoSvc.Start(ctx, symbols)
		}
		log.Info("exchange_orders: enabled")
	} else {
		log.Info("exchange_orders: disabled (EXCHANGE_ORDERS_ENABLED=false)")
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
		"bybit":   bybitRest,
	}

	tradeSvc := trade.NewService(
		cfg.DevMode,
		tradeRepo,
		restClients,
		log.With(zap.String("component", "order-manager")),
	)

	tradesThreadID := sharedconfig.GetEnvInt("TELEGRAM_TRADES_THREAD_ID", 0)
	tradeNotif := newTradeNotifier(ctx, tgNotifier, log.With(zap.String("component", "trade-notifier")), tradesThreadID)
	tradeSvc.WithOnTradeOpen(tradeNotif.OnTradeOpen)
	tradeSvc.WithOnTradeClose(tradeNotif.OnTradeClose)
	tradeSvc.WithOnTradeCloseError(tradeNotif.OnTradeCloseError)

	// --- Lead-Lag Arb Executor ---
	arbCfg := arbitration.LoadConfig()
	if arbCfg.Enabled {
		arbSvc := arbitration.New(ctx, arbCfg, tradeSvc, log.With(zap.String("component", "arb-executor")))
		cmp.WithOnSpreadOpenEvent(arbSvc.OnSpreadOpen)
		cmp.WithOnSpreadCloseEvent(arbSvc.OnSpreadClose)
		tickerService.WithOnSend(arbSvc.OnTicker)
		log.Info("arbitration strategy enabled",
			zap.Float64("min_spread_pct", arbCfg.MinSpreadPct),
		)
	} else {
		log.Info("arbitration strategy disabled (ARB_ENABLED=false)")
	}

	cmp.WithOnSpreadOpenEvent(func(_ *comparator.SpreadEvent) { st.RecordSpread() })
	cmp.WithOnSpreadOpenEvent(tgAgg.OnSpreadOpenEvent)
	tickerService.WithOnSend(det.Update)
	tickerService.WithOnSend(cmp.Update)


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



	// --- Scalping стратегия ---
	scalpCfg := scalping.LoadConfig()
	if scalpCfg.Enabled {
		scalpClient, ok := restClients[scalpCfg.Exchange]
		if !ok {
			log.Fatal("scalping: unknown exchange in SCALPING_EXCHANGE",
				zap.String("exchange", scalpCfg.Exchange))
		}
		scalpSvc := scalping.New(ctx, scalpCfg, tradeSvc, scalpClient, log.With(zap.String("component", "scalping")))
		tickerService.WithOnSend(scalpSvc.OnTicker)
		if err := scalpSvc.Start(ctx); err != nil {
			log.Fatal("scalping start failed", zap.Error(err))
		}
		log.Info("scalping strategy enabled", zap.Strings("symbols", scalpCfg.Symbols))
	} else {
		log.Info("scalping strategy disabled (SCALPING_ENABLED=false)")
	}

	// --- Grid стратегия ---
	gridCfg := grid.LoadConfig()
	if gridCfg.Enabled {
		gridClient, ok := restClients[gridCfg.Exchange]
		if !ok {
			log.Fatal("grid: unknown exchange in GRID_EXCHANGE",
				zap.String("exchange", gridCfg.Exchange))
		}
		gridSvc := grid.NewService(gridCfg, gridClient, log.With(zap.String("component", "grid")), tgNotifier)
		gridSvc.WithRepository(grid.NewGridRepository(pool))
		gridSvc.Start(ctx)
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

	watchExchanges(ctx, log, st, tickerService)
}

func watchExchanges(ctx context.Context, log *zap.Logger, st *stats.Stats, tickerService *ticker.TickerService) {
	bybitCfg := bybit.LoadConfig()
	log.Info("bybit config loaded", zap.Bool("enabled", bybitCfg.Enabled))
	if bybitCfg.Enabled {
		log.Info("starting bybit")
		bybitClient := bybit.NewClient(bybitCfg, log.With(zap.String("market", "bybit")), st, tickerService)
		go func() {
			log.Info("bybit goroutine started")
			if err := bybitClient.Run(ctx); err != nil {
				log.Error("bybit client stopped", zap.Error(err))
			}
		}()
	} else {
		log.Info("bybit disabled, skipping")
	}

	binanceCfg := binance.LoadConfig()
	log.Info("binance config loaded", zap.Bool("enabled", binanceCfg.Enabled))
	if !binanceCfg.Enabled {
		log.Info("binance disabled, skipping")
		if !bybitCfg.Enabled {
			log.Warn("no exchanges enabled, bot will do nothing")
		}
		<-ctx.Done()
		return
	}
	log.Info("starting binance")
	binanceClient := binance.NewClient(binanceCfg, log.With(zap.String("market", "binance")), st, tickerService)
	if err := binanceClient.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("binance client stopped", zap.Error(err))
	}
}


func sendTopVolatile(ctx context.Context, log *zap.Logger, rest *binance.RestClient, tg *telegram.Notifier, obSvc *orderbook.Service) {
	tickers, err := rest.GetTopVolatile(ctx, 10)
	if err != nil {
		log.Error("failed to get top volatile", zap.Error(err))
		return
	}

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

	msg := "🔥 <b>Топ-10 волатильных криптовалют (Binance, 24ч)</b>\n\n"
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
		log.With(zap.String("component", "telegram")),
	)
	return tg, agg
}
