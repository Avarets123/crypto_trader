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
	"github.com/osman/bot-traider/internal/binance"
	exchange_orders "github.com/osman/bot-traider/internal/exchange_orders"
	"github.com/osman/bot-traider/internal/kucoin"
	"github.com/osman/bot-traider/internal/news"
	"github.com/osman/bot-traider/internal/notifications"
	"github.com/osman/bot-traider/internal/orderbook"
	sharedconfig "github.com/osman/bot-traider/internal/shared/config"
	"github.com/osman/bot-traider/internal/shared/db"
	"github.com/osman/bot-traider/internal/shared/exchange"
	"github.com/osman/bot-traider/internal/shared/logger"
	redisclient "github.com/osman/bot-traider/internal/shared/redis"
	"github.com/osman/bot-traider/internal/shared/stats"
	"github.com/osman/bot-traider/internal/shared/telegram"
	"github.com/osman/bot-traider/internal/ticker"
	"github.com/osman/bot-traider/internal/trade"
	"github.com/osman/bot-traider/internal/trade_strategies/grid"
	"github.com/osman/bot-traider/internal/trade_strategies/microscalping"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
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
	tgNotifier, _ := initTg(ctx, log)

	// --- Order Book ---
	obStore := orderbook.NewStore()
	obFetcher := orderbook.NewFetcher(
		binanceRest,
		sharedconfig.GetEnv("BINANCE_WS_URL", "wss://stream.binance.com:9443"),
		log.With(zap.String("component", "orderbook")),
	)
	obSvc := orderbook.NewService(obFetcher, obStore, log.With(zap.String("component", "orderbook")))

	// --- Top Volatile Provider — единственный источник топ-10 монет ---
	// Интервал обновления берётся из ORDERBOOK_REFRESH_INTERVAL_MIN (общий с orderbook-alerts).
	alertCfg := orderbook.LoadAlertsConfig()
	topProvider := binance.NewTopVolatileProvider(binanceRest, 10, log.With(zap.String("component", "top-volatile")))
	if err := topProvider.Fetch(ctx); err != nil {
		log.Fatal("top volatile: initial fetch failed", zap.Error(err))
	}

	go sendTopVolatileBinance(ctx, log, []binance.VolatileTicker{binance.VolatileTicker{
		Symbol: "BTCUSDT",
	}, binance.VolatileTicker{
		Symbol: "ETHUSDT",
	}}, tgNotifier, obSvc)

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
		alertSvc.SetSymbols([]string{"BTCUSDT", "ETHUSDT"})
	}

	st := stats.New(ctx, log)

	// --- Exchange Orders ---
	// tradeAgg и eoFetcher объявлены здесь, wiring trade-хуков выполняется после создания volatile.
	var tradeAgg *exchange_orders.TradeAggregator
	var eoSvc *exchange_orders.Service
	var eoFetcher *exchange_orders.Fetcher
	if sharedconfig.GetEnvBool("EXCHANGE_ORDERS_ENABLED", false) {
		eoRepo := exchange_orders.NewRepository(pool, log.With(zap.String("component", "exchange-orders")))
		eoRepo.WithOnSaved(st.RecordExchangeOrders)
		eoFetcher = exchange_orders.NewFetcher(
			"binance",
			sharedconfig.GetEnv("BINANCE_WS_URL", "wss://stream.binance.com:9443"),
			eoRepo,
			log.With(zap.String("component", "exchange-orders")),
		)
		tradeAgg = exchange_orders.NewTradeAggregator()
		if alertSvc != nil {
			alertSvc.WithTradeAggregator(tradeAgg)
		}
		eoSvc = exchange_orders.NewService(eoFetcher, log.With(zap.String("component", "exchange-orders")))
		eoSvc.Start(ctx, []string{"BTCUSDT", "ETHUSDT"})
		log.Info("exchange_orders: enabled")
	} else {
		log.Info("exchange_orders: disabled (EXCHANGE_ORDERS_ENABLED=false)")
	}
		// --- Ticker сервис ---
	repo := ticker.NewRepository(pool, log)
	tickerService := ticker.NewService(ctx, repo, log, ticker.LoadConfig())
	tradeRepo := trade.NewRepo(pool, log.With(zap.String("component", "trade-repo")))

	// Объявляем переменные заранее — хук захватит их по ссылке.
	// Сами сервисы создаются позже, после инициализации tickerService.
	var binanceTickerClient *binance.Client
	var kucoinTickerClient *kucoin.Client



	restClients := map[string]exchange.RestClient{
		"binance": binanceRest,
	}

		tradeSvc := trade.NewService(
		cfg.DevMode,
		tradeRepo,
		restClients,
		log.With(zap.String("component", "order-manager")),
	)


	// --- Microscalping стратегия (событийный вход по taker buy) ---
	microscalpingSvc := microscalping.New(ctx, tradeSvc, tickerService, obSvc, []string{"BTCUSDT", "ETHUSDT"}, log)

	// notifyExchanges отправляет обновлённый список символов биржевым клиентам.
	// К топ-10 добираются символы с открытыми позициями — чтобы не потерять тикеры
	// по валютам, которые выпали из топа пока по ним есть активная сделка.
	notifyExchanges := func() {
		all := trade.MergeSymbols(topProvider.Symbols(), trade.OpenTradeSymbols(tradeSvc))
		if binanceTickerClient != nil {
			binanceTickerClient.NotifySymbolsChanged(all)
		}
		if kucoinTickerClient != nil {
			kucoinTickerClient.NotifySymbolsChanged(all)
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
		if microscalpingSvc != nil {
			microscalpingSvc.OnSymbolsChanged(added, removed)
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
	recoveredTrades := notifications.RecoverTrades(ctx, tradeRedisRepo, restClients, tradeRepo, log.With(zap.String("component", "recover-trades")))
	if len(recoveredTrades) > 0 {
		log.Warn("recovered and closed positions from previous run", zap.Int("count", len(recoveredTrades)))
		notifications.SendRecoveryNotification(ctx, tgNotifier, tradesThreadID, recoveredTrades)
	}
	tradeNotif := notifications.NewTradeNotifier(ctx, tgNotifier, log.With(zap.String("component", "trade-notifier")), tradesThreadID)
	tradeSvc.WithOnTradeOpen(tradeNotif.OnTradeOpen)
	tradeSvc.WithOnTradeClose(tradeNotif.OnTradeClose)
	tradeSvc.WithOnTradeCloseError(tradeNotif.OnTradeCloseError)

	// При закрытии позиции сразу обновляем стримы: если символ выпал из топа
	// пока сделка была открыта — он отпишется немедленно, не ждя следующего обновления топа.
	//Ёп-твою-мать
	tradeSvc.WithOnTradeClose(func(_ *trade.Trade) { notifyExchanges() })

	// --- Position Monitor ---
	notifications.NewPositionMonitor(ctx, tradeSvc, tickerService, tgNotifier, tradesThreadID, log)



	// Подключаем fan-out trade-хука: tradeAgg (для alerts) + microscalping (для CVD/whale метрик).
	// Wiring после создания microscalpingSvc чтобы передать корректный указатель.
	if eoFetcher != nil {
		eoFetcher.WithOnTrade(func(o exchange_orders.ExchangeOrder) {
			if tradeAgg != nil {
				tradeAgg.OnTrade(o)
			}
			if microscalpingSvc != nil {
				microscalpingSvc.OnTrade(o)
			}
		})
	}


	gridCfg := grid.LoadConfig()
	grid.New(ctx, pool, tickerService, topProvider.Symbols(), restClients[gridCfg.Exchange], tgNotifier, tradesThreadID, log)


	// --- RSS News ---
	news.New(ctx, pool, tgNotifier, false, sharedconfig.GetEnvInt("TELEGRAM_NEWS_THREAD_ID", 0), sharedconfig.GetEnvInt("NEWS_FETCH_INTERVAL_MIN", 30), log )


	// --- Запуск бирж ---
	// Клиенты создаются здесь, после tickerService, и назначаются в переменные объявленные выше.
	// Это позволяет хуку topProvider.OnSymbolsChanged вызывать NotifySymbolsChanged на них.
	topVolatileFetcher := topProvider.FetchSymbols

	anyExchangeEnabled := false

	binanceCfg := binance.LoadConfig()
	log.Info("binance config loaded", zap.Bool("enabled", binanceCfg.Enabled))
	if binanceCfg.Enabled {
		anyExchangeEnabled = true
		log.Info("starting binance")
		binanceTickerClient = binance.NewClient(binanceCfg, log.With(zap.String("market", "binance")), st, tickerService)
		binanceTickerClient.WithSymbolFetcher(topVolatileFetcher)
		go func() {
			if err := binanceTickerClient.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("binance client stopped", zap.Error(err))
			}
		}()
	} else {
		log.Info("binance disabled (BINANCE_ENABLED=false)")
	}

	kucoinCfg := kucoin.LoadConfig()
	log.Info("kucoin config loaded", zap.Bool("enabled", kucoinCfg.Enabled))
	if kucoinCfg.Enabled {
		anyExchangeEnabled = true

		// REST клиент (для торговли, если API ключи заданы)
		kucoinRest := kucoin.NewRestClient(ctx, log.With(zap.String("component", "kucoin-rest")))
		if kucoinRest.HasAPIKeys() {
			restClients["kucoin"] = kucoinRest
			log.Info("kucoin: trading enabled")
		} else {
			log.Info("kucoin: market data only (no API keys)")
		}

		// Топ-10 волатильных KuCoin в Telegram
		go sendTopVolatileKucoin(ctx, log, kucoinRest, tgNotifier)

		// WS клиент для рыночных данных
		log.Info("starting kucoin")
		kucoinTickerClient = kucoin.NewClient(kucoinCfg, log.With(zap.String("market", "kucoin")), st, tickerService)
		kucoinTickerClient.WithSymbolFetcher(func(ctx context.Context) ([]string, error) {
			// Используем те же топ-символы что и Binance
			return topProvider.Symbols(), nil
		})
		go func() {
			if err := kucoinTickerClient.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("kucoin client stopped", zap.Error(err))
			}
		}()
	} else {
		log.Info("kucoin disabled (KUCOIN_ENABLED=false)")
	}

	if !anyExchangeEnabled {
		log.Warn("no exchanges enabled, bot will do nothing")
	}

	<-ctx.Done()
	log.Info("shutdown complete")
}


