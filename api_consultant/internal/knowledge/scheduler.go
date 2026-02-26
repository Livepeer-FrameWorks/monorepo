package knowledge

import (
	"bufio"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/logging"
	"golang.org/x/sync/errgroup"
)

// PageEmbeddedEvent is emitted when the crawler successfully embeds a page.
type PageEmbeddedEvent struct {
	PageURL    string
	SourceRoot string
	TenantID   string
}

type CrawlScheduler struct {
	crawler        *Crawler
	db             *sql.DB
	pageCache      *PageCacheStore
	health         *HealthTracker
	interval       time.Duration
	tenantID       string
	sitemaps       []string
	sitemapsDir    string
	logger         logging.Logger
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	onPageEmbedded func(PageEmbeddedEvent)
}

type SchedulerConfig struct {
	Crawler        *Crawler
	DB             *sql.DB
	PageCache      *PageCacheStore
	Health         *HealthTracker
	Interval       time.Duration
	TenantID       string
	Sitemaps       []string
	SitemapsDir    string
	Logger         logging.Logger
	OnPageEmbedded func(PageEmbeddedEvent)
}

func NewCrawlScheduler(cfg SchedulerConfig) *CrawlScheduler {
	interval := cfg.Interval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	health := cfg.Health
	if health == nil {
		health = NewHealthTracker()
	}
	return &CrawlScheduler{
		crawler:        cfg.Crawler,
		db:             cfg.DB,
		pageCache:      cfg.PageCache,
		health:         health,
		interval:       interval,
		tenantID:       cfg.TenantID,
		sitemaps:       cfg.Sitemaps,
		sitemapsDir:    cfg.SitemapsDir,
		logger:         cfg.Logger,
		onPageEmbedded: cfg.OnPageEmbedded,
	}
}

// Health returns the crawl health tracker for admin API exposure.
func (s *CrawlScheduler) Health() *HealthTracker {
	if s == nil {
		return nil
	}
	return s.health
}

func (s *CrawlScheduler) Start(ctx context.Context) {
	if s == nil {
		return
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Add(1)
	defer s.wg.Done()

	for {
		s.runCycle(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Minute):
			// Brief pause between cycles to rebuild queue with fresh state.
			// The actual pacing is inside drainQueue.
		}
	}
}

