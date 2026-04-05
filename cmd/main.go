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
	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
	"github.com/osman/bot-traider/internal/shared/comparator"
	"github.com/osman/bot-traider/internal/shared/db"
	"github.com/osman/bot-traider/internal/shared/detector"
	"github.com/osman/bot-traider/internal/shared/logger"
	"github.com/osman/bot-traider/internal/shared/stats"
	"github.com/osman/bot-traider/internal/shared/telegram"
	"github.com/osman/bot-traider/internal/ticker"
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

	tg := telegram.New(
		sharedconfig.GetEnv("TELEGRAM_BOT_TOKEN", ""),
		sharedconfig.GetEnv("TELEGRAM_CHAT_ID", ""),
		log.With(zap.String("component", "telegram")),
	)
	tgAgg := telegram.NewAggregator(
		tg,
		sharedconfig.GetEnvInt("TELEGRAM_AGGREGATE_SEC", 30),
		log.With(zap.String("component", "telegram")),
	)

	st := stats.New(ctx, log)

	repo := ticker.NewRepository(pool, log)
	tickerService := ticker.NewService(ctx, repo, log, ticker.LoadConfig())
	log.Info("storage service started")

	spreadRepo := comparator.NewSpreadRepository(pool, log.With(zap.String("component", "comparator")))
	cmp := comparator.New(ctx, cfg.SpreadThresholdPct, spreadRepo, log.With(zap.String("component", "comparator")))
	cmp.WithOnSpreadOpen(st.RecordSpread)
	cmp.WithOnSpreadOpenEvent(func(e *comparator.SpreadEvent) {
		tgAgg.Add(ctx, telegram.Event{
			Type:      telegram.EventSpread,
			Symbol:    e.Symbol,
			Exchange:  e.ExchangeHigh,
			Exchange2: e.ExchangeLow,
			ChangePct: e.MaxSpreadPct,
		})
	})
	tickerService.WithOnSend(cmp.Update)

	detectorRepo := detector.NewDetectorRepository(pool, log.With(zap.String("component", "detector")))
	det := detector.New(ctx, detector.LoadConfig(), detectorRepo, log.With(zap.String("component", "detector")))
	det.WithOnPump(st.RecordPump)
	det.WithOnCrash(st.RecordCrash)
	det.WithOnPumpEvent(func(e *detector.DetectorEvent) {
		tgAgg.Add(ctx, telegram.Event{
			Type:      telegram.EventPump,
			Symbol:    e.Symbol,
			Exchange:  e.Exchange,
			ChangePct: e.ChangePct,
			WindowSec: e.WindowSec,
		})
	})
	det.WithOnCrashEvent(func(e *detector.DetectorEvent) {
		tgAgg.Add(ctx, telegram.Event{
			Type:      telegram.EventCrash,
			Symbol:    e.Symbol,
			Exchange:  e.Exchange,
			ChangePct: e.ChangePct,
			WindowSec: e.WindowSec,
		})
	})
	tickerService.WithOnSend(det.Update)

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
	if !bybitCfg.Enabled && !okxCfg.Enabled {
		log.Warn("no exchanges enabled except binance")
	}
	log.Info("starting binance")
	binanceClient := binance.NewClient(binanceCfg, log.With(zap.String("market", "binance")), st, tickerService)
	if err := binanceClient.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("binance client stopped", zap.Error(err))
	}
}