// sendTopVolatileBinance отправляет топ-10 волатильных пар Binance со стаканами в Telegram.
func sendTopVolatileBinance(ctx context.Context, log *zap.Logger, tickers []binance.VolatileTicker, tg *telegram.Notifier, obSvc *orderbook.Service) {
	symbols := make([]string, len(tickers))
	for i, t := range tickers {
		symbols[i] = t.Symbol
	}

	// Запускаем diff-стримы: REST-снимок 5000 уровней + WS дельты в фоне
	obSvc.Init(ctx, symbols)

	// Небольшая пауза чтобы первые снапшоты успели загрузиться
	select {
	case <-ctx.Done():
		return
	case <-time.After(3 * time.Second):
	}

	newsThreadID := sharedconfig.GetEnvInt("TELEGRAM_NEWS_THREAD_ID", 0)

	// Берём топ-10
	top := tickers
	if len(top) > 10 {
		top = top[:10]
	}

	msg := "🔥 <b>Топ-10 волатильных криптовалют (Binance, 24ч)</b>\n\n"
	for i, t := range top {
		arrow := "📈"
		if t.PriceChangePercent < 0 {
			arrow = "📉"
		}
		sign := "+"
		if t.PriceChangePercent < 0 {
			sign = ""
		}
		msg += fmt.Sprintf("%s <b>%s</b>  %s%.2f%%  <code>$%s</code>\n",
			arrow, t.Symbol, sign, t.PriceChangePercent, notifications.FormatPrice(t.LastPrice))

		ob, ok := obSvc.GetBook(t.Symbol)
		if !ok {
			log.Warn("orderbook unavailable", zap.String("symbol", t.Symbol))
		} else {
			msg += formatOrderBook(ob)
		}

		if i < len(top)-1 {
			msg += "\n"
		}
	}

	tg.SendToThread(ctx, msg, newsThreadID)
	log.Info("binance top volatile sent to telegram")
}

