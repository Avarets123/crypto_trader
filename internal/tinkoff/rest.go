package tinkoff

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	investgo "github.com/russianinvestments/invest-api-go-sdk/investgo"
	pb "github.com/russianinvestments/invest-api-go-sdk/proto"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/osman/bot-traider/internal/shared/exchange"
)

var _ exchange.RestClient = (*RestClient)(nil)

const sandboxAccountCacheFile = ".tinkoff_sandbox_account_id"

type instrumentInfo struct {
	UID     string
	LotSize int
}

// RestClient — REST-клиент Т-Инвестиции, реализует exchange.RestClient.
// Торгует акциями TQBR через gRPC OrdersService / SandboxService.
// GetFreeUSDT возвращает свободный RUB-баланс (основная валюта для TQBR).
type RestClient struct {
	cfg       *Config
	log       *zap.Logger
	sdkClient *investgo.Client // для ордеров/аккаунта (sandbox или prod)
	instClient *investgo.Client // для InstrumentsService — всегда prod-endpoint

	mu    sync.RWMutex
	cache map[string]instrumentInfo // ticker → {UID, LotSize}
}

// NewRestClient создаёт REST-клиент и проверяет/инициализирует аккаунт.
// В sandbox-режиме при пустом AccountID автоматически создаёт или переиспользует
// существующий sandbox-счёт, кэшируя ID в файл sandboxAccountCacheFile.
func NewRestClient(ctx context.Context, cfg *Config, log *zap.Logger) (*RestClient, error) {
	endpoint := "invest-public-api.tinkoff.ru:443"
	if cfg.Sandbox {
		endpoint = "sandbox-invest-public-api.tinkoff.ru:443"
	}

	sdkClient, err := investgo.NewClient(ctx, investgo.Config{
		Token:    cfg.Token,
		EndPoint: endpoint,
		AppName:  "bot-traider",
	}, &zapLogger{log})
	if err != nil {
		return nil, fmt.Errorf("tinkoff rest: create sdk client: %w", err)
	}

	// Отдельный клиент для InstrumentsService — sandbox-endpoint не поддерживает
	// большие ответы (unexpected EOF на FindInstrument/ShareByUid)
	instSdkClient, err := investgo.NewClient(ctx, investgo.Config{
		Token:    cfg.Token,
		EndPoint: "invest-public-api.tinkoff.ru:443",
		AppName:  "bot-traider",
	}, &zapLogger{log})
	if err != nil {
		sdkClient.Stop() //nolint:errcheck
		return nil, fmt.Errorf("tinkoff rest: create instruments sdk client: %w", err)
	}

	c := &RestClient{
		cfg:        cfg,
		log:        log,
		sdkClient:  sdkClient,
		instClient: instSdkClient,
		cache:      make(map[string]instrumentInfo),
	}

	stopAll := func() {
		sdkClient.Stop()     //nolint:errcheck
		instSdkClient.Stop() //nolint:errcheck
	}

	if cfg.Sandbox && cfg.AccountID == "" {
		accountID, err := c.resolveSandboxAccount()
		if err != nil {
			stopAll()
			return nil, fmt.Errorf("tinkoff rest: sandbox account: %w", err)
		}
		cfg.AccountID = accountID
	}

	if err := c.checkAccount(); err != nil {
		stopAll()
		return nil, fmt.Errorf("tinkoff rest: %w", err)
	}

	if cfg.Sandbox && cfg.TradeEnabled {
		if err := c.ensureSandboxBalance(); err != nil {
			log.Warn("tinkoff rest: sandbox balance top-up failed", zap.Error(err))
		}
	}

	log.Info("tinkoff rest client ready",
		zap.String("account_id", cfg.AccountID),
		zap.Bool("sandbox", cfg.Sandbox),
	)
	return c, nil
}

// Stop закрывает SDK-клиенты.
func (c *RestClient) Stop() {
	c.sdkClient.Stop()  //nolint:errcheck
	c.instClient.Stop() //nolint:errcheck
}

