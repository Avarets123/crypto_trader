package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/binance"
	"github.com/osman/bot-traider/internal/gateio"
	"github.com/osman/bot-traider/internal/shared/logger"
)

func main() {
	cfg := binance.LoadConfig()
	log := logger.New(cfg.LogLevel)
	defer log.Sync() //nolint:errcheck

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		log.Info("shutting down")
		cancel()
	}()

	gateCfg := gateio.LoadConfig()
	gateClient := gateio.NewClient(gateCfg, log.With(zap.String("market", "gateio")))
	go func() {
		if err := gateClient.Run(ctx); err != nil {
			log.Error("gateio client stopped", zap.Error(err))
		}
	}()

	binanceClient := binance.NewClient(cfg, log.With(zap.String("market", "binance")))
	if err := binanceClient.Run(ctx); err != nil {
		log.Error("binance client stopped", zap.Error(err))
		os.Exit(1)
	}
}
