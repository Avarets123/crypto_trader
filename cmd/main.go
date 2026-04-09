package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/binance"
	"github.com/osman/bot-traider/internal/bybit"
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
	st := stats.New(ctx, log)

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

	tradeNotif := newTradeNotifier(ctx, tgNotifier, log.With(zap.String("component", "trade-notifier")))
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
		go newsSvc.Start(ctx)
		log.Info("news: RSS parser enabled")
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
