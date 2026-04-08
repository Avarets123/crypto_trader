package okx

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"math"
	"strconv"
	"time"
)

// formatQty форматирует количество:
// qty >= 1 → целое число, qty < 1 → 6 знаков после запятой.
func formatQty(qty float64) string {
	if qty >= 1 {
		return strconv.FormatFloat(math.Floor(qty), 'f', 0, 64)
	}
	return strconv.FormatFloat(math.Floor(qty*1_000_000)/1_000_000, 'f', 6, 64)
}

// signOKX вычисляет Base64(HMAC-SHA256(secret, timestamp+method+path+body)).
func signOKX(secret, timestamp, method, path, body string) string {
	preHash := timestamp + method + path + body
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(preHash))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// okxTimestamp возвращает текущее время в формате ISO 8601 с миллисекундами.
func okxTimestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
