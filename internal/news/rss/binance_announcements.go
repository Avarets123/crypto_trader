package rss

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const binanceAnnouncementsURL = "https://www.binance.com/bapi/composite/v1/public/cms/article/list/query?type=1&catalogId=48&pageNo=1&pageSize=20"
const binanceArticleBaseURL = "https://www.binance.com/en/support/announcement/"

type binanceAnnouncementsResponse struct {
	Code string `json:"code"`
	Data struct {
		Catalogs []struct {
			Articles []struct {
				ID          int64  `json:"id"`
				Code        string `json:"code"`
				Title       string `json:"title"`
				ReleaseDate int64  `json:"releaseDate"` // unix ms
			} `json:"articles"`
		} `json:"catalogs"`
	} `json:"data"`
}

// FetchBinanceAnnouncements получает последние объявления о листингах с Binance.
func FetchBinanceAnnouncements(ctx context.Context) ([]Item, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, binanceAnnouncementsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("binance announcements: create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("binance announcements: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("binance announcements: unexpected status %d", resp.StatusCode)
	}

	var result binanceAnnouncementsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("binance announcements: decode: %w", err)
	}
	if result.Code != "000000" {
		return nil, fmt.Errorf("binance announcements: api error code %s", result.Code)
	}

	var items []Item
	for _, cat := range result.Data.Catalogs {
		for _, a := range cat.Articles {
			link := binanceArticleBaseURL + a.Code
			guid := fmt.Sprintf("binance-announce-%d", a.ID)
			publishedAt := time.UnixMilli(a.ReleaseDate).UTC()
			items = append(items, Item{
				Source:      "binance",
				GUID:        guid,
				Title:       a.Title,
				Link:        link,
				Summary:     a.Title,
				PublishedAt: &publishedAt,
				IsListing:   true,
			})
		}
	}
	return items, nil
}
