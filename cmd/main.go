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
	"github.com/osman/bot-traider/internal/okx"
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

	binanceRest := binance.NewRestClient(log.With(zap.String("component", "binance-rest")))
	bybitRest := bybit.NewRestClient(log.With(zap.String("component", "bybit-rest")))
	log.Info("checking exchange api keys...")

	binanceInfo, err := binanceRest.GetAccountInfo(ctx)
	if err != nil {
		log.Fatal("binance api key invalid", zap.Error(err))
	}
	log.Info("binance api key valid",
		zap.String("account_type", binanceInfo.AccountType),
		zap.Int("balances_count", len(binanceInfo.Balances)),
	)

	bybitInfo, err := bybitRest.GetAccountInfo(ctx)
	if err != nil {
		log.Fatal("bybit api key invalid", zap.Error(err))
	}
	log.Info("bybit api key valid",
		zap.String("account_type", bybitInfo.AccountType),
		zap.Int("balances_count", len(bybitInfo.Balances)),
	)

	// --- Торговые клиенты (ws или rest) ---
	binanceTradeMode := sharedconfig.GetEnv("BINANCE_TRADE_MODE", "ws")
	log.Info("binance trade mode", zap.String("mode", binanceTradeMode))
	var binanceTradeClient exchange.RestClient
	if binanceTradeMode == "ws" {
		binanceWsTrade := binance.NewWsTradeClient(log.With(zap.String("component", "binance-ws-trade")))
		go func() {
			log.Info("binance ws trade client started")
			binanceWsTrade.Run(ctx)
			log.Error("binance ws trade client stopped")
		}()
		binanceTradeClient = binanceWsTrade
	} else {
		log.Info("binance trading via REST")
		binanceTradeClient = binanceRest
	}

	bybitTradeMode := sharedconfig.GetEnv("BYBIT_TRADE_MODE", "ws")
	log.Info("bybit trade mode", zap.String("mode", bybitTradeMode))
	var bybitTradeClient exchange.RestClient
	if bybitTradeMode == "ws" {
		bybitWsTrade := bybit.NewWsTradeClient(log.With(zap.String("component", "bybit-ws-trade")))
		go func() {
			log.Info("bybit ws trade client started")
			bybitWsTrade.Run(ctx)
			log.Error("bybit ws trade client stopped")
		}()
		bybitTradeClient = bybitWsTrade
	} else {
		log.Info("bybit trading via REST")
		bybitTradeClient = bybitRest
	}

	tgNotifier, tgAgg := initTg(ctx, log)
	st := stats.New(ctx, log)

	// --- Ticker сервис ---
	repo := ticker.NewRepository(pool, log)
	tickerService := ticker.NewService(ctx, repo, log, ticker.LoadConfig())
	log.Info("storage service started")

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
		"binance": binanceTradeClient,
		"bybit":   bybitTradeClient,
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

	// --- Lead-Lag Arb Executor ---
	arbCfg := arbitration.LoadConfig()
	arbSvc := arbitration.New(ctx, arbCfg, tradeSvc,  log.With(zap.String("component", "arb-executor")))

	cmp.WithOnSpreadOpenEvent(arbSvc.OnSpreadOpen)
	cmp.WithOnSpreadOpenEvent(func(_ *comparator.SpreadEvent) { st.RecordSpread() })
	cmp.WithOnSpreadOpenEvent(tgAgg.OnSpreadOpenEvent)

	tickerService.WithOnSend(arbSvc.OnTicker)
	tickerService.WithOnSend(det.Update)
	tickerService.WithOnSend(cmp.Update)

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

	okxCfg := okx.LoadConfig()
	log.Info("okx config loaded", zap.Bool("enabled", okxCfg.Enabled))
	if okxCfg.Enabled {
		log.Info("starting okx")
		okxClient := okx.NewClient(okxCfg, log.With(zap.String("market", "okx")), st, tickerService)
		go func() {
			log.Info("okx goroutine started")
			if err := okxClient.Run(ctx); err != nil {
				log.Error("okx client stopped", zap.Error(err))
			}
		}()
	} else {
		log.Info("okx disabled, skipping")
	}

	binanceCfg := binance.LoadConfig()
	log.Info("binance config loaded", zap.Bool("enabled", binanceCfg.Enabled))
	if !binanceCfg.Enabled {
		log.Info("binance disabled, skipping")
		if !bybitCfg.Enabled && !okxCfg.Enabled {
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
