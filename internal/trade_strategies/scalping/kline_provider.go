package scalping

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// KlineProvider получает исторические свечи через REST и подписывается на WS kline-стримы.
type KlineProvider struct {
	exchange    string
	restBaseURL string
	wsBaseURL   string
	http        *http.Client
	log         *zap.Logger
}

// NewKlineProvider создаёт KlineProvider для указанной биржи.
// Всегда использует mainnet-эндпоинты для рыночных данных — testnet не имеет большинства символов.
// DEV_MODE влияет только на торговые клиенты (rest.go, ws_trade_client.go).
func NewKlineProvider(exchange string, log *zap.Logger) *KlineProvider {
	var restBaseURL, wsBaseURL string
	switch strings.ToLower(exchange) {
	case "binance":
		restBaseURL = "https://api.binance.com"
		wsBaseURL = "wss://stream.binance.com:9443/ws"
	case "bybit":
		restBaseURL = "https://api.bybit.com"
		wsBaseURL = "wss://stream.bybit.com/v5/public/spot"
	default:
		log.Warn("kline_provider: unknown exchange, defaulting to binance", zap.String("exchange", exchange))
		restBaseURL = "https://api.binance.com"
		wsBaseURL = "wss://stream.binance.com:9443/ws"
	}

	return &KlineProvider{
		exchange:    strings.ToLower(exchange),
		restBaseURL: restBaseURL,
		wsBaseURL:   wsBaseURL,
		http:        &http.Client{Timeout: 15 * time.Second},
		log:         log,
	}
}

// FetchHistory загружает исторические свечи через REST (однократно при старте).
func (p *KlineProvider) FetchHistory(ctx context.Context, symbol, interval string, limit int) ([]Candle, error) {
	switch p.exchange {
	case "binance":
		return p.fetchBinanceHistory(ctx, symbol, interval, limit)
	case "bybit":
		return p.fetchBybitHistory(ctx, symbol, interval, limit)
	default:
		return nil, fmt.Errorf("kline_provider: unsupported exchange %s", p.exchange)
	}
}

// Subscribe запускает горутину подписки на kline WS-стрим.
// onCandle вызывается для каждой полученной свечи (в том числе незакрытых).
func (p *KlineProvider) Subscribe(ctx context.Context, symbol, interval string, onCandle func(Candle)) {
	switch p.exchange {
	case "binance":
		go p.subscribeBinance(ctx, symbol, interval, onCandle)
	case "bybit":
		go p.subscribeBybit(ctx, symbol, interval, onCandle)
	default:
		p.log.Error("kline_provider: unsupported exchange for subscribe", zap.String("exchange", p.exchange))
	}
}

// ─── Binance ─────────────────────────────────────────────────────────────────

func (p *KlineProvider) fetchBinanceHistory(ctx context.Context, symbol, interval string, limit int) ([]Candle, error) {
	url := fmt.Sprintf("%s/api/v3/klines?symbol=%s&interval=%s&limit=%d",
		p.restBaseURL, symbol, interval, limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("binance klines REST: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("binance klines REST status %d: %s", resp.StatusCode, body)
	}

	// [[openTime, open, high, low, close, volume, closeTime, ...], ...]
	var raw [][]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("binance klines unmarshal: %w", err)
	}

	candles := make([]Candle, 0, len(raw))
	for _, k := range raw {
		if len(k) < 6 {
			continue
		}
		var openTimeMs int64
		var openStr, highStr, lowStr, closeStr, volStr string
		json.Unmarshal(k[0], &openTimeMs)  //nolint:errcheck
		json.Unmarshal(k[1], &openStr)     //nolint:errcheck
		json.Unmarshal(k[2], &highStr)     //nolint:errcheck
		json.Unmarshal(k[3], &lowStr)      //nolint:errcheck
		json.Unmarshal(k[4], &closeStr)    //nolint:errcheck
		json.Unmarshal(k[5], &volStr)      //nolint:errcheck

		o, _ := strconv.ParseFloat(openStr, 64)
		h, _ := strconv.ParseFloat(highStr, 64)
		l, _ := strconv.ParseFloat(lowStr, 64)
		c, _ := strconv.ParseFloat(closeStr, 64)
		v, _ := strconv.ParseFloat(volStr, 64)

		candles = append(candles, Candle{
			OpenTime: time.UnixMilli(openTimeMs),
			Open:     o, High: h, Low: l, Close: c,
			Volume:   v,
			IsClosed: true, // исторические свечи всегда закрыты
		})
	}
	p.log.Info("kline_provider: binance history fetched",
		zap.String("symbol", symbol),
		zap.String("interval", interval),
		zap.Int("count", len(candles)),
	)
	return candles, nil
}

