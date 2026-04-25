package rss

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const bitgetAnnouncementsURL = "https://api.bitget.com/api/v2/public/annoucements?annType=coin_listings&pageNo=1&pageSize=20&language=en_US"

type bitgetAnnouncementsResponse struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data []struct {
		AnnID    string `json:"annId"`
		AnnTitle string `json:"annTitle"`
		AnnType  string `json:"annType"`
		AnnURL   string `json:"annUrl"`
		CTime    string `json:"cTime"` // unix ms as string
	} `json:"data"`
}

// FetchBitgetAnnouncements получает последние объявления о листингах с Bitget.
func FetchBitgetAnnouncements(ctx context.Context) ([]Item, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bitgetAnnouncementsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("bitget announcements: create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bitget announcements: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bitget announcements: unexpected status %d", resp.StatusCode)
	}

	var result bitgetAnnouncementsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("bitget announcements: decode: %w", err)
	}
	if result.Code != "00000" {
		return nil, fmt.Errorf("bitget announcements: api error %s: %s", result.Code, result.Msg)
	}

	var items []Item
	for _, a := range result.Data {
		var publishedAt *time.Time
		if a.CTime != "" {
			var ms int64
			if _, err := fmt.Sscanf(a.CTime, "%d", &ms); err == nil {
				t := time.UnixMilli(ms).UTC()
				publishedAt = &t
			}
		}
		items = append(items, Item{
			Source:      "bitget",
			GUID:        fmt.Sprintf("bitget-announce-%s", a.AnnID),
			Title:       a.AnnTitle,
			Link:        a.AnnURL,
			Summary:     a.AnnTitle,
			PublishedAt: publishedAt,
		})
	}
	return items, nil
}
