package rss

import (
	"context"
	"time"

	"github.com/mmcdole/gofeed"
)

const coindeskURL = "https://www.coindesk.com/arc/outboundfeeds/rss/"

// FetchCoinDesk парсит RSS-ленту CoinDesk.
func FetchCoinDesk(ctx context.Context) ([]Item, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	fp := gofeed.NewParser()
	feed, err := fp.ParseURLWithContext(coindeskURL, ctx)
	if err != nil {
		return nil, err
	}

	items := make([]Item, 0, len(feed.Items))
	for _, i := range feed.Items {
		guid := i.GUID
		if guid == "" {
			guid = i.Link
		}
		items = append(items, Item{
			Source:      "coindesk",
			GUID:        guid,
			Title:       i.Title,
			Link:        i.Link,
			Summary:     buildSummary(i.Description),
			PublishedAt: i.PublishedParsed,
		})
	}
	return items, nil
}