func (p *KlineProvider) subscribeBinance(ctx context.Context, symbol, interval string, onCandle func(Candle)) {
	streamName := strings.ToLower(symbol) + "@kline_" + interval
	wsURL := p.wsBaseURL + "/" + streamName

	reconnectDelay := time.Second
	for {
		if ctx.Err() != nil {
			return
		}

		conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
		if err != nil {
			p.log.Warn("kline_provider: binance ws dial failed",
				zap.String("symbol", symbol), zap.Error(err))
			select {
			case <-time.After(reconnectDelay):
				reconnectDelay = minDuration(reconnectDelay*2, 60*time.Second)
			case <-ctx.Done():
				return
			}
			continue
		}
		reconnectDelay = time.Second

		p.log.Info("kline_provider: connected to binance kline stream",
			zap.String("symbol", symbol), zap.String("interval", interval))

		p.readBinanceLoop(ctx, conn, symbol, onCandle)
		conn.Close()
	}
}

func (p *KlineProvider) readBinanceLoop(ctx context.Context, conn *websocket.Conn, symbol string, onCandle func(Candle)) {
	for {
		if ctx.Err() != nil {
			return
		}
		conn.SetReadDeadline(time.Now().Add(90 * time.Second)) //nolint:errcheck
		_, msg, err := conn.ReadMessage()
		if err != nil {
			p.log.Warn("kline_provider: binance ws read error",
				zap.String("symbol", symbol), zap.Error(err))
			return
		}

		var event struct {
			K struct {
				T int64  `json:"t"` // open time ms
				O string `json:"o"`
				H string `json:"h"`
				L string `json:"l"`
				C string `json:"c"`
				V string `json:"v"`
				X bool   `json:"x"` // is closed
			} `json:"k"`
		}
		if err := json.Unmarshal(msg, &event); err != nil {
			continue
		}

		o, _ := strconv.ParseFloat(event.K.O, 64)
		h, _ := strconv.ParseFloat(event.K.H, 64)
		l, _ := strconv.ParseFloat(event.K.L, 64)
		c, _ := strconv.ParseFloat(event.K.C, 64)
		v, _ := strconv.ParseFloat(event.K.V, 64)

		onCandle(Candle{
			OpenTime: time.UnixMilli(event.K.T),
			Open:     o, High: h, Low: l, Close: c,
			Volume:   v,
			IsClosed: event.K.X,
		})
	}
}

// ─── Bybit ───────────────────────────────────────────────────────────────────

