package rss

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const okxAnnouncementsURL = "https://www.okx.com/api/v5/support/announcements?limit=20"
const okxListingAnnType = "announcements-new-listings"

type okxAnnouncementsResponse struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data []struct {
		Details []struct {
			AnnType string `json:"annType"`
			Title   string `json:"title"`
			URL     string `json:"url"`
			PTime   string `json:"pTime"` // unix ms as string
		} `json:"details"`
	} `json:"data"`
}

// FetchOKXAnnouncements получает последние объявления о листингах с OKX.
func FetchOKXAnnouncements(ctx context.Context) ([]Item, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, okxAnnouncementsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("okx announcements: create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("okx announcements: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("okx announcements: unexpected status %d", resp.StatusCode)
	}

	var result okxAnnouncementsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("okx announcements: decode: %w", err)
	}
	if result.Code != "0" {
		return nil, fmt.Errorf("okx announcements: api error %s: %s", result.Code, result.Msg)
	}

	var items []Item
	for _, d := range result.Data {
		for _, a := range d.Details {
			if a.AnnType != okxListingAnnType {
				continue
			}
			ms, err := strconv.ParseInt(a.PTime, 10, 64)
			if err != nil {
				continue
			}
			publishedAt := time.UnixMilli(ms).UTC()
			items = append(items, Item{
				Source:      "okx",
				GUID:        fmt.Sprintf("okx-announce-%s", a.URL),
				Title:       a.Title,
				Link:        a.URL,
				Summary:     a.Title,
				PublishedAt: &publishedAt,
			})
		}
	}
	return items, nil
}
