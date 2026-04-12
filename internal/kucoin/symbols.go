package kucoin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/osman/bot-traider/internal/blacklist"
)

// kuSymbolInfo — описание торговой пары KuCoin.
type kuSymbolInfo struct {
	Symbol        string `json:"symbol"`
	QuoteCurrency string `json:"quoteCurrency"`
	EnableTrading bool   `json:"enableTrading"`
	IsMarginEnabled bool `json:"isMarginEnabled"`
}

// fetchSymbolsByQuote возвращает список активных торговых пар с заданной котировочной валютой.
func fetchSymbolsByQuote(ctx context.Context, restURL, quote string) ([]string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, restURL+"/api/v1/symbols", nil)
	if err != nil {
		return nil, fmt.Errorf("kucoin fetchSymbols: build request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kucoin fetchSymbols: do request: %w", err)
	}
	defer resp.Body.Close()

	var raw struct {
		Code string         `json:"code"`
		Data []kuSymbolInfo `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("kucoin fetchSymbols: decode: %w", err)
	}
	if raw.Code != "200000" {
		return nil, fmt.Errorf("kucoin fetchSymbols: api error code %s", raw.Code)
	}

	var result []string
	for _, s := range raw.Data {
		if s.QuoteCurrency == quote && s.EnableTrading {
			// Конвертируем в внутренний формат: "BTC-USDT" → "BTCUSDT"
			internalSymbol := fromKucoinSymbol(s.Symbol)
			result = append(result, internalSymbol)
		}
	}
	return result, nil
}

// SymbolWatcher периодически обновляет список символов и вызывает onChange при изменениях.
type SymbolWatcher struct {
	logger   *zap.Logger
	interval time.Duration
	fetchFn  func(ctx context.Context) ([]string, error)
	current  []string
	mu       sync.RWMutex
	onChange func(added []string, removed []string, all []string)
}

// NewSymbolWatcher создаёт новый SymbolWatcher.
func NewSymbolWatcher(
	interval time.Duration,
	fetchFn func(ctx context.Context) ([]string, error),
	log *zap.Logger,
	onChange func(added, removed, all []string),
) *SymbolWatcher {
	return &SymbolWatcher{
		logger:   log,
		interval: interval,
		fetchFn:  fetchFn,
		onChange: onChange,
	}
}

// Run запускает watcher: первый fetch сразу, затем по тикеру.
func (w *SymbolWatcher) Run(ctx context.Context) error {
	if err := w.refresh(ctx); err != nil {
		w.logger.Error("kucoin: initial symbol fetch failed", zap.Error(err))
	}

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := w.refresh(ctx); err != nil {
				w.logger.Error("kucoin: symbol refresh failed", zap.Error(err))
			}
		}
	}
}

// refresh получает символы и сравнивает с текущим списком.
func (w *SymbolWatcher) refresh(ctx context.Context) error {
	symbols, err := w.fetchFn(ctx)
	if err != nil {
		return err
	}
	symbols = blacklist.FilterSymbols(symbols)

	w.mu.Lock()
	old := w.current
	w.current = symbols
	w.mu.Unlock()

	added, removed := compareSymbols(old, symbols)

	if len(added) > 0 || len(removed) > 0 {
		fields := []zap.Field{
			zap.Int("total", len(symbols)),
			zap.Int("added", len(added)),
			zap.Int("removed", len(removed)),
		}
		if len(added) > 0 {
			fields = append(fields, zap.Strings("added_list", added))
		}
		if len(removed) > 0 {
			fields = append(fields, zap.Strings("removed_list", removed))
		}
		w.logger.Info("kucoin: symbols updated", fields...)
		w.onChange(added, removed, symbols)
	} else {
		w.logger.Info("kucoin: symbols unchanged", zap.Int("total", len(symbols)))
	}

	return nil
}

// compareSymbols возвращает добавленные и удалённые символы через map для O(n) сравнения.
func compareSymbols(old, newSymbols []string) (added, removed []string) {
	oldSet := make(map[string]struct{}, len(old))
	for _, s := range old {
		oldSet[s] = struct{}{}
	}

	newSet := make(map[string]struct{}, len(newSymbols))
	for _, s := range newSymbols {
		newSet[s] = struct{}{}
		if _, ok := oldSet[s]; !ok {
			added = append(added, s)
		}
	}

	for _, s := range old {
		if _, ok := newSet[s]; !ok {
			removed = append(removed, s)
		}
	}

	return added, removed
}
