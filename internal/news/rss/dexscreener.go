package rss

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const dexscreenerProfilesURL = "https://api.dexscreener.com/token-profiles/latest/v1"
const dexscreenerTokensURL = "https://api.dexscreener.com/tokens/v1/%s/%s"

// skipDexChains — шумные цепочки с преимущественно pump.fun мем-токенами.
var skipDexChains = map[string]bool{
	"solana": true,
}

type dexscreenerProfile struct {
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

type dexscreenerTokensResponse []struct {
	BaseToken struct {
		Name   string `json:"name"`
		Symbol string `json:"symbol"`
	} `json:"baseToken"`
}

// fetchDexTokenTitle запрашивает имя и символ токена и возвращает строку вида "SYMBOL — Name".
// При ошибке возвращает пустую строку.
func fetchDexTokenTitle(ctx context.Context, chainID, address string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	url := fmt.Sprintf(dexscreenerTokensURL, chainID, address)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var result dexscreenerTokensResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || len(result) == 0 {
		return ""
	}

	symbol := result[0].BaseToken.Symbol
	name := result[0].BaseToken.Name
	if symbol == "" {
		return ""
	}
	if name != "" && name != symbol {
		return fmt.Sprintf("%s — %s", symbol, name)
	}
	return symbol
}

// FetchDexScreenerListings возвращает последние новые токен-профили с DEX (кроме Solana).
func FetchDexScreenerListings(ctx context.Context) ([]Item, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, dexscreenerProfilesURL, nil)
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

	var allProfiles []dexscreenerProfile
	if err := json.NewDecoder(resp.Body).Decode(&allProfiles); err != nil {
		return nil, fmt.Errorf("dexscreener: decode: %w", err)
	}

	// Фильтруем шумные цепочки
	var profiles []dexscreenerProfile
	for _, p := range allProfiles {
		if !skipDexChains[p.ChainID] {
			profiles = append(profiles, p)
		}
	}

	// Параллельно получаем имена токенов
	titles := make([]string, len(profiles))
	var wg sync.WaitGroup
	for i, p := range profiles {
		i, p := i, p
		wg.Add(1)
		go func() {
			defer wg.Done()
			titles[i] = fetchDexTokenTitle(ctx, p.ChainID, p.TokenAddress)
		}()
	}
	wg.Wait()

	var items []Item
	for i, p := range profiles {
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

		title := titles[i]
		if title == "" {
			title = "New DEX token listing"
		}

		items = append(items, Item{
			Source:      "dexscreener",
			GUID:        fmt.Sprintf("dex-%s-%s", p.ChainID, p.TokenAddress),
			Title:       title,
			Link:        p.URL,
			Summary:     buildSummary(summary),
			PublishedAt: publishedAt,
			IsListing:   true,
		})
	}
	return items, nil
}
