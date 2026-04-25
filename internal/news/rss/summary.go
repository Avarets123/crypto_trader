package rss

import (
	"regexp"
	"strings"
	"time"
)

var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

// Item — статья из RSS до конвертации в news.Article.
type Item struct {
	Source      string
	GUID        string
	Title       string
	Link        string
	Summary     string
	PublishedAt *time.Time
	IsListing   bool
}

// buildSummary удаляет HTML-теги и обрезает до 300 символов.
func buildSummary(html string) string {
	if html == "" {
		return ""
	}
	clean := htmlTagRe.ReplaceAllString(html, "")
	clean = strings.TrimSpace(clean)
	runes := []rune(clean)
	if len(runes) > 300 {
		return string(runes[:300])
	}
	return clean
}