// resolveSandboxAccount возвращает ID sandbox-счёта:
// 1. Читает из файла-кэша
// 2. Запрашивает существующие sandbox-счета через API
// 3. Создаёт новый счёт если нет ни одного
// Найденный/созданный ID сохраняется в файл для следующих запусков.
func (c *RestClient) resolveSandboxAccount() (string, error) {
	// 1. Пробуем файловый кэш
	if id := readSandboxAccountCache(c.log); id != "" {
		c.log.Info("tinkoff sandbox: account loaded from cache", zap.String("account_id", id))
		return id, nil
	}

	sandboxClient := c.sdkClient.NewSandboxServiceClient()

	// 2. Проверяем уже существующие счета
	existing, err := sandboxClient.GetSandboxAccounts()
	if err != nil {
		return "", fmt.Errorf("get sandbox accounts: %w", err)
	}
	if accs := existing.GetAccounts(); len(accs) > 0 {
		id := accs[0].GetId()
		c.log.Info("tinkoff sandbox: reusing existing account",
			zap.String("account_id", id),
			zap.String("name", accs[0].GetName()),
		)
		saveSandboxAccountCache(id, c.log)
		return id, nil
	}

	// 3. Создаём новый sandbox-счёт
	resp, err := sandboxClient.OpenSandboxAccount()
	if err != nil {
		return "", fmt.Errorf("open sandbox account: %w", err)
	}
	id := resp.GetAccountId()
	c.log.Info("tinkoff sandbox: new account created", zap.String("account_id", id))
	saveSandboxAccountCache(id, c.log)
	return id, nil
}

func readSandboxAccountCache(log *zap.Logger) string {
	data, err := os.ReadFile(sandboxAccountCacheFile)
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return ""
	}
	log.Debug("tinkoff sandbox: cache file read", zap.String("file", sandboxAccountCacheFile))
	return id
}

func saveSandboxAccountCache(id string, log *zap.Logger) {
	if err := os.WriteFile(sandboxAccountCacheFile, []byte(id), 0600); err != nil {
		log.Warn("tinkoff sandbox: failed to save account cache",
			zap.String("file", sandboxAccountCacheFile),
			zap.Error(err),
		)
		return
	}
	log.Info("tinkoff sandbox: account id cached", zap.String("file", sandboxAccountCacheFile))
}

func (c *RestClient) checkAccount() error {
	if c.cfg.Sandbox {
		sandboxClient := c.sdkClient.NewSandboxServiceClient()
		resp, err := sandboxClient.GetSandboxAccounts()
		if err != nil {
			return fmt.Errorf("get sandbox accounts: %w", err)
		}
		for _, acc := range resp.GetAccounts() {
			if acc.GetId() == c.cfg.AccountID {
				c.log.Info("tinkoff sandbox: account verified",
					zap.String("id", acc.GetId()),
					zap.String("name", acc.GetName()),
				)
				return nil
			}
		}
		return fmt.Errorf("sandbox account %q not found — delete %s to recreate", c.cfg.AccountID, sandboxAccountCacheFile)
	}

	usersClient := c.sdkClient.NewUsersServiceClient()
	resp, err := usersClient.GetAccounts(nil)
	if err != nil {
		return fmt.Errorf("get accounts: %w", err)
	}
	for _, acc := range resp.GetAccounts() {
		if acc.GetId() == c.cfg.AccountID {
			c.log.Info("tinkoff: account verified",
				zap.String("id", acc.GetId()),
				zap.String("name", acc.GetName()),
			)
			return nil
		}
	}
	ids := make([]string, 0, len(resp.GetAccounts()))
	for _, acc := range resp.GetAccounts() {
		ids = append(ids, acc.GetId())
	}
	return fmt.Errorf("account %q not found, available: %v", c.cfg.AccountID, ids)
}

// sandboxTopUpRUB — сумма пополнения sandbox-счёта при нехватке баланса (рублей).
const sandboxTopUpRUB = 1_000_000

