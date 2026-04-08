package bybit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// fetchCoinBalance запрашивает фактический баланс монеты через GET /v5/account/wallet-balance.
// Возвращает walletBalance для указанной монеты.
func fetchCoinBalance(ctx context.Context, httpClient *http.Client, baseURL, apiKey, secret, coin string, log *zap.Logger) (float64, error) {
	query := fmt.Sprintf("accountType=UNIFIED&coin=%s", coin)

	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	recvWindow := "5000"
	signPayload := timestamp + apiKey + recvWindow + query
	signature := signBybit(secret, signPayload)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v5/account/wallet-balance?"+query, nil)
	if err != nil {
		return 0, fmt.Errorf("fetchCoinBalance: create request: %w", err)
	}
	req.Header.Set("X-BAPI-API-KEY", apiKey)
	req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
	req.Header.Set("X-BAPI-SIGN", signature)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fetchCoinBalance: request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	log.Debug("bybit: fetchCoinBalance response",
		zap.String("coin", coin),
		zap.ByteString("body", body),
	)

	var raw struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				Coin []struct {
					Coin          string `json:"coin"`
					WalletBalance string `json:"walletBalance"`
				} `json:"coin"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0, fmt.Errorf("fetchCoinBalance: unmarshal: %w", err)
	}
	if raw.RetCode != 0 {
		return 0, fmt.Errorf("fetchCoinBalance: api error %d %s", raw.RetCode, raw.RetMsg)
	}

	if len(raw.Result.List) == 0 || len(raw.Result.List[0].Coin) == 0 {
		return 0, fmt.Errorf("fetchCoinBalance: coin %s not found in response", coin)
	}

	for _, c := range raw.Result.List[0].Coin {
		if strings.EqualFold(c.Coin, coin) {
			bal, err := strconv.ParseFloat(c.WalletBalance, 64)
			if err != nil {
				return 0, fmt.Errorf("fetchCoinBalance: parse balance: %w", err)
			}
			log.Info("bybit: fetched coin balance",
				zap.String("coin", coin),
				zap.Float64("wallet_balance", bal),
			)
			return bal, nil
		}
	}

	return 0, fmt.Errorf("fetchCoinBalance: coin %s not in list", coin)
}

// bybitBaseAsset извлекает базовый актив из символа (например, "SOLUSDT" → "SOL").
func bybitBaseAsset(symbol string) string {
	for _, quote := range []string{"USDT", "USDC", "BTC", "ETH"} {
		if len(symbol) > len(quote) && strings.HasSuffix(symbol, quote) {
			return symbol[:len(symbol)-len(quote)]
		}
	}
	return symbol
}

// fetchNetQty запрашивает /v5/execution/list для указанного orderId и возвращает
// чистое количество базового актива после вычета комиссии, уплаченной в базовой валюте.
// Повторяет запрос до maxAttempts раз с паузой 300 мс (исполнения могут появляться с задержкой).
// При неудаче возвращает fallback.
func fetchNetQty(ctx context.Context, httpClient *http.Client, baseURL, apiKey, secret, symbol, orderID string, fallback float64, log *zap.Logger) float64 {
	query := fmt.Sprintf("category=spot&orderId=%s&symbol=%s", orderID, symbol)

	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(300 * time.Millisecond):
			case <-ctx.Done():
				log.Warn("bybit: fetchNetQty: context cancelled", zap.String("order_id", orderID))
				return fallback
			}
		}

		timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
		recvWindow := "5000"
		signPayload := timestamp + apiKey + recvWindow + query
		signature := signBybit(secret, signPayload)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v5/execution/list?"+query, nil)
		if err != nil {
			log.Warn("bybit: fetchNetQty: failed to create request", zap.Error(err))
			return fallback
		}
		req.Header.Set("X-BAPI-API-KEY", apiKey)
		req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
		req.Header.Set("X-BAPI-SIGN", signature)
		req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)

		resp, err := httpClient.Do(req)
		if err != nil {
			log.Warn("bybit: fetchNetQty: request failed",
				zap.Error(err),
				zap.Int("attempt", attempt+1),
			)
			return fallback
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		log.Debug("bybit: fetchNetQty: raw response",
			zap.String("order_id", orderID),
			zap.Int("attempt", attempt+1),
			zap.ByteString("body", body),
		)

		var raw struct {
			RetCode int    `json:"retCode"`
			RetMsg  string `json:"retMsg"`
			Result  struct {
				List []struct {
					ExecQty     string `json:"execQty"`
					ExecFee     string `json:"execFee"`
					FeeCurrency string `json:"feeCurrency"`
				} `json:"list"`
			} `json:"result"`
		}

		if err := json.Unmarshal(body, &raw); err != nil {
			log.Warn("bybit: fetchNetQty: unmarshal failed",
				zap.Error(err),
				zap.ByteString("body", body),
			)
			return fallback
		}

		if raw.RetCode != 0 {
			log.Warn("bybit: fetchNetQty: api error",
				zap.Int("code", raw.RetCode),
				zap.String("msg", raw.RetMsg),
			)
			return fallback
		}

		if len(raw.Result.List) == 0 {
			log.Debug("bybit: fetchNetQty: execution list empty, retrying",
				zap.String("order_id", orderID),
				zap.Int("attempt", attempt+1),
			)
			continue
		}

		base := bybitBaseAsset(symbol)
		totalExecQty := 0.0
		totalFee := 0.0
		for _, exec := range raw.Result.List {
			q, _ := strconv.ParseFloat(exec.ExecQty, 64)
			totalExecQty += q
			if strings.EqualFold(exec.FeeCurrency, base) {
				f, _ := strconv.ParseFloat(exec.ExecFee, 64)
				totalFee += f
			}
		}

		netQty := totalExecQty - totalFee
		log.Info("bybit: fetchNetQty: computed from executions",
			zap.String("order_id", orderID),
			zap.String("symbol", symbol),
			zap.String("base_asset", base),
			zap.Float64("exec_qty", totalExecQty),
			zap.Float64("fee", totalFee),
			zap.Float64("net_qty", netQty),
		)
		return netQty
	}

	log.Warn("bybit: fetchNetQty: exhausted retries, using fallback",
		zap.String("order_id", orderID),
		zap.Float64("fallback", fallback),
	)
	return fallback
}
