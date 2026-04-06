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
func formatQty(qty float64) string {
	if qty >= 1 {
		return strconv.FormatFloat(math.Floor(qty), 'f', 0, 64)
	}
	return strconv.FormatFloat(math.Floor(qty*1_000_000)/1_000_000, 'f', 6, 64)
}

// signREST добавляет timestamp, recvWindow и signature в url.Values (REST API).
func signREST(secret string, params url.Values) {
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	params.Set("recvWindow", "5000")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(params.Encode()))
	params.Set("signature", hex.EncodeToString(mac.Sum(nil)))
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
