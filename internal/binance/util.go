package binance

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"time"
)

// formatQty форматирует количество с учётом LOT_SIZE:
// qty >= 1 → целое число, qty < 1 → 6 знаков после запятой.
// Используется только если stepSize неизвестен.
func formatQty(qty float64) string {
	if qty >= 1 {
		return strconv.FormatFloat(math.Floor(qty), 'f', 0, 64)
	}
	return strconv.FormatFloat(math.Floor(qty*1_000_000)/1_000_000, 'f', 6, 64)
}

// formatQtyWithStep форматирует количество с учётом LOT_SIZE stepSize биржи.
// Округляет вниз до ближайшего кратного stepSize.
func formatQtyWithStep(qty, stepSize float64) string {
	if stepSize <= 0 {
		return formatQty(qty)
	}
	floored := math.Floor(qty/stepSize) * stepSize
	decimals := stepSizeDecimals(stepSize)
	return strconv.FormatFloat(floored, 'f', decimals, 64)
}

// signREST добавляет timestamp, recvWindow и signature в url.Values (REST API).
func signREST(secret string, params url.Values) {
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	params.Set("recvWindow", "5000")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(params.Encode()))
	params.Set("signature", hex.EncodeToString(mac.Sum(nil)))
}

// baseAsset извлекает базовый актив из символа (например, "JOEUSDT" → "JOE").
// Перебирает известные котировочные активы и отрезает суффикс.
func baseAsset(symbol string) string {
	for _, quote := range []string{"USDT", "USDC", "BUSD", "BTC", "ETH", "BNB"} {
		if len(symbol) > len(quote) && symbol[len(symbol)-len(quote):] == quote {
			return symbol[:len(symbol)-len(quote)]
		}
	}
	return symbol
}

// netQtyAfterFills вычисляет фактически полученное количество базового актива после BUY-ордера.
// Если комиссия списана в базовом активе (не в BNB/USDT), вычитает её из executedQty.
func netQtyAfterFills(symbol string, executedQty float64, fills []struct {
	Commission      string `json:"commission"`
	CommissionAsset string `json:"commissionAsset"`
}) float64 {
	base := baseAsset(symbol)
	net := executedQty
	for _, f := range fills {
		if f.CommissionAsset == base {
			c, _ := strconv.ParseFloat(f.Commission, 64)
			net -= c
		}
	}
	return net
}

// signWS добавляет timestamp, recvWindow, apiKey и signature в map параметров (WS API).
// Подпись вычисляется через HMAC-SHA256 от query-string представления параметров.
func signWS(apiKey, secret string, params map[string]interface{}) {
	params["timestamp"] = time.Now().UnixMilli()
	params["recvWindow"] = 5000
	params["apiKey"] = apiKey

	v := url.Values{}
	for k, val := range params {
		v.Set(k, fmt.Sprintf("%v", val))
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(v.Encode()))
	params["signature"] = hex.EncodeToString(mac.Sum(nil))
}
