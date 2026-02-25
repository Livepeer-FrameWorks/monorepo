package knowledge

import (
	"sort"
	"time"
)

// CrawlItem represents a single page to be crawled, with its computed priority score.
type CrawlItem struct {
	PageURL           string
	SourceRoot        string
	SourceType        string // "local", "sitemap", "direct", "discovered"
	Render            bool
	LastFetchedAt     time.Time
	SitemapPriority   float64
	SitemapChangeFreq string
	SitemapLastMod    string
	ContentHash       string

	ConsecutiveUnchanged int
	ConsecutiveFailures  int

	Score float64
}

// CrawlQueue is a priority-ordered list of pages to process within a cycle.
type CrawlQueue struct {
	items []CrawlItem
	idx   int
}

// Pop returns the next highest-priority item, or nil when exhausted.
func (q *CrawlQueue) Pop() *CrawlItem {
	if q == nil || q.idx >= len(q.items) {
		return nil
	}
	item := &q.items[q.idx]
	q.idx++
	return item
}

// Remaining returns the number of unprocessed items.
func (q *CrawlQueue) Remaining() int {
	if q == nil {
		return 0
	}
	return len(q.items) - q.idx
}

// Total returns the original queue size.
func (q *CrawlQueue) Total() int {
	if q == nil {
		return 0
	}
	return len(q.items)
}

// BuildQueue constructs a priority-ordered crawl queue by merging source definitions,
// sitemap-expanded pages, and cached page state.
func BuildQueue(
	sources []crawlSource,
	cached map[string]*PageCache,
	sitemapPages map[string][]Page,
	now time.Time,
	interval time.Duration,
) *CrawlQueue {
	seen := make(map[string]bool)
	var items []CrawlItem

	// Local files — highest base priority, fast to process.
	for _, src := range sources {
		if src.localPath == "" {
			continue
		}
		pageURL := src.url // already "local://..." from loadSources
		if seen[pageURL] {
			continue
		}
		seen[pageURL] = true
		item := CrawlItem{
			PageURL:    pageURL,
			SourceRoot: localFileSourceRoot,
			SourceType: "local",
		}
		if pc := cached[pageURL]; pc != nil {
			item.LastFetchedAt = pc.LastFetchedAt
			item.ContentHash = pc.ContentHash
			item.ConsecutiveUnchanged = pc.ConsecutiveUnchanged
			item.ConsecutiveFailures = pc.ConsecutiveFailures
		}
		items = append(items, item)
	}

	// Direct pages (page: and render: prefixed).
	for _, src := range sources {
		if src.localPath != "" || !src.direct {
			continue
		}
		if seen[src.url] {
			continue
		}
		seen[src.url] = true
		item := CrawlItem{
			PageURL:    src.url,
			SourceRoot: directPageSourceRoot,
			SourceType: "direct",
			Render:     src.render,
		}
		if pc := cached[src.url]; pc != nil {
			item.LastFetchedAt = pc.LastFetchedAt
			item.ContentHash = pc.ContentHash
			item.ConsecutiveUnchanged = pc.ConsecutiveUnchanged
			item.ConsecutiveFailures = pc.ConsecutiveFailures
		}
		items = append(items, item)
	}

	// Sitemap pages — expanded from XML into individual page URLs.
	for _, src := range sources {
		if src.localPath != "" || src.direct {
			continue
		}
		pages, ok := sitemapPages[src.url]
		if !ok {
			continue
		}
		for _, p := range pages {
			if seen[p.URL] {
				continue
			}
			seen[p.URL] = true
			item := CrawlItem{
				PageURL:           p.URL,
				SourceRoot:        src.url,
				SourceType:        "sitemap",
				Render:            src.render,
				SitemapPriority:   p.Priority,
				SitemapChangeFreq: p.ChangeFreq,
				SitemapLastMod:    p.LastMod,
			}
			if pc := cached[p.URL]; pc != nil {
				item.LastFetchedAt = pc.LastFetchedAt
				item.ContentHash = pc.ContentHash
				item.ConsecutiveUnchanged = pc.ConsecutiveUnchanged
				item.ConsecutiveFailures = pc.ConsecutiveFailures
			}
			items = append(items, item)
		}
	}

	for i := range items {
		items[i].Score = scorePage(items[i], now, interval)
	}
	sort.SliceStable(items, func(a, b int) bool {
		return items[a].Score > items[b].Score
	})

	return &CrawlQueue{items: items}
}

// scorePage computes a priority score for a crawl item.
// Higher score = process first.
func scorePage(item CrawlItem, now time.Time, interval time.Duration) float64 {
	score := 0.0

	// Source type base priority.
	switch item.SourceType {
	case "local":
		score += 100.0
	case "direct":
		score += 50.0
	case "sitemap":
		score += 10.0
	case "discovered":
		score += 5.0
	}

	// Never-crawled pages get a large bonus — index new content first.
	if item.LastFetchedAt.IsZero() {
		score += 80.0
		return score
	}

	// Staleness: linear ramp from 0 to 40 as age approaches the interval.
	age := now.Sub(item.LastFetchedAt)
	staleness := age.Seconds() / interval.Seconds()
	if staleness > 2.0 {
		staleness = 2.0
	}
	score += staleness * 20.0

	// Sitemap priority hint (0.0–1.0, default 0.5).
	score += item.SitemapPriority * 10.0

	// Changefreq hint.
	switch item.SitemapChangeFreq {
	case "always":
		score += 8.0
	case "hourly":
		score += 6.0
	case "daily":
		score += 4.0
	case "weekly":
		score += 2.0
	}

	// Pages that never change drift down in priority.
	unchangedPenalty := float64(item.ConsecutiveUnchanged) * 2.0
	if unchangedPenalty > 20.0 {
		unchangedPenalty = 20.0
	}
	score -= unchangedPenalty

	// Failed pages get exponential backoff.
	failPenalty := float64(item.ConsecutiveFailures) * 5.0
	if failPenalty > 30.0 {
		failPenalty = 30.0
	}
	score -= failPenalty

	return score
}
