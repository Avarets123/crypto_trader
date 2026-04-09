package news

import "time"

// Article — одна новость из RSS-ленты.
type Article struct {
	ID          int64
	Source      string
	GUID        string
	Title       string
	Link        string
	Description string
	Summary     string
	PublishedAt *time.Time
	CreatedAt   time.Time
}