// ensureSandboxBalance пополняет sandbox-счёт если баланс RUB равен нулю или недостаточен.
func (c *RestClient) ensureSandboxBalance() error {
	sandboxClient := c.sdkClient.NewSandboxServiceClient()
	resp, err := sandboxClient.GetSandboxWithdrawLimits(c.cfg.AccountID)
	if err != nil {
		return fmt.Errorf("get sandbox balance: %w", err)
	}
	var rubBalance float64
	for _, mv := range resp.GetMoney() {
		if strings.ToLower(mv.GetCurrency()) == "rub" {
			rubBalance = moneyValueToFloat(mv)
			break
		}
	}
	if rubBalance >= float64(sandboxTopUpRUB)/2 {
		c.log.Info("tinkoff sandbox: balance sufficient", zap.Float64("rub", rubBalance))
		return nil
	}
	_, err = sandboxClient.SandboxPayIn(&investgo.SandboxPayInRequest{
		AccountId: c.cfg.AccountID,
		Currency:  "RUB",
		Unit:      sandboxTopUpRUB,
		Nano:      0,
	})
	if err != nil {
		return fmt.Errorf("sandbox pay in: %w", err)
	}
	c.log.Info("tinkoff sandbox: balance topped up",
		zap.Float64("before", rubBalance),
		zap.Int64("added_rub", sandboxTopUpRUB),
	)
	return nil
}

// resolveInstrument возвращает UID и размер лота для тикера.
// Результат кэшируется — повторные вызовы не ходят в API.
func (c *RestClient) resolveInstrument(symbol string) (instrumentInfo, error) {
	c.mu.RLock()
	info, ok := c.cache[symbol]
	c.mu.RUnlock()
	if ok {
		return info, nil
	}

	instClient := c.instClient.NewInstrumentsServiceClient()
	findResp, err := instClient.FindInstrument(symbol)
	if err != nil {
		return instrumentInfo{}, fmt.Errorf("tinkoff: find instrument %q: %w", symbol, err)
	}

	var uid string
	for _, inst := range findResp.GetInstruments() {
		if !strings.EqualFold(inst.GetTicker(), symbol) {
			continue
		}
		if inst.GetInstrumentType() != "share" {
			continue
		}
		if inst.GetClassCode() == c.cfg.ExchangeFilter {
			uid = inst.GetUid()
			break
		}
		if uid == "" {
			uid = inst.GetUid()
		}
	}
	if uid == "" {
		return instrumentInfo{}, fmt.Errorf("tinkoff: instrument %q not found", symbol)
	}

	shareResp, err := instClient.ShareByUid(uid)
	if err != nil {
		return instrumentInfo{}, fmt.Errorf("tinkoff: get share info %q: %w", symbol, err)
	}

	lotSize := int(shareResp.GetInstrument().GetLot())
	if lotSize < 1 {
		lotSize = 1
	}

	info = instrumentInfo{UID: uid, LotSize: lotSize}
	c.mu.Lock()
	c.cache[symbol] = info
	c.mu.Unlock()

	c.log.Info("tinkoff: instrument resolved",
		zap.String("symbol", symbol),
		zap.String("uid", uid),
		zap.Int("lot_size", lotSize),
	)
	return info, nil
}

// lastPrice возвращает последнюю цену инструмента через MarketDataService.
// Использует prod-endpoint (instClient) — sandbox не всегда возвращает актуальные цены.
func (c *RestClient) lastPrice(uid string) (float64, error) {
	mdClient := c.instClient.NewMarketDataServiceClient()
	resp, err := mdClient.GetLastPrices([]string{uid})
	if err != nil {
		return 0, fmt.Errorf("tinkoff get last price: %w", err)
	}
	for _, lp := range resp.GetLastPrices() {
		if lp.GetInstrumentUid() == uid || lp.GetFigi() == uid {
			return quotationToFloat(lp.GetPrice()), nil
		}
	}
	return 0, fmt.Errorf("tinkoff: last price not found for uid %q", uid)
}

