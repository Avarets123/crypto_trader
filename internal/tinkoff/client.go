package tinkoff

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	investgo "github.com/russianinvestments/invest-api-go-sdk/investgo"
	pb "github.com/russianinvestments/invest-api-go-sdk/proto"
	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/exchange_orders"
	"github.com/osman/bot-traider/internal/orderbook"
	"github.com/osman/bot-traider/internal/ticker"
)

// Client — клиент Т-Инвестиции (gRPC).
// Подписывается на поток котировок (LastPrice), стаканов (OrderBook)
// и ленту сделок (Trade) для переданных инструментов.
// Список инструментов задаётся через NotifySymbolsChanged и обновляется динамически.
type Client struct {
	config *Config
	log    *zap.Logger

	tickerSvc   *ticker.TickerService
	obStore     *orderbook.Store
	onTrade     func(exchange_orders.ExchangeOrder)
	onOrderBook func(*orderbook.OrderBook)

	mu          sync.RWMutex
	instruments []VolatileTicker // текущий список инструментов
	restartCh   chan struct{}     // сигнал перезапуска стрима (буфер 1)
}

// NewClient создаёт новый Client.
func NewClient(cfg *Config, log *zap.Logger, tickerSvc *ticker.TickerService, obStore *orderbook.Store) *Client {
	return &Client{
		config:    cfg,
		log:       log,
		tickerSvc: tickerSvc,
		obStore:   obStore,
		restartCh: make(chan struct{}, 1),
	}
}

// WithOnTrade устанавливает хук, вызываемый при каждой входящей сделке.
func (c *Client) WithOnTrade(fn func(exchange_orders.ExchangeOrder)) {
	c.onTrade = fn
}

// WithOnOrderBook устанавливает хук, вызываемый при каждом обновлении стакана.
func (c *Client) WithOnOrderBook(fn func(*orderbook.OrderBook)) {
	c.onOrderBook = fn
}

// NotifySymbolsChanged обновляет список инструментов и перезапускает стрим.
// Потокобезопасен. Дублирующие вызовы без новых данных не приводят к лишним рестартам.
func (c *Client) NotifySymbolsChanged(instruments []VolatileTicker) {
	c.mu.Lock()
	c.instruments = make([]VolatileTicker, len(instruments))
	copy(c.instruments, instruments)
	c.mu.Unlock()

	// Неблокирующая отправка: если уже есть сигнал — не дублируем
	select {
	case c.restartCh <- struct{}{}:
	default:
	}
}

// Run подключается к API Т-Инвестиции и запускает поток рыночных данных.
// Блокирует до ctx.Done(). При изменении инструментов (NotifySymbolsChanged) перезапускает стрим.
func (c *Client) Run(ctx context.Context) error {
	endpoint := "invest-public-api.tinkoff.ru:443"
	if c.config.Sandbox {
		endpoint = "sandbox-invest-public-api.tinkoff.ru:443"
	}

	sdkCfg := investgo.Config{
		Token:    c.config.Token,
		EndPoint: endpoint,
		AppName:  "bot-traider",
	}

	sdkClient, err := investgo.NewClient(ctx, sdkCfg, &zapLogger{c.log})
	if err != nil {
		return fmt.Errorf("tinkoff: create sdk client: %w", err)
	}
	defer sdkClient.Stop() //nolint:errcheck

	mdStreamClient := sdkClient.NewMarketDataStreamClient()

	for {
		// Ждём сигнала с новым списком инструментов
		select {
		case <-ctx.Done():
			return nil
		case <-c.restartCh:
		}

		c.mu.RLock()
		instruments := make([]VolatileTicker, len(c.instruments))
		copy(instruments, c.instruments)
		c.mu.RUnlock()

		if len(instruments) == 0 {
			c.log.Warn("tinkoff: received empty instruments list, waiting for next update")
			continue
		}

		c.log.Info("tinkoff: starting stream",
			zap.Int("instruments", len(instruments)),
			zap.Strings("symbols", symbolsOf(instruments)),
		)

		// Запускаем стрим с текущими инструментами до изменения списка или ctx.Done()
		c.runWithInstruments(ctx, mdStreamClient, instruments)
	}
}

