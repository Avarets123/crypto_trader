package rss

import (
	"context"
	"time"

	"github.com/mmcdole/gofeed"
)

const decryptURL = "https://decrypt.co/feed"

// FetchDecrypt парсит RSS-ленту Decrypt.
func FetchDecrypt(ctx context.Context) ([]Item, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	fp := gofeed.NewParser()
	feed, err := fp.ParseURLWithContext(decryptURL, ctx)
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
			Source:      "decrypt",
			GUID:        guid,
			Title:       i.Title,
			Link:        i.Link,
			Summary:     buildSummary(i.Description),
			PublishedAt: i.PublishedParsed,
		})
	}
	return items, nil
}
