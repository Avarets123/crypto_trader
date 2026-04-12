package kucoin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// sign вычисляет HMAC-SHA256 и возвращает base64-строку.
// Используется для KC-API-SIGN и KC-API-PASSPHRASE (версия 2).
func sign(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// buildSignPayload формирует строку для подписи REST-запроса.
// Формат: timestamp + method + path (включая query string) + body.
func buildSignPayload(timestamp, method, path, body string) string {
	return timestamp + method + path + body
}

// nowTimestamp возвращает текущее время в миллисекундах как строку.
func nowTimestamp() string {
	return strconv.FormatInt(time.Now().UnixMilli(), 10)
}

// toKucoinSymbol конвертирует внутренний символ в формат KuCoin.
// Пример: "BTCUSDT" → "BTC-USDT"
func toKucoinSymbol(symbol string) string {
	for _, quote := range []string{"USDT", "USDC", "BTC", "ETH", "BNB"} {
		if strings.HasSuffix(symbol, quote) {
			base := symbol[:len(symbol)-len(quote)]
			return base + "-" + quote
		}
	}
	return symbol
}

// fromKucoinSymbol конвертирует символ KuCoin во внутренний формат.
// Пример: "BTC-USDT" → "BTCUSDT"
func fromKucoinSymbol(symbol string) string {
	return strings.ReplaceAll(symbol, "-", "")
}

// calcChangePctFromRate форматирует изменение цены из десятичного rate KuCoin.
// KuCoin присылает changeRate как десятичное (напр. -0.0545 = -5.45%).
func calcChangePctFromRate(changeRate string) string {
	var rate float64
	fmt.Sscanf(changeRate, "%f", &rate)
	pct := rate * 100
	if pct >= 0 {
		return fmt.Sprintf("+%.2f%%", pct)
	}
	return fmt.Sprintf("%.2f%%", pct)
}

// calcOpen вычисляет цену открытия из текущей цены и изменения.
// open = lastPrice - changePrice
func calcOpen(lastPrice, changePrice string) string {
	var last, change float64
	fmt.Sscanf(lastPrice, "%f", &last)
	fmt.Sscanf(changePrice, "%f", &change)
	return strconv.FormatFloat(last-change, 'f', -1, 64)
}

// formatQty форматирует количество для ордера.
// qty >= 1 → целое число, qty < 1 → 8 знаков после запятой.
func formatQty(qty float64) string {
	if qty >= 1 {
		return strconv.FormatFloat(math.Floor(qty), 'f', 0, 64)
	}
	return strconv.FormatFloat(math.Round(qty*1e8)/1e8, 'f', 8, 64)
}

// newConnectID генерирует уникальный ID для WS-подключения.
func newConnectID() string {
	b := make([]byte, 8)
	rand.Read(b) //nolint:errcheck
	return hex.EncodeToString(b)
}

// quoteFromKucoinSymbol извлекает котируемую валюту из символа KuCoin.
// Пример: "BTC-USDT" → "USDT"
func quoteFromKucoinSymbol(symbol string) string {
	parts := strings.SplitN(symbol, "-", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return "USDT"
}
