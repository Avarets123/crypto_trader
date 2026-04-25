package rss

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const coinbaseProductsURL = "https://api.exchange.coinbase.com/products"

type coinbaseProduct struct {
	ID            string `json:"id"`
	BaseCurrency  string `json:"base_currency"`
	QuoteCurrency string `json:"quote_currency"`
	Status        string `json:"status"`
}

// FetchCoinbaseListings возвращает все активные спот-пары Coinbase (USD/USDT/USDC).
// Дата листинга в API отсутствует — используется GUID-дедупликация в БД.
func FetchCoinbaseListings(ctx context.Context) ([]Item, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coinbaseProductsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("coinbase listings: create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("coinbase listings: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("coinbase listings: unexpected status %d", resp.StatusCode)
	}

	var products []coinbaseProduct
	if err := json.NewDecoder(resp.Body).Decode(&products); err != nil {
		return nil, fmt.Errorf("coinbase listings: decode: %w", err)
	}

	allowedQuotes := map[string]bool{"USD": true, "USDT": true, "USDC": true}
	var items []Item
	for _, p := range products {
		if p.Status != "online" || !allowedQuotes[p.QuoteCurrency] {
			continue
		}
		link := fmt.Sprintf("https://www.coinbase.com/en/advanced-trade/spot/%s-%s", p.BaseCurrency, p.QuoteCurrency)
		items = append(items, Item{
			Source:    "coinbase",
			GUID:      fmt.Sprintf("coinbase-product-%s", p.ID),
			Title:     fmt.Sprintf("Coinbase listing: %s", p.ID),
			Link:      link,
			Summary:   fmt.Sprintf("Coinbase listed %s/%s", p.BaseCurrency, p.QuoteCurrency),
			IsListing: true,
		})
	}
	return items, nil
}
