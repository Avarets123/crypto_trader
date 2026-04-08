package bybit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
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

// formatQtyWithStep форматирует количество с учётом qtyStep биржи.
// Округляет вниз до ближайшего кратного stepSize.
// Если qty после округления равен 0 (qty < stepSize), использует один шаг.
func formatQtyWithStep(qty, stepSize float64) string {
	if stepSize <= 0 {
		return formatQty(qty)
	}
	floored := math.Floor(qty/stepSize) * stepSize
	if floored <= 0 {
		floored = stepSize
	}
	decimals := stepSizeDecimals(stepSize)
	return strconv.FormatFloat(floored, 'f', decimals, 64)
}

// signBybit вычисляет HMAC-SHA256 подпись над payload.
func signBybit(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// quoteFromSymbol определяет котируемую валюту по суффиксу символа.
func quoteFromSymbol(symbol string) string {
	if strings.HasSuffix(symbol, "BTC") {
		return "BTC"
	}
	return "USDT"
}

// calcChangePct вычисляет процентное изменение цены относительно открытия.
func calcChangePct(open, last string) string {
	var o, l float64
	fmt.Sscanf(open, "%f", &o)
	fmt.Sscanf(last, "%f", &l)
	if o == 0 {
		return "0.00%"
	}
	pct := (l - o) / o * 100
	if pct >= 0 {
		return fmt.Sprintf("+%.2f%%", pct)
	}
	return fmt.Sprintf("%.2f%%", pct)
}
