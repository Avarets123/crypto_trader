package rss

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const bybitAnnouncementsURL = "https://api.bybit.com/v5/announcements/index?locale=en-US&type=new_crypto&page=1&limit=20"

type bybitAnnouncementsResponse struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		List []struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			URL         string `json:"url"`
			PublishTime int64  `json:"publishTime"`
		} `json:"list"`
	} `json:"result"`
}

// FetchBybitAnnouncements получает последние объявления о листингах с Bybit.
func FetchBybitAnnouncements(ctx context.Context) ([]Item, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bybitAnnouncementsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("bybit announcements: create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bybit announcements: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bybit announcements: unexpected status %d", resp.StatusCode)
	}

	var result bybitAnnouncementsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("bybit announcements: decode: %w", err)
	}
	if result.RetCode != 0 {
		return nil, fmt.Errorf("bybit announcements: api error %d: %s", result.RetCode, result.RetMsg)
	}

	var items []Item
	for _, a := range result.Result.List {
		publishedAt := time.UnixMilli(a.PublishTime).UTC()
		items = append(items, Item{
			Source:      "bybit",
			GUID:        fmt.Sprintf("bybit-announce-%s", a.URL),
			Title:       a.Title,
			Link:        a.URL,
			Summary:     buildSummary(a.Description),
			PublishedAt: &publishedAt,
		})
	}
	return items, nil
}
