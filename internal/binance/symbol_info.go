package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"go.uber.org/zap"
)

var (
	stepSizeCache   = make(map[string]float64)
	stepSizeMu      sync.RWMutex
	stepSizeBaseURL = "https://api.binance.com"
)

// SetStepSizeBaseURL устанавливает базовый URL для запросов exchange info (для testnet).
func SetStepSizeBaseURL(baseURL string) {
	stepSizeMu.Lock()
	stepSizeBaseURL = baseURL
	stepSizeMu.Unlock()
}

// getStepSize возвращает LOT_SIZE stepSize для символа (из кэша или с биржи).
// При ошибке возвращает 0 (означает "неизвестно"), вызывающий должен использовать fallback.
func getStepSize(ctx context.Context, symbol string, log *zap.Logger) float64 {
	stepSizeMu.RLock()
	if s, ok := stepSizeCache[symbol]; ok {
		stepSizeMu.RUnlock()
		return s
	}
	stepSizeMu.RUnlock()

	stepSizeMu.RLock()
	baseURL := stepSizeBaseURL
	stepSizeMu.RUnlock()

	url := fmt.Sprintf("%s/api/v3/exchangeInfo?symbol=%s", baseURL, symbol)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.Warn("binance: failed to build exchangeInfo request",
			zap.String("symbol", symbol),
			zap.Error(err),
		)
		return 0
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Warn("binance: failed to fetch exchangeInfo",
			zap.String("symbol", symbol),
			zap.Error(err),
		)
		return 0
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Warn("binance: exchangeInfo returned non-200",
			zap.String("symbol", symbol),
			zap.Int("status", resp.StatusCode),
			zap.ByteString("body", body),
		)
		return 0
	}

	body, _ := io.ReadAll(resp.Body)

	var info struct {
		Symbols []struct {
			Symbol  string `json:"symbol"`
			Filters []struct {
				FilterType string `json:"filterType"`
				StepSize   string `json:"stepSize"`
			} `json:"filters"`
		} `json:"symbols"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		log.Warn("binance: failed to unmarshal exchangeInfo",
			zap.String("symbol", symbol),
			zap.Error(err),
		)
		return 0
	}

	for _, sym := range info.Symbols {
		if sym.Symbol == symbol {
			for _, f := range sym.Filters {
				if f.FilterType == "LOT_SIZE" {
					step, err := strconv.ParseFloat(f.StepSize, 64)
					if err != nil || step <= 0 {
						log.Warn("binance: invalid LOT_SIZE stepSize",
							zap.String("symbol", symbol),
							zap.String("step_size_raw", f.StepSize),
						)
						return 0
					}
					log.Info("binance: LOT_SIZE stepSize fetched",
						zap.String("symbol", symbol),
						zap.Float64("step_size", step),
					)
					stepSizeMu.Lock()
					stepSizeCache[symbol] = step
					stepSizeMu.Unlock()
					return step
				}
			}
		}
	}

	log.Warn("binance: LOT_SIZE filter not found for symbol", zap.String("symbol", symbol))
	return 0
}

// stepSizeDecimals возвращает количество знаков после запятой для stepSize.
// Например: stepSize=0.01 → 2, stepSize=1 → 0, stepSize=0.001 → 3.
func stepSizeDecimals(stepSize float64) int {
	s := strconv.FormatFloat(stepSize, 'f', -1, 64)
	s = strings.TrimRight(s, "0")
	i := strings.Index(s, ".")
	if i < 0 {
		return 0
	}
	return len(s) - i - 1
}
