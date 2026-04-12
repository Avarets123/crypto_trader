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
	return p.fetchBinanceHistory(ctx, symbol, interval, limit)
}

// Subscribe запускает горутину подписки на kline WS-стрим.
// onCandle вызывается для каждой полученной свечи (в том числе незакрытых).
func (p *KlineProvider) Subscribe(ctx context.Context, symbol, interval string, onCandle func(Candle)) {
	go p.subscribeBinance(ctx, symbol, interval, onCandle)
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

// ─── Helpers ─────────────────────────────────────────────────────────────────

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