// runWithInstruments запускает стрим с заданными инструментами.
// Возвращает когда: ctx отменён, пришёл сигнал рестарта (новые инструменты) или ошибка.
func (c *Client) runWithInstruments(ctx context.Context, mdClient *investgo.MarketDataStreamClient, instruments []VolatileTicker) {
	wait := time.Second
	for {
		if err := c.runStream(ctx, mdClient, instruments); err != nil {
			if ctx.Err() != nil {
				return
			}
			c.log.Warn("tinkoff: stream error, reconnecting",
				zap.Error(err),
				zap.Duration("wait", wait),
			)
		}

		select {
		case <-ctx.Done():
			return
		case <-c.restartCh:
			// Получили новые инструменты — выходим, внешний цикл перезапустится
			return
		case <-time.After(wait):
		}
		wait = nextWait(wait, 60*time.Second)
	}
}

// runStream открывает единый MarketDataStream, подписывается на данные и слушает до ошибки.
func (c *Client) runStream(ctx context.Context, mdClient *investgo.MarketDataStreamClient, instruments []VolatileTicker) error {
	// Строим локальные карты uid → symbol/currency для этой сессии
	uidToSymbol := make(map[string]string, len(instruments))
	uidToCurrency := make(map[string]string, len(instruments))
	uids := make([]string, 0, len(instruments))
	for _, inst := range instruments {
		uidToSymbol[inst.UID] = inst.Symbol
		uidToCurrency[inst.UID] = inst.Currency
		uids = append(uids, inst.UID)
	}

	stream, err := mdClient.MarketDataStream()
	if err != nil {
		return fmt.Errorf("create stream: %w", err)
	}
	defer stream.Stop()

	c.log.Info("tinkoff: stream connected", zap.Int("instruments", len(uids)))

	// Подписываемся на последние цены (аналог тикера)
	lpCh, err := stream.SubscribeLastPrice(uids)
	if err != nil {
		return fmt.Errorf("subscribe last price: %w", err)
	}

	// Подписываемся на стаканы
	obCh, err := stream.SubscribeOrderBook(uids, c.config.OrderBookDepth)
	if err != nil {
		return fmt.Errorf("subscribe order book: %w", err)
	}

	// Подписываемся на ленту обезличенных сделок
	tradeCh, err := stream.SubscribeTrade(uids, pb.TradeSourceType_TRADE_SOURCE_ALL, false)
	if err != nil {
		return fmt.Errorf("subscribe trades: %w", err)
	}

	// Запускаем потребителей — каждый получает свои read-only карты
	go c.consumeLastPrices(ctx, lpCh, uidToSymbol, uidToCurrency)
	go c.consumeOrderBooks(ctx, obCh, uidToSymbol)
	go c.consumeTrades(ctx, tradeCh, uidToSymbol)

	// Listen блокирует и читает из gRPC стрима, раскладывая данные по каналам
	return stream.Listen()
}

// consumeLastPrices читает обновления последней цены и отправляет в TickerService.
func (c *Client) consumeLastPrices(ctx context.Context, ch <-chan *pb.LastPrice, uidToSymbol, uidToCurrency map[string]string) {
	for {
		select {
		case <-ctx.Done():
			return
		case lp, ok := <-ch:
			if !ok {
				return
			}
			uid := lp.GetInstrumentUid()
			if uid == "" {
				uid = lp.GetFigi()
			}
			sym, exists := uidToSymbol[uid]
			if !exists {
				continue
			}
			price := quotationToFloat(lp.GetPrice())
			if price == 0 {
				continue
			}
			t := ticker.Ticker{
				Exchange:  "tinkoff",
				Symbol:    sym,
				Quote:     uidToCurrency[uid],
				Price:     strconv.FormatFloat(price, 'f', -1, 64),
				CreatedAt: lp.GetTime().AsTime(),
			}
			if c.tickerSvc != nil {
				c.tickerSvc.Send(t)
			}
		}
	}
}

