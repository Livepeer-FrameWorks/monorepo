package knowledge

import (
	"testing"
	"time"
)

func TestScorePageLocalFilesFirst(t *testing.T) {
	now := time.Now()
	interval := 24 * time.Hour

	local := scorePage(CrawlItem{SourceType: "local"}, now, interval)
	direct := scorePage(CrawlItem{SourceType: "direct"}, now, interval)
	sitemap := scorePage(CrawlItem{SourceType: "sitemap"}, now, interval)

	if local <= direct || direct <= sitemap {
		t.Fatalf("expected local > direct > sitemap, got %.1f, %.1f, %.1f", local, direct, sitemap)
	}
}

func TestScorePageNeverCrawledBonus(t *testing.T) {
	now := time.Now()
	interval := 24 * time.Hour

	neverCrawled := scorePage(CrawlItem{
		SourceType: "sitemap",
	}, now, interval)

	recentlyCrawled := scorePage(CrawlItem{
		SourceType:    "sitemap",
		LastFetchedAt: now.Add(-1 * time.Hour),
	}, now, interval)

	if neverCrawled <= recentlyCrawled {
		t.Fatalf("never-crawled (%.1f) should score higher than recently-crawled (%.1f)", neverCrawled, recentlyCrawled)
	}
}

func TestScorePageStalenessRamp(t *testing.T) {
	now := time.Now()
	interval := 24 * time.Hour

	fresh := scorePage(CrawlItem{
		SourceType:    "sitemap",
		LastFetchedAt: now.Add(-1 * time.Hour),
	}, now, interval)

	stale := scorePage(CrawlItem{
		SourceType:    "sitemap",
		LastFetchedAt: now.Add(-23 * time.Hour),
	}, now, interval)

	veryStale := scorePage(CrawlItem{
		SourceType:    "sitemap",
		LastFetchedAt: now.Add(-48 * time.Hour),
	}, now, interval)

	if fresh >= stale || stale >= veryStale {
		t.Fatalf("expected fresh < stale < veryStale, got %.1f, %.1f, %.1f", fresh, stale, veryStale)
	}
}

func TestScorePageUnchangedPenalty(t *testing.T) {
	now := time.Now()
	interval := 24 * time.Hour
	base := CrawlItem{
		SourceType:    "sitemap",
		LastFetchedAt: now.Add(-12 * time.Hour),
	}

	volatile := base
	volatile.ConsecutiveUnchanged = 0

	stable := base
	stable.ConsecutiveUnchanged = 5

	veryStable := base
	veryStable.ConsecutiveUnchanged = 10

	vs := scorePage(volatile, now, interval)
	ss := scorePage(stable, now, interval)
	vss := scorePage(veryStable, now, interval)

	if vs <= ss || ss <= vss {
		t.Fatalf("expected volatile > stable > veryStable, got %.1f, %.1f, %.1f", vs, ss, vss)
	}
}

func TestScorePageFailureBackoff(t *testing.T) {
	now := time.Now()
	interval := 24 * time.Hour

	healthy := scorePage(CrawlItem{
		SourceType:          "sitemap",
		LastFetchedAt:       now.Add(-12 * time.Hour),
		ConsecutiveFailures: 0,
	}, now, interval)

	failing := scorePage(CrawlItem{
		SourceType:          "sitemap",
		LastFetchedAt:       now.Add(-12 * time.Hour),
		ConsecutiveFailures: 3,
	}, now, interval)

	if healthy <= failing {
		t.Fatalf("healthy page (%.1f) should score higher than failing page (%.1f)", healthy, failing)
	}
}

func TestScorePageChangefreqBoost(t *testing.T) {
	now := time.Now()
	interval := 24 * time.Hour

	daily := scorePage(CrawlItem{
		SourceType:        "sitemap",
		LastFetchedAt:     now.Add(-12 * time.Hour),
		SitemapChangeFreq: "daily",
	}, now, interval)

	yearly := scorePage(CrawlItem{
		SourceType:        "sitemap",
		LastFetchedAt:     now.Add(-12 * time.Hour),
		SitemapChangeFreq: "yearly",
	}, now, interval)

	if daily <= yearly {
		t.Fatalf("daily changefreq (%.1f) should score higher than yearly (%.1f)", daily, yearly)
	}
}

