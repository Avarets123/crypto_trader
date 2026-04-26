package rss

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"
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

type dexscreenerPair struct {
	BaseToken struct {
		Name   string `json:"name"`
		Symbol string `json:"symbol"`
	} `json:"baseToken"`
	Liquidity struct {
		USD float64 `json:"usd"`
	} `json:"liquidity"`
	Volume struct {
		H1  float64 `json:"h1"`
		H24 float64 `json:"h24"`
	} `json:"volume"`
	Txns struct {
		H1 struct {
			Buys  int `json:"buys"`
			Sells int `json:"sells"`
		} `json:"h1"`
	} `json:"txns"`
	PriceChange struct {
		H1  float64 `json:"h1"`
		H24 float64 `json:"h24"`
	} `json:"priceChange"`
}

type dexTokenData struct {
	title         string
	liquidityUSD  float64
	volumeH1      float64
	txnsH1Buys    int
	priceChangeH1 float64
}

// fetchDexTokenData запрашивает данные пары (название, ликвидность, объём, сделки, изменение цены).
// Берётся первая пара с максимальной ликвидностью.
func fetchDexTokenData(ctx context.Context, chainID, address string) dexTokenData {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	url := fmt.Sprintf(dexscreenerTokensURL, chainID, address)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return dexTokenData{}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return dexTokenData{}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return dexTokenData{}
	}

	var pairs []dexscreenerPair
	if err := json.NewDecoder(resp.Body).Decode(&pairs); err != nil || len(pairs) == 0 {
		return dexTokenData{}
	}

	// Выбираем пару с максимальной ликвидностью.
	best := pairs[0]
	for _, p := range pairs[1:] {
		if p.Liquidity.USD > best.Liquidity.USD {
			best = p
		}
	}

	symbol := best.BaseToken.Symbol
	name := best.BaseToken.Name
	title := symbol
	if name != "" && name != symbol {
		title = fmt.Sprintf("%s — %s", symbol, name)
	}

	return dexTokenData{
		title:         title,
		liquidityUSD:  best.Liquidity.USD,
		volumeH1:      best.Volume.H1,
		txnsH1Buys:    best.Txns.H1.Buys,
		priceChangeH1: best.PriceChange.H1,
	}
}

// scoreDexToken считает скор токена (0–7) по ликвидности, объёму, сделкам и социальным ссылкам.
func scoreDexToken(p dexscreenerProfile, d dexTokenData) int {
	score := 0

	hasTwitter, hasWebsite, hasTelegram := false, false, false
	for _, l := range p.Links {
		switch l.Type {
		case "twitter":
			hasTwitter = true
		case "website":
			hasWebsite = true
		case "telegram":
			hasTelegram = true
		}
	}
	if hasTwitter || hasWebsite || hasTelegram {
		score++
	}
	if (hasTwitter || hasTelegram) && hasWebsite {
		score++ // есть и соцсеть, и сайт
	}

	if d.liquidityUSD > 10_000 {
		score++
	}
	if d.liquidityUSD > 50_000 {
		score++
	}
	if d.volumeH1 > 1_000 {
		score++
	}
	if d.txnsH1Buys > 20 {
		score++
	}
	if d.priceChangeH1 > 5 {
		score++ // положительный моментум
	}

	return score
}

// formatDexMetrics формирует строку с метриками для TG-сообщения.
func formatDexMetrics(d dexTokenData, score int) string {
	liq := formatUSD(d.liquidityUSD)
	vol := formatUSD(d.volumeH1)
	sign := "+"
	if d.priceChangeH1 < 0 {
		sign = ""
	}
	return fmt.Sprintf("💧 Ликвидность: %s | 📊 Объём 1ч: %s | 🛒 Покупки 1ч: %d | 📈 Цена 1ч: %s%.2f%% | ⭐ Скор: %d/7",
		liq, vol, d.txnsH1Buys, sign, d.priceChangeH1, score)
}

func formatUSD(v float64) string {
	if v >= 1_000_000 {
		return fmt.Sprintf("$%.1fM", v/1_000_000)
	}
	if v >= 1_000 {
		return fmt.Sprintf("$%.1fK", v/1_000)
	}
	return fmt.Sprintf("$%.0f", math.Round(v))
}

func dexMinScore() int {
	if v, err := strconv.Atoi(os.Getenv("DEXSCREENER_MIN_SCORE")); err == nil && v >= 0 {
		return v
	}
	return 3
}

// FetchDexScreenerListings возвращает отфильтрованные по скору токен-профили с DEX.
func FetchDexScreenerListings(ctx context.Context, log *zap.Logger) ([]Item, error) {
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

	// Фильтруем шумные цепочки.
	var profiles []dexscreenerProfile
	for _, p := range allProfiles {
		if !skipDexChains[p.ChainID] {
			profiles = append(profiles, p)
		}
	}

	// Параллельно получаем данные пар для каждого токена.
	tokenData := make([]dexTokenData, len(profiles))
	var wg sync.WaitGroup
	for i, p := range profiles {
		i, p := i, p
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokenData[i] = fetchDexTokenData(ctx, p.ChainID, p.TokenAddress)
		}()
	}
	wg.Wait()

	minScore := dexMinScore()
	log.Info("dexscreener: scoring tokens",
		zap.Int("total_profiles", len(allProfiles)),
		zap.Int("after_chain_filter", len(profiles)),
		zap.Int("min_score", minScore),
	)

	var items []Item
	for i, p := range profiles {
		d := tokenData[i]
		score := scoreDexToken(p, d)
		log.Debug("dexscreener: token scored",
			zap.String("chain", p.ChainID),
			zap.String("address", p.TokenAddress),
			zap.String("title", d.title),
			zap.Int("score", score),
			zap.Float64("liquidity_usd", d.liquidityUSD),
			zap.Float64("volume_h1", d.volumeH1),
			zap.Int("txns_h1_buys", d.txnsH1Buys),
			zap.Float64("price_change_h1", d.priceChangeH1),
			zap.Int("links_count", len(p.Links)),
		)
		if score < minScore {
			continue
		}

		var publishedAt *time.Time
		if p.UpdatedAt != "" {
			if t, err := time.Parse(time.RFC3339Nano, p.UpdatedAt); err == nil {
				publishedAt = &t
			}
		}

		desc := p.Description
		if desc == "" {
			desc = fmt.Sprintf("New token on %s DEX", p.ChainID)
		}
		metrics := formatDexMetrics(d, score)
		summary := buildSummary(desc) + "\n" + metrics

		title := d.title
		if title == "" {
			title = "New DEX token listing"
		}

		items = append(items, Item{
			Source:      "dexscreener",
			GUID:        fmt.Sprintf("dex-%s-%s", p.ChainID, p.TokenAddress),
			Title:       title,
			Link:        p.URL,
			Summary:     summary,
			PublishedAt: publishedAt,
			IsListing:   true,
		})
	}
	return items, nil
}
