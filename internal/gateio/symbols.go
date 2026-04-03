package gateio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
)

const currencyPairsURL = "https://api.gateio.ws/api/v4/spot/currency_pairs"

type currencyPair struct {
	ID          string `json:"id"`
	Quote       string `json:"quote"`
	TradeStatus string `json:"trade_status"`
}

// FetchUSDTSymbols получает актуальный список USDT-пар со статусом tradable.
// Возвращает символы в формате BTC_USDT (native Gate.io).
func FetchUSDTSymbols(ctx context.Context, log *zap.Logger) ([]string, error) {
	return fetchSymbolsByQuote(ctx, "USDT")
}

// FetchBTCSymbols получает актуальный список BTC-пар со статусом tradable.
// Возвращает символы в формате ETH_BTC (native Gate.io).
func FetchBTCSymbols(ctx context.Context, log *zap.Logger) ([]string, error) {
	return fetchSymbolsByQuote(ctx, "BTC")
}

func fetchSymbolsByQuote(ctx context.Context, quote string) ([]string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, currencyPairsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("fetchSymbolsByQuote(%s): build request: %w", quote, err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetchSymbolsByQuote(%s): do request: %w", quote, err)
	}
	defer resp.Body.Close()

	var pairs []currencyPair
	if err := json.NewDecoder(resp.Body).Decode(&pairs); err != nil {
		return nil, fmt.Errorf("fetchSymbolsByQuote(%s): decode: %w", quote, err)
	}

	var result []string
	for _, p := range pairs {
		if p.Quote == quote && p.TradeStatus == "tradable" {
			result = append(result, p.ID)
		}
	}
	return result, nil
}

// SymbolWatcher периодически обновляет список символов и вызывает onChange при изменениях.
type SymbolWatcher struct {
	logger   *zap.Logger
	interval time.Duration
	current  []string
	mu       sync.RWMutex
	onChange func(added, removed, all []string)
}

// NewSymbolWatcher создаёт новый SymbolWatcher.
func NewSymbolWatcher(
	interval time.Duration,
	log *zap.Logger,
	onChange func(added, removed, all []string),
) *SymbolWatcher {
	return &SymbolWatcher{
		logger:   log,
		interval: interval,
		onChange: onChange,
	}
}

// Run запускает watcher: первый fetch сразу, затем по тикеру. Блокирует до ctx.Done().
func (w *SymbolWatcher) Run(ctx context.Context) error {
	if err := w.refresh(ctx); err != nil {
		w.logger.Error("initial symbol fetch failed", zap.Error(err))
	}

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := w.refresh(ctx); err != nil {
				w.logger.Error("symbol refresh failed", zap.Error(err))
			}
		}
	}
}

// refresh получает символы и сравнивает с текущим списком.
func (w *SymbolWatcher) refresh(ctx context.Context) error {
	usdt, err := FetchUSDTSymbols(ctx, w.logger)
	if err != nil {
		return err
	}
	btc, err := FetchBTCSymbols(ctx, w.logger)
	if err != nil {
		return err
	}
	symbols := mergeSymbols(usdt, btc)

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
		w.logger.Info("symbols updated", fields...)
		w.onChange(added, removed, symbols)
	} else {
		w.logger.Info("symbols unchanged", zap.Int("total", len(symbols)))
	}

	return nil
}

// mergeSymbols объединяет несколько срезов символов без дубликатов.
func mergeSymbols(slices ...[]string) []string {
	seen := make(map[string]struct{})
	var result []string
	for _, s := range slices {
		for _, sym := range s {
			if _, ok := seen[sym]; !ok {
				seen[sym] = struct{}{}
				result = append(result, sym)
			}
		}
	}
	return result
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
