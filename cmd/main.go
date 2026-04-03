package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/binance"
	"github.com/osman/bot-traider/internal/bybit"
	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
	"github.com/osman/bot-traider/internal/shared/db"
	"github.com/osman/bot-traider/internal/shared/logger"
	"github.com/osman/bot-traider/internal/shared/stats"
	"github.com/osman/bot-traider/internal/ticker"
)

func main() {
	cfg := sharedconfig.LoadBase()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log := logger.New(cfg.LogLevel)
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


	bybitClient := bybit.NewClient(bybit.LoadConfig(), log.With(zap.String("market", "bybit")), st, tickerService)
	go func() {
		if err := bybitClient.Run(ctx); err != nil {
			log.Error("bybit client stopped", zap.Error(err))
		}
	}()

	// gateClient := gateio.NewClient(gateio.LoadConfig(), log.With(zap.String("market", "gateio")), st, svc)
	// go func() {
	// 	if err := gateClient.Run(ctx); err != nil {
	// 		log.Error("gateio client stopped", zap.Error(err))
	// 	}
	// }()

	binanceCfg := binance.LoadConfig()
	binanceClient := binance.NewClient(binanceCfg, log.With(zap.String("market", "binance")), st, tickerService)
	if err := binanceClient.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("binance client stopped", zap.Error(err))
	}
}