func (s *CrawlScheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

type crawlSource struct {
	url       string
	direct    bool
	render    bool
	localPath string // non-empty for local: file entries
}

const (
	directPagePrefix = "page:"
	renderPrefix     = "render:"
	localPrefix      = "local:"
)

func (s *CrawlScheduler) loadSources() []crawlSource {
	seen := make(map[string]bool)
	var result []crawlSource
	add := func(raw string) {
		raw = strings.TrimSpace(raw)

		// Local file — mutually exclusive with page:/render: prefixes.
		if strings.HasPrefix(raw, localPrefix) {
			raw = strings.TrimPrefix(raw, localPrefix)
			raw = strings.TrimSpace(raw)
			if raw == "" {
				return
			}
			lp := raw
			if !filepath.IsAbs(lp) && s.sitemapsDir != "" {
				lp = filepath.Join(s.sitemapsDir, lp)
			}
			lp = filepath.Clean(lp)
			key := "local://" + lp
			if !seen[key] {
				seen[key] = true
				result = append(result, crawlSource{url: key, localPath: lp})
			}
			return
		}

		direct := strings.HasPrefix(raw, directPagePrefix)
		if direct {
			raw = strings.TrimPrefix(raw, directPagePrefix)
			raw = strings.TrimSpace(raw)
		}
		render := strings.HasPrefix(raw, renderPrefix)
		if render {
			raw = strings.TrimPrefix(raw, renderPrefix)
			raw = strings.TrimSpace(raw)
		}
		if raw != "" && !seen[raw] {
			seen[raw] = true
			result = append(result, crawlSource{url: raw, direct: direct, render: render})
		}
	}
	for _, u := range s.sitemaps {
		add(u)
	}
	if s.sitemapsDir != "" {
		entries, err := os.ReadDir(s.sitemapsDir)
		if err != nil {
			if !os.IsNotExist(err) {
				s.logger.WithError(err).WithField("dir", s.sitemapsDir).Warn("Failed to read sitemaps directory")
			}
			return result
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			path := s.sitemapsDir + "/" + entry.Name()
			f, err := os.Open(path)
			if err != nil {
				s.logger.WithError(err).WithField("file", path).Warn("Failed to open sitemap file")
				continue
			}
			func() {
				defer f.Close()
				scanner := bufio.NewScanner(f)
				for scanner.Scan() {
					line := strings.TrimSpace(scanner.Text())
					if line == "" || strings.HasPrefix(line, "#") {
						continue
					}
					add(os.ExpandEnv(line))
				}
			}()
		}
	}
	return result
}

const directPageSourceRoot = "pagelist://direct"

func (s *CrawlScheduler) runCycle(ctx context.Context) {
	defer s.cleanupStaleCache(ctx)

	sources := s.loadSources()
	if len(sources) == 0 {
		s.logger.Debug("No sources configured, skipping crawl cycle")
		return
	}

	// Phase 1: Expand sitemaps into individual page URLs (lightweight XML fetch).
	sitemapPages := s.expandSitemaps(ctx, sources)

	// Phase 2: Load all cached page state for priority scoring.
	cached := s.loadCachedPages(ctx)

	// Phase 3: Build priority queue.
	queue := BuildQueue(sources, cached, sitemapPages, time.Now(), s.interval)
	total := queue.Total()
	if total == 0 {
		s.logger.Debug("Empty crawl queue, nothing to process")
		return
	}
	s.logger.WithField("total", total).Info("Crawl queue built")
	crawlQueueSize.Set(float64(total))

	// Phase 4: Persist sitemap metadata for newly discovered pages.
	s.persistSitemapMeta(ctx, sitemapPages, cached)

	// Phase 5: Drain at steady rate.
	s.health.StartCycle()
	s.drainQueue(ctx, queue)
}

// expandSitemaps fetches and parses sitemap XML for each sitemap source.
// Returns a map of sitemap URL → expanded pages. Failures are logged and skipped.
func (s *CrawlScheduler) expandSitemaps(ctx context.Context, sources []crawlSource) map[string][]Page {
	result := make(map[string][]Page)
	var mu sync.Mutex

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(3)

	for _, src := range sources {
		if src.localPath != "" || src.direct {
			continue
		}
		src := src
		g.Go(func() error {
			pages, err := s.crawler.CrawlSitemap(gctx, src.url)
			if err != nil {
				s.logger.WithError(err).WithField("sitemap", src.url).Warn("Failed to expand sitemap")
				return nil
			}
			mu.Lock()
			result[src.url] = pages
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	return result
}

func (s *CrawlScheduler) loadCachedPages(ctx context.Context) map[string]*PageCache {
	if s.pageCache == nil {
		return nil
	}
	all, err := s.pageCache.ListForTenant(ctx, s.tenantID)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to load page cache, building queue without history")
		return nil
	}
	cached := make(map[string]*PageCache, len(all))
	for i := range all {
		cached[all[i].PageURL] = &all[i]
	}
	return cached
}

// persistSitemapMeta upserts page cache entries for newly discovered sitemap pages
// so their priority/changefreq metadata is available for future cycles.
func (s *CrawlScheduler) persistSitemapMeta(ctx context.Context, sitemapPages map[string][]Page, cached map[string]*PageCache) {
	if s.pageCache == nil {
		return
	}
	seen := make(map[string]struct{})
	var newEntries []PageCache
	for sitemapURL, pages := range sitemapPages {
		for _, p := range pages {
			if cached != nil {
				if _, exists := cached[p.URL]; exists {
					continue
				}
			}
			if _, dup := seen[p.URL]; dup {
				continue
			}
			seen[p.URL] = struct{}{}
			newEntries = append(newEntries, PageCache{
				TenantID:          s.tenantID,
				SourceRoot:        sitemapURL,
				PageURL:           p.URL,
				SitemapPriority:   p.Priority,
				SitemapChangeFreq: p.ChangeFreq,
				SourceType:        "sitemap",
				LastFetchedAt:     time.Time{}, // never fetched
			})
		}
	}
	if len(newEntries) > 0 {
		if err := s.pageCache.BulkUpsert(ctx, newEntries); err != nil {
			s.logger.WithError(err).Warn("Failed to persist sitemap metadata for new pages")
		} else {
			s.logger.WithField("count", len(newEntries)).Debug("Persisted sitemap metadata for new pages")
		}
	}
}

// drainQueue processes the crawl queue at a steady rate spread across the interval.
func (s *CrawlScheduler) drainQueue(ctx context.Context, queue *CrawlQueue) {
	total := queue.Total()
	if total == 0 {
		return
	}

	tickInterval := s.interval / time.Duration(total)
	minDelay := s.crawler.minCrawlDelay
	if minDelay <= 0 {
		minDelay = defaultMinCrawlDelay
	}
	if tickInterval < minDelay {
		tickInterval = minDelay
	}
	crawlTickInterval.Set(tickInterval.Seconds())

	s.logger.WithField("tick", tickInterval.String()).WithField("total", total).Info("Starting queue drain")

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(3)

	var result CrawlResult
	var resultMu sync.Mutex

	// Process first item immediately, then pace subsequent items.
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-gctx.Done():
			_ = g.Wait()
			s.logCycleResult(result)
			return
		case <-timer.C:
			item := queue.Pop()
			if item == nil {
				_ = g.Wait()
				s.logCycleResult(result)
				return
			}
			crawlQueueRemaining.Set(float64(queue.Remaining()))

			it := item
			g.Go(func() error {
				status := s.processItem(gctx, it)
				resultMu.Lock()
				result.add(status)
				resultMu.Unlock()
				return nil
			})

			if queue.Remaining() == 0 {
				_ = g.Wait()
				s.logCycleResult(result)
				return
			}
			timer.Reset(tickInterval)
		}
	}
}

func (s *CrawlScheduler) processItem(ctx context.Context, item *CrawlItem) PageStatus {
	if item.SourceType == "local" {
		localPath := strings.TrimPrefix(item.PageURL, "local://")
		err := s.crawler.CrawlLocalFiles(ctx, s.tenantID, []string{localPath})
		if err != nil {
			s.health.RecordFailure(item.SourceRoot)
			s.updateOutcome(ctx, item, false, true)
			return PageFailed
		}
		s.health.RecordSuccess(item.SourceRoot, 1)
		s.updateOutcome(ctx, item, true, false)
		if s.onPageEmbedded != nil {
			s.onPageEmbedded(PageEmbeddedEvent{
				PageURL:    item.PageURL,
				SourceRoot: item.SourceRoot,
				TenantID:   s.tenantID,
			})
		}
		return PageEmbedded
	}

	status, err := s.crawler.processPage(ctx, s.tenantID, item.SourceRoot, item.PageURL, item.SitemapLastMod, item.SourceType, item.Render, nil)
	if err != nil {
		s.health.RecordFailure(item.SourceRoot)
		s.updateOutcome(ctx, item, false, true)
		return PageFailed
	}

	changed := status == PageEmbedded
	failed := status == PageFailed
	s.updateOutcome(ctx, item, changed, failed)

	if changed {
		s.health.RecordSuccess(item.SourceRoot, 1)
		if s.onPageEmbedded != nil {
			s.onPageEmbedded(PageEmbeddedEvent{
				PageURL:    item.PageURL,
				SourceRoot: item.SourceRoot,
				TenantID:   s.tenantID,
			})
		}
	}
	return status
}

func (s *CrawlScheduler) updateOutcome(ctx context.Context, item *CrawlItem, changed, failed bool) {
	if s.pageCache == nil {
		return
	}
	if err := s.pageCache.UpdateCrawlOutcome(ctx, s.tenantID, item.PageURL, changed, failed); err != nil {
		s.logger.WithError(err).WithField("url", item.PageURL).Warn("Failed to update crawl outcome")
	}
}

func (s *CrawlScheduler) logCycleResult(result CrawlResult) {
	s.logger.
		WithField("embedded", result.Embedded).
		WithField("skipped_304", result.Skipped304).
		WithField("skipped_hash", result.SkippedHash).
		WithField("skipped_ttl", result.SkippedTTL).
		WithField("failed", result.Failed).
		WithField("no_chunks", result.NoChunks).
		WithField("excluded", result.Excluded).
		WithField("disallowed", result.Disallowed).
		Info("Crawl queue drain completed")

	crawlPagesTotal.WithLabelValues("embedded").Add(float64(result.Embedded))
	crawlPagesTotal.WithLabelValues("skipped_304").Add(float64(result.Skipped304))
	crawlPagesTotal.WithLabelValues("skipped_hash").Add(float64(result.SkippedHash))
	crawlPagesTotal.WithLabelValues("skipped_ttl").Add(float64(result.SkippedTTL))
	crawlPagesTotal.WithLabelValues("failed").Add(float64(result.Failed))
	crawlPagesTotal.WithLabelValues("no_chunks").Add(float64(result.NoChunks))
	crawlPagesTotal.WithLabelValues("excluded").Add(float64(result.Excluded))
	crawlPagesTotal.WithLabelValues("disallowed").Add(float64(result.Disallowed))

	crawlQueueRemaining.Set(0)
}

func (s *CrawlScheduler) cleanupStaleCache(ctx context.Context) {
	if s.pageCache != nil {
		n, err := s.pageCache.CleanupStale(ctx, s.tenantID, 2*s.interval)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to cleanup stale page cache entries")
		} else if n > 0 {
			s.logger.WithField("deleted", n).Info("Cleaned up stale page cache entries")
		}
	}

	if s.db != nil {
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM skipper.skipper_crawl_jobs WHERE tenant_id = $1 AND finished_at < $2`,
			s.tenantID, time.Now().Add(-7*24*time.Hour)); err != nil {
			s.logger.WithError(err).Warn("Failed to cleanup old crawl jobs")
		}
	}
}