// consumeOrderBooks читает полные снимки стакана и обновляет Store.
func (c *Client) consumeOrderBooks(ctx context.Context, ch <-chan *pb.OrderBook, uidToSymbol map[string]string) {
	for {
		select {
		case <-ctx.Done():
			return
		case ob, ok := <-ch:
			if !ok {
				return
			}
			uid := ob.GetInstrumentUid()
			if uid == "" {
				uid = ob.GetFigi()
			}
			sym, exists := uidToSymbol[uid]
			if !exists {
				continue
			}

			bids := convertOrders(ob.GetBids())
			asks := convertOrders(ob.GetAsks())
			// UnixNano как монотонный идентификатор обновления
			updateID := ob.GetTime().AsTime().UnixNano()

			lb := c.obStore.GetOrCreate(sym)
			lb.Init(bids, asks, updateID, c.log)

			if c.onOrderBook != nil {
				snap := &orderbook.OrderBook{
					Symbol:    sym,
					UpdatedAt: ob.GetTime().AsTime(),
				}
				snap.Bids = make([]orderbook.Entry, len(bids))
				snap.Asks = make([]orderbook.Entry, len(asks))
				for i, b := range bids {
					snap.Bids[i] = orderbook.Entry{Price: b[0], Qty: b[1]}
				}
				for i, a := range asks {
					snap.Asks[i] = orderbook.Entry{Price: a[0], Qty: a[1]}
				}
				c.onOrderBook(snap)
			}

			c.log.Debug("tinkoff: orderbook updated",
				zap.String("symbol", sym),
				zap.Int("bids", len(bids)),
				zap.Int("asks", len(asks)),
			)
		}
	}
}

// consumeTrades читает ленту сделок и вызывает onTrade.
func (c *Client) consumeTrades(ctx context.Context, ch <-chan *pb.Trade, uidToSymbol map[string]string) {
	for {
		select {
		case <-ctx.Done():
			return
		case trade, ok := <-ch:
			if !ok {
				return
			}
			if c.onTrade == nil {
				continue
			}
			uid := trade.GetInstrumentUid()
			if uid == "" {
				uid = trade.GetFigi()
			}
			sym, exists := uidToSymbol[uid]
			if !exists {
				continue
			}
			price := quotationToFloat(trade.GetPrice())
			order := exchange_orders.ExchangeOrder{
				Exchange:  "tinkoff",
				Symbol:    sym,
				Price:     strconv.FormatFloat(price, 'f', -1, 64),
				Quantity:  strconv.FormatInt(trade.GetQuantity(), 10),
				Side:      directionToSide(trade.GetDirection()),
				TradeTime: trade.GetTime().AsTime(),
			}
			c.onTrade(order)
		}
	}
}

// convertOrders преобразует срез pb.Order в [][2]string{price, qty} для LocalBook.
func convertOrders(orders []*pb.Order) [][2]string {
	result := make([][2]string, 0, len(orders))
	for _, o := range orders {
		price := quotationToFloat(o.GetPrice())
		qty := float64(o.GetQuantity())
		result = append(result, [2]string{
			strconv.FormatFloat(price, 'f', -1, 64),
			strconv.FormatFloat(qty, 'f', -1, 64),
		})
	}
	return result
}

// quotationToFloat преобразует Quotation (units + nano) в float64.
func quotationToFloat(q *pb.Quotation) float64 {
	if q == nil {
		return 0
	}
	return float64(q.GetUnits()) + float64(q.GetNano())/1e9
}

// directionToSide преобразует TradeDirection в строку "buy"/"sell".
func directionToSide(d pb.TradeDirection) string {
	if d == pb.TradeDirection_TRADE_DIRECTION_BUY {
		return "buy"
	}
	return "sell"
}

// currencyByClassCode определяет валюту котирования по коду класса инструмента.
func currencyByClassCode(classCode string) string {
	switch classCode {
	case "TQBR", "TQTF", "TQIF", "TQOB":
		return "RUB"
	case "SPBXM", "SPBHK":
		return "USD"
	default:
		return "RUB"
	}
}

func nextWait(current, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}
	return next
}
