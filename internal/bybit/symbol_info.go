package bybit

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

// knownStepSizes — захардкоженные qtyStep для популярных USDT-пар Bybit spot.
// Используются как начальное значение кеша и как fallback если REST-запрос не удался.
var knownStepSizes = map[string]float64{
	"BTCUSDT":  0.000001,
	"ETHUSDT":  0.0001,
	"SOLUSDT":  0.01,
	"XRPUSDT":  0.1,
	"ADAUSDT":  1,
	"DOGEUSDT": 1,
	"LTCUSDT":  0.001,
}

var (
	stepSizeCache   = initStepSizeCache()
	stepSizeMu      sync.RWMutex
	stepSizeBaseURL = "https://api.bybit.com"
)

func initStepSizeCache() map[string]float64 {
	m := make(map[string]float64, len(knownStepSizes))
	for k, v := range knownStepSizes {
		m[k] = v
	}
	return m
}

// SetStepSizeBaseURL устанавливает базовый URL для запросов instruments-info (для testnet).
func SetStepSizeBaseURL(baseURL string) {
	stepSizeMu.Lock()
	stepSizeBaseURL = baseURL
	stepSizeMu.Unlock()
}

// getStepSize возвращает qtyStep (LOT_SIZE) для символа (из кэша или с биржи).
// При ошибке возвращает 0 — вызывающий должен использовать fallback.
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

	url := fmt.Sprintf("%s/v5/market/instruments-info?category=spot&symbol=%s", baseURL, symbol)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.Warn("bybit: failed to build instruments-info request",
			zap.String("symbol", symbol),
			zap.Error(err),
		)
		return 0
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Warn("bybit: failed to fetch instruments-info",
			zap.String("symbol", symbol),
			zap.Error(err),
		)
		return 0
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Warn("bybit: instruments-info returned non-200",
			zap.String("symbol", symbol),
			zap.Int("status", resp.StatusCode),
			zap.ByteString("body", body),
		)
		return 0
	}

	body, _ := io.ReadAll(resp.Body)

	var info struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				Symbol        string `json:"symbol"`
				LotSizeFilter struct {
					QtyStep string `json:"qtyStep"`
				} `json:"lotSizeFilter"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		log.Warn("bybit: failed to unmarshal instruments-info",
			zap.String("symbol", symbol),
			zap.Error(err),
		)
		return 0
	}
	if info.RetCode != 0 {
		log.Warn("bybit: instruments-info api error",
			zap.String("symbol", symbol),
			zap.Int("ret_code", info.RetCode),
			zap.String("ret_msg", info.RetMsg),
		)
		return 0
	}

	for _, sym := range info.Result.List {
		if sym.Symbol == symbol {
			step, err := strconv.ParseFloat(sym.LotSizeFilter.QtyStep, 64)
			if err != nil || step <= 0 {
				log.Warn("bybit: invalid qtyStep",
					zap.String("symbol", symbol),
					zap.String("qty_step_raw", sym.LotSizeFilter.QtyStep),
				)
				return 0
			}
			log.Info("bybit: qtyStep fetched",
				zap.String("symbol", symbol),
				zap.Float64("qty_step", step),
			)
			stepSizeMu.Lock()
			stepSizeCache[symbol] = step
			stepSizeMu.Unlock()
			return step
		}
	}

	log.Warn("bybit: qtyStep not found for symbol", zap.String("symbol", symbol))
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
