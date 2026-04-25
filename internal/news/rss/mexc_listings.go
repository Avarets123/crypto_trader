package rss

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const mexcExchangeInfoURL = "https://api.mexc.com/api/v3/exchangeInfo"

type mexcExchangeInfoResponse struct {
	Symbols []struct {
		Symbol   string `json:"symbol"`
		Status   string `json:"status"` // "1" = trading
		BaseAsset  string `json:"baseAsset"`
		QuoteAsset string `json:"quoteAsset"`
		FullName   string `json:"fullName"`
	} `json:"symbols"`
}

// FetchMEXCListings возвращает все активные USDT-пары MEXC.
// Дата листинга в API отсутствует — используется GUID-дедупликация в БД.
func FetchMEXCListings(ctx context.Context) ([]Item, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mexcExchangeInfoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("mexc listings: create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mexc listings: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mexc listings: unexpected status %d", resp.StatusCode)
	}

	var result mexcExchangeInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("mexc listings: decode: %w", err)
	}

	var items []Item
	for _, s := range result.Symbols {
		if s.Status != "1" || s.QuoteAsset != "USDT" {
			continue
		}
		link := fmt.Sprintf("https://www.mexc.com/exchange/%s_%s", s.BaseAsset, s.QuoteAsset)
		title := fmt.Sprintf("MEXC listing: %s", s.Symbol)
		if s.FullName != "" {
			title = fmt.Sprintf("MEXC listing: %s (%s)", s.Symbol, s.FullName)
		}
		items = append(items, Item{
			Source:  "mexc",
			GUID:    fmt.Sprintf("mexc-symbol-%s", s.Symbol),
			Title:   title,
			Link:    link,
			Summary: fmt.Sprintf("MEXC listed %s/%s", s.BaseAsset, s.QuoteAsset),
		})
	}
	return items, nil
}
