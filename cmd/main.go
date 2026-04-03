package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/binance"
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

	client := binance.NewClient(cfg, log)
	if err := client.Run(ctx); err != nil {
		log.Error("client stopped with error", zap.Error(err))
		os.Exit(1)
	}
}