// lotsFromConfig вычисляет количество лотов из TINKOFF_TRADE_AMOUNT_RUB.
// lots = floor(TradeAmountRUB / (pricePerShare * lotSize)), минимум 1.
func (c *RestClient) lotsFromConfig(pricePerShare float64, lotSize int) int64 {
	if c.cfg.TradeAmountRUB <= 0 || pricePerShare <= 0 || lotSize < 1 {
		return 0 // сигнал: использовать внешний qty
	}
	lots := int64(math.Floor(c.cfg.TradeAmountRUB / (pricePerShare * float64(lotSize))))
	if lots < 1 {
		lots = 1
	}
	return lots
}

// PlaceMarketOrder выставляет рыночный ордер.
// Если TINKOFF_TRADE_AMOUNT_RUB > 0 — qty игнорируется, лоты считаются по последней цене.
// Иначе qty (штук акций) округляется до ближайшего целого лота.
func (c *RestClient) PlaceMarketOrder(ctx context.Context, symbol, side string, qty float64) (exchange.OrderResult, error) {
	_ = ctx
	info, err := c.resolveInstrument(symbol)
	if err != nil {
		return exchange.OrderResult{}, err
	}

	var lots int64
	if c.cfg.TradeAmountRUB > 0 {
		price, err := c.lastPrice(info.UID)
		if err != nil {
			return exchange.OrderResult{}, err
		}
		lots = c.lotsFromConfig(price, info.LotSize)
		c.log.Info("tinkoff: lots from TradeAmountRUB",
			zap.String("symbol", symbol),
			zap.Float64("last_price", price),
			zap.Float64("trade_amount_rub", c.cfg.TradeAmountRUB),
			zap.Int64("lots", lots),
		)
	} else {
		lots = qtyToLots(qty, info.LotSize)
	}
	if lots <= 0 {
		return exchange.OrderResult{}, fmt.Errorf("tinkoff place market order: lots=0 for %q (price=0 or TradeAmountRUB too small)", symbol)
	}

	direction := sideToDirection(side)

	c.log.Info("tinkoff: placing market order",
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.Float64("qty", qty),
		zap.Int64("lots", lots),
	)

	req := &investgo.PostOrderRequest{
		InstrumentId: info.UID,
		Quantity:     lots,
		Direction:    direction,
		AccountId:    c.cfg.AccountID,
		OrderType:    pb.OrderType_ORDER_TYPE_MARKET,
		OrderId:      generateOrderID(),
	}

	resp, err := c.postOrderWithRetry(req)
	if err != nil {
		return exchange.OrderResult{}, fmt.Errorf("tinkoff place market order: %w", err)
	}

	execPrice := moneyValueToFloat(resp.GetExecutedOrderPrice())
	execQty := float64(resp.GetLotsExecuted()) * float64(info.LotSize)

	c.log.Info("tinkoff: market order placed",
		zap.String("order_id", resp.GetOrderId()),
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.Float64("exec_qty", execQty),
		zap.Float64("exec_price", execPrice),
	)

	return exchange.OrderResult{
		OrderID: resp.GetOrderId(),
		Price:   execPrice,
		Qty:     execQty,
	}, nil
}

