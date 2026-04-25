package rss

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const kucoinAnnouncementsURL = "https://api.kucoin.com/api/v3/announcements?currentPage=1&pageSize=20&annType=new-listings&lang=en_US"

type kucoinAnnouncementsResponse struct {
	Code string `json:"code"`
	Data struct {
		Items []struct {
			AnnID    int64    `json:"annId"`
			AnnTitle string   `json:"annTitle"`
			AnnType  []string `json:"annType"`
			AnnDesc  string   `json:"annDesc"`
			CTime    int64    `json:"cTime"`
			AnnURL   string   `json:"annUrl"`
		} `json:"items"`
	} `json:"data"`
}

// FetchKuCoinAnnouncements получает последние объявления о листингах с KuCoin.
func FetchKuCoinAnnouncements(ctx context.Context) ([]Item, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, kucoinAnnouncementsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("kucoin announcements: create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kucoin announcements: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kucoin announcements: unexpected status %d", resp.StatusCode)
	}

	var result kucoinAnnouncementsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("kucoin announcements: decode: %w", err)
	}
	if result.Code != "200000" {
		return nil, fmt.Errorf("kucoin announcements: api error code %s", result.Code)
	}

	var items []Item
	for _, a := range result.Data.Items {
		guid := fmt.Sprintf("kucoin-announce-%d", a.AnnID)
		publishedAt := time.UnixMilli(a.CTime).UTC()
		items = append(items, Item{
			Source:      "kucoin",
			GUID:        guid,
			Title:       a.AnnTitle,
			Link:        a.AnnURL,
			Summary:     a.AnnDesc,
			PublishedAt: &publishedAt,
			IsListing:   true,
		})
	}
	return items, nil
}
