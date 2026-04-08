package binance

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

type exchangeInfo struct {
	Symbols []symbolInfo `json:"symbols"`
}

type symbolInfo struct {
	Symbol     string `json:"symbol"`
	Status     string `json:"status"`
	QuoteAsset string `json:"quoteAsset"`
}

func fetchSymbolsByQuote(ctx context.Context, restURL, quote string) ([]string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, restURL+"/api/v3/exchangeInfo", nil)
	if err != nil {
		return nil, fmt.Errorf("fetchSymbolsByQuote(%s): build request: %w", quote, err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetchSymbolsByQuote(%s): do request: %w", quote, err)
	}
	defer resp.Body.Close()

	var info exchangeInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("fetchSymbolsByQuote(%s): decode: %w", quote, err)
	}

	var result []string
	for _, s := range info.Symbols {
		if s.QuoteAsset == quote && s.Status == "TRADING" {
			result = append(result, s.Symbol)
		}
	}
	return result, nil
}

// SymbolWatcher периодически обновляет список символов и вызывает onChange при изменениях.
type SymbolWatcher struct {
	logger   *zap.Logger
	interval time.Duration
	restURL  string
	pinned   []string // если непустой — используются вместо REST-запроса
	current  []string
	mu       sync.RWMutex
	onChange func(added []string, removed []string, all []string)
}

// NewSymbolWatcher создаёт новый SymbolWatcher.
// pinned — фиксированный список символов; если nil/пустой — символы берутся с биржи.
func NewSymbolWatcher(
	interval time.Duration,
	restURL string,
	pinned []string,
	log *zap.Logger,
	onChange func(added, removed, all []string),
) *SymbolWatcher {
	return &SymbolWatcher{
		logger:   log,
		interval: interval,
		restURL:  restURL,
		pinned:   pinned,
		onChange: onChange,
	}
}

// Run запускает watcher: первый fetch сразу, затем по тикеру (только при динамическом режиме).
func (w *SymbolWatcher) Run(ctx context.Context) error {
	if err := w.refresh(ctx); err != nil {
		w.logger.Error("initial symbol fetch failed", zap.Error(err))
	}

	if len(w.pinned) > 0 {
		// Список зафиксирован — периодическое обновление не нужно.
		<-ctx.Done()
		return ctx.Err()
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
	var symbols []string
	if len(w.pinned) > 0 {
		symbols = blacklist.FilterSymbols(w.pinned)
		w.logger.Info("using pinned symbols", zap.Int("count", len(symbols)))
	} else {
		var err error
		symbols, err = fetchSymbolsByQuote(ctx, w.restURL, "USDT")
		if err != nil {
			return err
		}
		symbols = blacklist.FilterSymbols(symbols)
	}

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