// PlaceLimitOrder выставляет лимитный ордер.
// Если TINKOFF_TRADE_AMOUNT_RUB > 0 — qty игнорируется, лоты считаются по переданной цене.
// postOnly игнорируется — Tinkoff не поддерживает LIMIT_MAKER эквивалент в этом методе.
func (c *RestClient) PlaceLimitOrder(ctx context.Context, symbol, side string, qty, price float64, _ bool) (exchange.OrderResult, error) {
	_ = ctx
	info, err := c.resolveInstrument(symbol)
	if err != nil {
		return exchange.OrderResult{}, err
	}

	lots := c.lotsFromConfig(price, info.LotSize)
	if lots == 0 {
		lots = qtyToLots(qty, info.LotSize)
	}
	direction := sideToDirection(side)

	c.log.Info("tinkoff: placing limit order",
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.Float64("qty", qty),
		zap.Int64("lots", lots),
		zap.Float64("price", price),
	)

	req := &investgo.PostOrderRequest{
		InstrumentId: info.UID,
		Quantity:     lots,
		Price:        floatToQuotation(price),
		Direction:    direction,
		AccountId:    c.cfg.AccountID,
		OrderType:    pb.OrderType_ORDER_TYPE_LIMIT,
		OrderId:      generateOrderID(),
	}

	resp, err := c.postOrderWithRetry(req)
	if err != nil {
		return exchange.OrderResult{}, fmt.Errorf("tinkoff place limit order: %w", err)
	}

	c.log.Info("tinkoff: limit order placed",
		zap.String("order_id", resp.GetOrderId()),
		zap.String("symbol", symbol),
		zap.String("side", side),
		zap.Float64("price", price),
		zap.Int64("lots", lots),
	)

	return exchange.OrderResult{
		OrderID: resp.GetOrderId(),
		Price:   price,
		Qty:     float64(lots) * float64(info.LotSize),
	}, nil
}

// CancelOrder отменяет заявку по orderID.
func (c *RestClient) CancelOrder(ctx context.Context, _ string, orderID string) error {
	_ = ctx
	c.log.Info("tinkoff: cancelling order", zap.String("order_id", orderID))

	var err error
	if c.cfg.Sandbox {
		_, err = c.sdkClient.NewSandboxServiceClient().CancelSandboxOrder(c.cfg.AccountID, orderID)
	} else {
		_, err = c.sdkClient.NewOrdersServiceClient().CancelOrder(c.cfg.AccountID, orderID, nil)
	}
	if err != nil {
		return fmt.Errorf("tinkoff cancel order %q: %w", orderID, err)
	}

	c.log.Info("tinkoff: order cancelled", zap.String("order_id", orderID))
	return nil
}

// GetOpenOrders возвращает активные заявки.
// Если symbol пустой — возвращает все активные заявки.
func (c *RestClient) GetOpenOrders(ctx context.Context, symbol string) ([]exchange.OpenOrder, error) {
	_ = ctx

	var orders []*pb.OrderState
	if c.cfg.Sandbox {
		resp, err := c.sdkClient.NewSandboxServiceClient().GetSandboxOrders(c.cfg.AccountID)
		if err != nil {
			return nil, fmt.Errorf("tinkoff get sandbox orders: %w", err)
		}
		orders = resp.GetOrders()
	} else {
		resp, err := c.sdkClient.NewOrdersServiceClient().GetOrders(c.cfg.AccountID, nil)
		if err != nil {
			return nil, fmt.Errorf("tinkoff get orders: %w", err)
		}
		orders = resp.GetOrders()
	}

	c.mu.RLock()
	uidToSymbol := make(map[string]string, len(c.cache))
	for sym, info := range c.cache {
		uidToSymbol[info.UID] = sym
	}
	c.mu.RUnlock()

	result := make([]exchange.OpenOrder, 0, len(orders))
	for _, o := range orders {
		sym := uidToSymbol[o.GetInstrumentUid()]
		if sym == "" {
			sym = o.GetInstrumentUid()
		}
		if symbol != "" && !strings.EqualFold(sym, symbol) {
			continue
		}

		c.mu.RLock()
		lotSize := 1
		if info, ok := c.cache[sym]; ok {
			lotSize = info.LotSize
		}
		c.mu.RUnlock()

		result = append(result, exchange.OpenOrder{
			OrderID: o.GetOrderId(),
			Symbol:  sym,
			Side:    directionToSideStr(o.GetDirection()),
			Price:   moneyValueToFloat(o.GetInitialSecurityPrice()),
			Qty:     float64(o.GetLotsRequested()) * float64(lotSize),
		})
	}
	return result, nil
}