// sendTopVolatileKucoin получает и отправляет топ-10 волатильных пар KuCoin в Telegram.
func sendTopVolatileKucoin(ctx context.Context, log *zap.Logger, rest *kucoin.RestClient, tg *telegram.Notifier) {
	newsThreadID := sharedconfig.GetEnvInt("TELEGRAM_NEWS_THREAD_ID", 0)

	tickers, err := rest.GetTopVolatile(ctx, 10)
	if err != nil {
		log.Warn("kucoin: failed to get top volatile", zap.Error(err))
		return
	}

	if len(tickers) == 0 {
		log.Warn("kucoin: top volatile list is empty")
		return
	}

	msg := "🔥 <b>Топ-10 волатильных криптовалют (KuCoin, 24ч)</b>\n\n"
	for i, t := range tickers {
		arrow := "📈"
		if t.PriceChangePercent < 0 {
			arrow = "📉"
		}
		sign := "+"
		if t.PriceChangePercent < 0 {
			sign = ""
		}
		msg += fmt.Sprintf("%s <b>%s</b>  %s%.2f%%  <code>$%s</code>",
			arrow, t.Symbol, sign, t.PriceChangePercent, notifications.FormatPrice(t.LastPrice))

		if i < len(tickers)-1 {
			msg += "\n"
		}
	}

	tg.SendToThread(ctx, msg, newsThreadID)
	log.Info("kucoin top volatile sent to telegram")
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
