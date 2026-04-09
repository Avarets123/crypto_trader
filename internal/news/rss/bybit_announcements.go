package rss

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const bybitAnnouncementsURL = "https://api.bybit.com/v5/announcements/index?locale=en-US&type=new_listings&page=1&limit=20"

type bybitAnnouncementsResponse struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		List []struct {
			ID            string `json:"id"`
			Title         string `json:"title"`
			Description   string `json:"description"`
			URL           string `json:"url"`
			DateTimestamp string `json:"dateTimestamp"` // unix ms в виде строки
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

	items := make([]Item, 0, len(result.Result.List))
	for _, a := range result.Result.List {
		guid := "bybit-announce-" + a.ID
		summary := buildSummary(a.Description)
		if summary == "" {
			summary = a.Title
		}

		var publishedAt *time.Time
		if ms, err := strconv.ParseInt(a.DateTimestamp, 10, 64); err == nil {
			t := time.UnixMilli(ms).UTC()
			publishedAt = &t
		}

		items = append(items, Item{
			Source:      "bybit",
			GUID:        guid,
			Title:       a.Title,
			Link:        a.URL,
			Summary:     summary,
			PublishedAt: publishedAt,
		})
	}
	return items, nil
}
