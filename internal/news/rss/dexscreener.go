package rss

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const dexscreenerProfilesURL = "https://api.dexscreener.com/token-profiles/latest/v1"

// skipDexChains — шумные цепочки с преимущественно pump.fun мем-токенами.
var skipDexChains = map[string]bool{
	"solana": true,
}

type dexscreenerProfilesResponse []struct {
	URL          string `json:"url"`
	ChainID      string `json:"chainId"`
	TokenAddress string `json:"tokenAddress"`
	Description  string `json:"description"`
	UpdatedAt    string `json:"updatedAt"` // RFC3339
	Links        []struct {
		Type  string `json:"type"`
		Label string `json:"label"`
		URL   string `json:"url"`
	} `json:"links"`
}

// FetchDexScreenerListings возвращает последние новые токен-профили с DEX (кроме Solana).
func FetchDexScreenerListings(ctx context.Context) ([]Item, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dexscreenerProfilesURL, nil)
	if err != nil {
		return nil, fmt.Errorf("dexscreener: create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dexscreener: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dexscreener: unexpected status %d", resp.StatusCode)
	}

	var profiles dexscreenerProfilesResponse
	if err := json.NewDecoder(resp.Body).Decode(&profiles); err != nil {
		return nil, fmt.Errorf("dexscreener: decode: %w", err)
	}

	var items []Item
	for _, p := range profiles {
		if skipDexChains[p.ChainID] {
			continue
		}

		var publishedAt *time.Time
		if p.UpdatedAt != "" {
			if t, err := time.Parse(time.RFC3339Nano, p.UpdatedAt); err == nil {
				publishedAt = &t
			}
		}

		summary := p.Description
		if summary == "" {
			summary = fmt.Sprintf("New token on %s DEX", p.ChainID)
		}

		items = append(items, Item{
			Source:      "dexscreener",
			GUID:        fmt.Sprintf("dex-%s-%s", p.ChainID, p.TokenAddress),
			Title:       fmt.Sprintf("New DEX listing [%s]: %s", p.ChainID, p.TokenAddress),
			Link:        p.URL,
			Summary:     buildSummary(summary),
			PublishedAt: publishedAt,
			IsListing:   true,
		})
	}
	return items, nil
}
