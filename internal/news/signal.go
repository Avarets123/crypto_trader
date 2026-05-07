package news

import (
	"strings"
	"time"
)

// Direction — направление сигнала из новости.
type Direction string

const (
	DirectionUp   Direction = "UP"
	DirectionDown Direction = "DOWN"
)

// Confidence — уровень уверенности Ollama в сигнале.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

// TickerSignal — один тикер из распарсенного Ollama-сигнала.
type TickerSignal struct {
	Symbol     string     // "BTC", "ETH" — без USDT-суффикса
	Direction  Direction  // UP / DOWN
	Confidence Confidence // high / medium / low
}

// NewsSignal — событие, эмитится news.Service в зарегистрированный listener
// для каждой новости с непустым набором тикеров после Ollama-классификации.
type NewsSignal struct {
	Source      string
	GUID        string
	Title       string
	Link        string
	PublishedAt *time.Time
	Tickers     []TickerSignal
	RawSignal   string // оригинальная строка от Ollama для диагностики
}

// HasHighConfidence возвращает true если хотя бы один тикер имеет high confidence.
func (s NewsSignal) HasHighConfidence() bool {
	for _, t := range s.Tickers {
		if t.Confidence == ConfidenceHigh {
			return true
		}
	}
	return false
}

// HighConfidenceTickers фильтрует только high-confidence тикеры.
func (s NewsSignal) HighConfidenceTickers() []TickerSignal {
	var out []TickerSignal
	for _, t := range s.Tickers {
		if t.Confidence == ConfidenceHigh {
			out = append(out, t)
		}
	}
	return out
}

// ParseSignal разбирает строку Ollama в []TickerSignal.
//
// Поддерживаемые форматы:
//   - "UP:BTC:high,ETH:medium"
//   - "DOWN:SOL:low"
//   - "UP:BTC:high|DOWN:ETH:medium"
//   - "NONE" / "" → nil
//
// Обратная совместимость со старым форматом без confidence:
//   - "UP:BTC,ETH" → confidence=medium по умолчанию.
//
// Невалидные сегменты тихо пропускаются.
func ParseSignal(raw string) []TickerSignal {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "NONE") {
		return nil
	}

	var result []TickerSignal
	for _, part := range strings.Split(raw, "|") {
		part = strings.TrimSpace(part)
		var dir Direction
		switch {
		case strings.HasPrefix(part, "UP:"):
			dir = DirectionUp
			part = strings.TrimPrefix(part, "UP:")
		case strings.HasPrefix(part, "DOWN:"):
			dir = DirectionDown
			part = strings.TrimPrefix(part, "DOWN:")
		default:
			continue
		}

		for _, tickerSpec := range strings.Split(part, ",") {
			tickerSpec = strings.TrimSpace(tickerSpec)
			if tickerSpec == "" {
				continue
			}
			symbol, conf := splitTickerConfidence(tickerSpec)
			if symbol == "" {
				continue
			}
			result = append(result, TickerSignal{
				Symbol:     strings.ToUpper(symbol),
				Direction:  dir,
				Confidence: conf,
			})
		}
	}
	return result
}

// splitTickerConfidence парсит "BTC:high" → ("BTC", "high").
// Для старого формата без confidence возвращает ("BTC", ConfidenceMedium).
func splitTickerConfidence(spec string) (string, Confidence) {
	parts := strings.Split(spec, ":")
	symbol := strings.TrimSpace(parts[0])
	if len(parts) == 1 {
		return symbol, ConfidenceMedium
	}
	confRaw := strings.ToLower(strings.TrimSpace(parts[1]))
	switch confRaw {
	case "high":
		return symbol, ConfidenceHigh
	case "medium", "med":
		return symbol, ConfidenceMedium
	case "low":
		return symbol, ConfidenceLow
	default:
		return symbol, ConfidenceMedium
	}
}
