package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/binance"
	"github.com/osman/bot-traider/internal/bybit"
	"github.com/osman/bot-traider/internal/gateio"
	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
	"github.com/osman/bot-traider/internal/shared/logger"
	"github.com/osman/bot-traider/internal/shared/stats"
)

func main() {
	base := sharedconfig.LoadBase()
	log := logger.New(base.LogLevel)
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

	st := stats.New()
	st.LogPeriodically(ctx, time.Second*5, log)

	bybitClient := bybit.NewClient(bybit.LoadConfig(), log.With(zap.String("market", "bybit")), st)
	go func() {
		if err := bybitClient.Run(ctx); err != nil {
			log.Error("bybit client stopped", zap.Error(err))
		}
	}()

	gateClient := gateio.NewClient(gateio.LoadConfig(), log.With(zap.String("market", "gateio")), st)
	go func() {
		if err := gateClient.Run(ctx); err != nil {
			log.Error("gateio client stopped", zap.Error(err))
		}
	}()

	binanceCfg := binance.LoadConfig()
	binanceClient := binance.NewClient(binanceCfg, log.With(zap.String("market", "binance")), st)
	if err := binanceClient.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("binance client stopped", zap.Error(err))
	}
}