// GetFreeUSDT возвращает свободный RUB-баланс (основная валюта TQBR).
func (c *RestClient) GetFreeUSDT(ctx context.Context) (float64, error) {
	_ = ctx

	var moneyList []*pb.MoneyValue
	if c.cfg.Sandbox {
		resp, err := c.sdkClient.NewSandboxServiceClient().GetSandboxWithdrawLimits(c.cfg.AccountID)
		if err != nil {
			return 0, fmt.Errorf("tinkoff get sandbox withdraw limits: %w", err)
		}
		moneyList = resp.GetMoney()
	} else {
		resp, err := c.sdkClient.NewOperationsServiceClient().GetWithdrawLimits(c.cfg.AccountID)
		if err != nil {
			return 0, fmt.Errorf("tinkoff get withdraw limits: %w", err)
		}
		moneyList = resp.GetMoney()
	}

	for _, mv := range moneyList {
		if strings.ToLower(mv.GetCurrency()) == "rub" {
			return moneyValueToFloat(mv), nil
		}
	}
	return 0, nil
}

func qtyToLots(qty float64, lotSize int) int64 {
	if lotSize < 1 {
		lotSize = 1
	}
	lots := int64(math.Round(qty / float64(lotSize)))
	if lots < 1 {
		lots = 1
	}
	return lots
}

func sideToDirection(side string) pb.OrderDirection {
	if strings.ToLower(side) == "sell" {
		return pb.OrderDirection_ORDER_DIRECTION_SELL
	}
	return pb.OrderDirection_ORDER_DIRECTION_BUY
}

func directionToSideStr(d pb.OrderDirection) string {
	if d == pb.OrderDirection_ORDER_DIRECTION_SELL {
		return "SELL"
	}
	return "BUY"
}

func floatToQuotation(f float64) *pb.Quotation {
	units := int64(f)
	nano := int32(math.Round((f - float64(units)) * 1e9))
	return &pb.Quotation{Units: units, Nano: nano}
}

func moneyValueToFloat(mv *pb.MoneyValue) float64 {
	if mv == nil {
		return 0
	}
	return float64(mv.GetUnits()) + float64(mv.GetNano())/1e9
}

// postOrderWithRetry выставляет ордер с повтором при ошибке 80002 (request rate limit).
// Другие ResourceExhausted подкоды (80001, 80004-80006) не ретраятся.
func (c *RestClient) postOrderWithRetry(req *investgo.PostOrderRequest) (*investgo.PostOrderResponse, error) {
	const maxAttempts = 3
	const rateLimitDelay = 10 * time.Second // 80002 = лимит запросов/мин

	for attempt := 0; attempt < maxAttempts; attempt++ {
		var resp *investgo.PostOrderResponse
		var err error
		if c.cfg.Sandbox {
			resp, err = c.sdkClient.NewSandboxServiceClient().PostSandboxOrder(req)
		} else {
			resp, err = c.sdkClient.NewOrdersServiceClient().PostOrder(req)
		}
		if err == nil {
			return resp, nil
		}
		st, ok := status.FromError(err)
		if !ok || st.Code() != codes.ResourceExhausted {
			return nil, err
		}
		// Ретраим только 80002 (Request limit exceeded)
		if !strings.Contains(st.Message(), "80002") {
			return nil, fmt.Errorf("%w (sub-code: %s)", err, st.Message())
		}
		if attempt >= maxAttempts-1 {
			break
		}
		c.log.Warn("tinkoff: request rate limit (80002), retrying",
			zap.Int("attempt", attempt+1),
			zap.Duration("delay", rateLimitDelay),
		)
		time.Sleep(rateLimitDelay)
		req.OrderId = generateOrderID()
	}
	return nil, fmt.Errorf("tinkoff: order failed after %d attempts (rate limit)", maxAttempts)
}

func generateOrderID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	// UUID v4 формат: xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
