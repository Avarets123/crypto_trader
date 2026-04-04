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
	"github.com/osman/bot-traider/internal/gateio"
	"github.com/osman/bot-traider/internal/okx"
	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
	"github.com/osman/bot-traider/internal/shared/comparator"
	"github.com/osman/bot-traider/internal/shared/db"
	"github.com/osman/bot-traider/internal/shared/detector"
	"github.com/osman/bot-traider/internal/shared/logger"
	"github.com/osman/bot-traider/internal/shared/stats"
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

	st := stats.New(ctx, log)

	repo := ticker.NewRepository(pool, log)
	tickerService := ticker.NewService(ctx, repo, log, ticker.LoadConfig())
	log.Info("storage service started")

	spreadRepo := comparator.NewSpreadRepository(pool, log.With(zap.String("component", "comparator")))
	cmp := comparator.New(ctx, cfg.SpreadThresholdPct, spreadRepo, log.With(zap.String("component", "comparator")))
	cmp.WithOnSpreadOpen(st.RecordSpread)
	tickerService.WithOnSend(cmp.Update)

	detectorRepo := detector.NewDetectorRepository(pool, log.With(zap.String("component", "detector")))
	det := detector.New(ctx, detector.LoadConfig(), detectorRepo, log.With(zap.String("component", "detector")))
	det.WithOnPump(st.RecordPump)
	det.WithOnCrash(st.RecordCrash)
	tickerService.WithOnSend(det.Update)

	bybitClient := bybit.NewClient(bybit.LoadConfig(), log.With(zap.String("market", "bybit")), st, tickerService)
	go func() {
		if err := bybitClient.Run(ctx); err != nil {
			log.Error("bybit client stopped", zap.Error(err))
		}
	}()

	gateClient := gateio.NewClient(gateio.LoadConfig(), log.With(zap.String("market", "gateio")), st, tickerService)
	go func() {
		if err := gateClient.Run(ctx); err != nil {
			log.Error("gateio client stopped", zap.Error(err))
		}
	}()

	okxClient := okx.NewClient(okx.LoadConfig(), log.With(zap.String("market", "okx")), st, tickerService)
	go func() {
		if err := okxClient.Run(ctx); err != nil {
			log.Error("okx client stopped", zap.Error(err))
		}
	}()

	binanceCfg := binance.LoadConfig()
	binanceClient := binance.NewClient(binanceCfg, log.With(zap.String("market", "binance")), st, tickerService)
	if err := binanceClient.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("binance client stopped", zap.Error(err))
	}
}
