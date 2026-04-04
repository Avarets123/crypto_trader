package comparator

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/osman/bot-traider/internal/ticker"
)

func newTestComparator(threshold float64) (*PriceComparator, *observer.ObservedLogs) {
	core, logs := observer.New(zap.WarnLevel)
	log := zap.New(core)
	return New(context.Background(), threshold, nil, log), logs
}

func TestCheckSpread_TriggersWhenAboveThreshold(t *testing.T) {
	c, logs := newTestComparator(1.0)

	c.Update(ticker.Ticker{Symbol: "BTCUSDT", Exchange: "binance", Price: "65000"})
	c.Update(ticker.Ticker{Symbol: "BTCUSDT", Exchange: "bybit", Price: "64300"})

	if logs.Len() == 0 {
		t.Fatal("expected warn log, got none")
	}
	entry := logs.All()[0]
	if entry.Message != "spread opened" {
		t.Fatalf("unexpected message: %s", entry.Message)
	}
}

func TestCheckSpread_SilentWhenBelowThreshold(t *testing.T) {
	c, logs := newTestComparator(1.0)

	c.Update(ticker.Ticker{Symbol: "BTCUSDT", Exchange: "binance", Price: "65000"})
	c.Update(ticker.Ticker{Symbol: "BTCUSDT", Exchange: "bybit", Price: "64995"})

	if logs.Len() != 0 {
		t.Fatalf("expected no logs, got %d", logs.Len())
	}
}

func TestCheckSpread_NoDuplicatePairs(t *testing.T) {
	c, logs := newTestComparator(1.0)

	c.Update(ticker.Ticker{Symbol: "BTCUSDT", Exchange: "binance", Price: "65000"})
	c.Update(ticker.Ticker{Symbol: "BTCUSDT", Exchange: "bybit", Price: "64300"})
	c.Update(ticker.Ticker{Symbol: "BTCUSDT", Exchange: "gateio", Price: "65200"})

	// 3 биржи → 3 уникальных пары максимум
	if logs.Len() > 3 {
		t.Fatalf("too many log entries: %d (expected <= 3)", logs.Len())
	}
}

func TestUpdate_IgnoresInvalidPrice(t *testing.T) {
	c, logs := newTestComparator(1.0)

	c.Update(ticker.Ticker{Symbol: "BTCUSDT", Exchange: "binance", Price: "invalid"})
	c.Update(ticker.Ticker{Symbol: "BTCUSDT", Exchange: "bybit", Price: "64300"})

	if logs.Len() != 0 {
		t.Fatalf("expected no logs after invalid price, got %d", logs.Len())
	}
}

func TestUpdate_DifferentSymbolsIsolated(t *testing.T) {
	c, logs := newTestComparator(1.0)

	// BTCUSDT спред большой, ETHUSDT — маленький
	c.Update(ticker.Ticker{Symbol: "BTCUSDT", Exchange: "binance", Price: "65000"})
	c.Update(ticker.Ticker{Symbol: "BTCUSDT", Exchange: "bybit", Price: "64300"})
	c.Update(ticker.Ticker{Symbol: "ETHUSDT", Exchange: "binance", Price: "3000"})
	c.Update(ticker.Ticker{Symbol: "ETHUSDT", Exchange: "bybit", Price: "3001"})

	// только BTCUSDT должен дать лог
	for _, entry := range logs.All() {
		if entry.ContextMap()["symbol"] == "ETHUSDT" {
			t.Fatal("ETHUSDT should not trigger spread log")
		}
	}
	if logs.Len() == 0 {
		t.Fatal("expected BTCUSDT spread log")
	}
}