func TestBuildQueueOrdering(t *testing.T) {
	now := time.Now()
	interval := 24 * time.Hour

	sources := []crawlSource{
		{url: "local:///docs/faq.md", localPath: "/docs/faq.md"},
		{url: "https://example.com/page", direct: true},
		{url: "https://example.com/sitemap.xml"},
	}

	sitemapPages := map[string][]Page{
		"https://example.com/sitemap.xml": {
			{URL: "https://example.com/docs/a", Priority: 0.8},
			{URL: "https://example.com/docs/b", Priority: 0.3},
		},
	}

	queue := BuildQueue(sources, nil, sitemapPages, now, interval)

	if queue.Total() != 4 {
		t.Fatalf("expected 4 items, got %d", queue.Total())
	}

	first := queue.Pop()
	if first.SourceType != "local" {
		t.Fatalf("expected local file first, got %s (%s)", first.SourceType, first.PageURL)
	}

	second := queue.Pop()
	if second.SourceType != "direct" {
		t.Fatalf("expected direct page second, got %s (%s)", second.SourceType, second.PageURL)
	}
}

func TestBuildQueueMergesCacheState(t *testing.T) {
	now := time.Now()
	interval := 24 * time.Hour

	sources := []crawlSource{
		{url: "https://example.com/sitemap.xml"},
	}
	sitemapPages := map[string][]Page{
		"https://example.com/sitemap.xml": {
			{URL: "https://example.com/page1", Priority: 0.5},
			{URL: "https://example.com/page2", Priority: 0.5},
		},
	}

	cached := map[string]*PageCache{
		"https://example.com/page1": {
			PageURL:              "https://example.com/page1",
			LastFetchedAt:        now.Add(-1 * time.Hour),
			ConsecutiveUnchanged: 5,
		},
	}

	queue := BuildQueue(sources, cached, sitemapPages, now, interval)
	if queue.Total() != 2 {
		t.Fatalf("expected 2 items, got %d", queue.Total())
	}

	// page2 (never crawled) should come first
	first := queue.Pop()
	if first.PageURL != "https://example.com/page2" {
		t.Fatalf("expected never-crawled page2 first, got %s", first.PageURL)
	}
}

func TestBuildQueueDeduplicates(t *testing.T) {
	now := time.Now()
	interval := 24 * time.Hour

	sources := []crawlSource{
		{url: "https://example.com/page", direct: true},
		{url: "https://example.com/sitemap.xml"},
	}
	sitemapPages := map[string][]Page{
		"https://example.com/sitemap.xml": {
			{URL: "https://example.com/page", Priority: 0.5},
		},
	}

	queue := BuildQueue(sources, nil, sitemapPages, now, interval)
	if queue.Total() != 1 {
		t.Fatalf("expected 1 item (dedup), got %d", queue.Total())
	}
	// Direct page takes precedence since it's added first
	item := queue.Pop()
	if item.SourceType != "direct" {
		t.Fatalf("expected direct source type, got %s", item.SourceType)
	}
}

func TestCrawlQueuePop(t *testing.T) {
	queue := &CrawlQueue{
		items: []CrawlItem{
			{PageURL: "a", Score: 10},
			{PageURL: "b", Score: 5},
		},
	}

	if queue.Total() != 2 || queue.Remaining() != 2 {
		t.Fatalf("expected total=2, remaining=2")
	}

	first := queue.Pop()
	if first.PageURL != "a" {
		t.Fatalf("expected 'a', got %q", first.PageURL)
	}
	if queue.Remaining() != 1 {
		t.Fatalf("expected remaining=1, got %d", queue.Remaining())
	}

	second := queue.Pop()
	if second.PageURL != "b" {
		t.Fatalf("expected 'b', got %q", second.PageURL)
	}

	third := queue.Pop()
	if third != nil {
		t.Fatalf("expected nil after exhaustion, got %+v", third)
	}
}

func TestCrawlQueueNil(t *testing.T) {
	var q *CrawlQueue
	if q.Pop() != nil || q.Total() != 0 || q.Remaining() != 0 {
		t.Fatal("nil queue should return zero values")
	}
}