func (p *KlineProvider) fetchBybitHistory(ctx context.Context, symbol, interval string, limit int) ([]Candle, error) {
	bybitInterval := convertIntervalToBybit(interval)
	url := fmt.Sprintf("%s/v5/market/kline?category=spot&symbol=%s&interval=%s&limit=%d",
		p.restBaseURL, symbol, bybitInterval, limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bybit klines REST: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bybit klines REST status %d: %s", resp.StatusCode, body)
	}

	var raw struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			// [startTime, open, high, low, close, volume, turnover]
			List [][]string `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("bybit klines unmarshal: %w", err)
	}
	if raw.RetCode != 0 {
		return nil, fmt.Errorf("bybit klines api error: %d %s", raw.RetCode, raw.RetMsg)
	}

	// Bybit возвращает данные в обратном порядке (новейшие первыми)
	list := raw.Result.List
	candles := make([]Candle, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		k := list[i]
		if len(k) < 6 {
			continue
		}
		startMs, _ := strconv.ParseInt(k[0], 10, 64)
		o, _ := strconv.ParseFloat(k[1], 64)
		h, _ := strconv.ParseFloat(k[2], 64)
		l, _ := strconv.ParseFloat(k[3], 64)
		c, _ := strconv.ParseFloat(k[4], 64)
		v, _ := strconv.ParseFloat(k[5], 64)

		candles = append(candles, Candle{
			OpenTime: time.UnixMilli(startMs),
			Open:     o, High: h, Low: l, Close: c,
			Volume:   v,
			IsClosed: true,
		})
	}
	p.log.Info("kline_provider: bybit history fetched",
		zap.String("symbol", symbol),
		zap.String("interval", interval),
		zap.Int("count", len(candles)),
	)
	return candles, nil
}

func (p *KlineProvider) subscribeBybit(ctx context.Context, symbol, interval string, onCandle func(Candle)) {
	bybitInterval := convertIntervalToBybit(interval)
	topic := fmt.Sprintf("kline.%s.%s", bybitInterval, symbol)

	reconnectDelay := time.Second
	for {
		if ctx.Err() != nil {
			return
		}

		conn, _, err := websocket.DefaultDialer.DialContext(ctx, p.wsBaseURL, nil)
		if err != nil {
			p.log.Warn("kline_provider: bybit ws dial failed",
				zap.String("symbol", symbol), zap.Error(err))
			select {
			case <-time.After(reconnectDelay):
				reconnectDelay = minDuration(reconnectDelay*2, 60*time.Second)
			case <-ctx.Done():
				return
			}
			continue
		}
		reconnectDelay = time.Second

		// Отправляем subscribe
		subMsg, _ := json.Marshal(map[string]interface{}{
			"op":   "subscribe",
			"args": []string{topic},
		})
		if err := conn.WriteMessage(websocket.TextMessage, subMsg); err != nil {
			p.log.Warn("kline_provider: bybit subscribe failed", zap.Error(err))
			conn.Close()
			continue
		}

		p.log.Info("kline_provider: connected to bybit kline stream",
			zap.String("symbol", symbol), zap.String("interval", interval))

		p.readBybitLoop(ctx, conn, symbol, onCandle)
		conn.Close()
	}
}

func (p *KlineProvider) readBybitLoop(ctx context.Context, conn *websocket.Conn, symbol string, onCandle func(Candle)) {
	for {
		if ctx.Err() != nil {
			return
		}
		conn.SetReadDeadline(time.Now().Add(90 * time.Second)) //nolint:errcheck
		_, msg, err := conn.ReadMessage()
		if err != nil {
			p.log.Warn("kline_provider: bybit ws read error",
				zap.String("symbol", symbol), zap.Error(err))
			return
		}

		// Обработка JSON-пинга
		var op struct {
			Op string `json:"op"`
		}
		if json.Unmarshal(msg, &op) == nil && op.Op == "ping" {
			conn.WriteJSON(map[string]string{"op": "pong"}) //nolint:errcheck
			continue
		}

		var event struct {
			Topic string `json:"topic"`
			Data  []struct {
				Start   int64  `json:"start"`
				Open    string `json:"open"`
				High    string `json:"high"`
				Low     string `json:"low"`
				Close   string `json:"close"`
				Volume  string `json:"volume"`
				Confirm bool   `json:"confirm"` // true = свеча закрыта
			} `json:"data"`
		}
		if err := json.Unmarshal(msg, &event); err != nil || len(event.Data) == 0 {
			continue
		}

		for _, k := range event.Data {
			o, _ := strconv.ParseFloat(k.Open, 64)
			h, _ := strconv.ParseFloat(k.High, 64)
			l, _ := strconv.ParseFloat(k.Low, 64)
			c, _ := strconv.ParseFloat(k.Close, 64)
			v, _ := strconv.ParseFloat(k.Volume, 64)

			onCandle(Candle{
				OpenTime: time.UnixMilli(k.Start),
				Open:     o, High: h, Low: l, Close: c,
				Volume:   v,
				IsClosed: k.Confirm,
			})
		}
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// convertIntervalToBybit конвертирует Binance-формат интервала в формат Bybit.
func convertIntervalToBybit(interval string) string {
	switch interval {
	case "1m":
		return "1"
	case "3m":
		return "3"
	case "5m":
		return "5"
	case "15m":
		return "15"
	case "30m":
		return "30"
	case "1h":
		return "60"
	case "2h":
		return "120"
	case "4h":
		return "240"
	case "6h":
		return "360"
	case "12h":
		return "720"
	case "1d":
		return "D"
	case "1w":
		return "W"
	default:
		return "1"
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
