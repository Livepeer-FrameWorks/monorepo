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

type CrawlScheduler struct {
	crawler     *Crawler
	db          *sql.DB
	pageCache   *PageCacheStore
	health      *HealthTracker
	interval    time.Duration
	tenantID    string
	sitemaps    []string
	sitemapsDir string
	logger      logging.Logger
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

type SchedulerConfig struct {
	Crawler     *Crawler
	DB          *sql.DB
	PageCache   *PageCacheStore
	Health      *HealthTracker
	Interval    time.Duration
	TenantID    string
	Sitemaps    []string
	SitemapsDir string
	Logger      logging.Logger
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
		crawler:     cfg.Crawler,
		db:          cfg.DB,
		pageCache:   cfg.PageCache,
		health:      health,
		interval:    interval,
		tenantID:    cfg.TenantID,
		sitemaps:    cfg.Sitemaps,
		sitemapsDir: cfg.SitemapsDir,
		logger:      cfg.Logger,
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

	// Fire immediately then on interval — non-blocking so service startup isn't delayed
	timer := time.NewTimer(0)
	defer timer.Stop()
	first := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.runCycle(ctx, first)
			first = false
			timer.Reset(s.interval)
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

func (s *CrawlScheduler) runCycle(ctx context.Context, checkTTL bool) {
	defer s.cleanupStaleCache(ctx)

	sources := s.loadSources()
	if len(sources) == 0 {
		s.logger.Debug("No sources configured, skipping crawl cycle")
		return
	}

	var directPages, directPagesRendered []string
	var localFiles []string
	var sitemapSources []crawlSource
	for _, src := range sources {
		if src.localPath != "" {
			localFiles = append(localFiles, src.localPath)
		} else if src.direct {
			if src.render {
				directPagesRendered = append(directPagesRendered, src.url)
			} else {
				directPages = append(directPages, src.url)
			}
		} else {
			sitemapSources = append(sitemapSources, src)
		}
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(3)
	for _, src := range sitemapSources {
		if gctx.Err() != nil {
			break
		}

		if checkTTL && s.pageCache != nil {
			lastFetched, err := s.pageCache.LastFetchedForSource(ctx, s.tenantID, src.url)
			if err == nil && lastFetched != nil && time.Since(*lastFetched) < s.interval {
				s.logger.WithField("sitemap", src.url).
					WithField("last_crawl", lastFetched.Format(time.RFC3339)).
					WithField("ttl", s.interval.String()).
					Info("Skipping sitemap — crawled recently")
				continue
			}
		}

		src := src
		g.Go(func() error {
			s.logger.WithField("sitemap", src.url).Info("Starting scheduled crawl")
			result, err := s.crawler.CrawlAndEmbed(gctx, s.tenantID, src.url, src.render)
			if err != nil {
				s.logger.WithError(err).WithField("sitemap", src.url).Warn("Scheduled crawl failed")
				failures := s.health.RecordFailure(src.url)
				if failures >= 3 {
					s.logger.WithField("sitemap", src.url).Warn("Source has 3+ consecutive failures")
				}
			} else {
				s.logger.WithField("sitemap", src.url).
					WithField("embedded", result.Embedded).
					Info("Scheduled crawl completed")
				s.health.RecordSuccess(src.url, result.Embedded)
			}
			return nil
		})
	}
	_ = g.Wait()

	allDirect := append(directPages, directPagesRendered...)
	if len(allDirect) > 0 {
		skipDirect := false
		if checkTTL && s.pageCache != nil {
			lastFetched, err := s.pageCache.LastFetchedForSource(ctx, s.tenantID, directPageSourceRoot)
			if err == nil && lastFetched != nil && time.Since(*lastFetched) < s.interval {
				s.logger.WithField("source", directPageSourceRoot).
					WithField("last_crawl", lastFetched.Format(time.RFC3339)).
					WithField("ttl", s.interval.String()).
					Info("Skipping direct pages — crawled recently")
				skipDirect = true
			}
		}
		if !skipDirect {
			total := len(allDirect)
			s.logger.WithField("pages", total).Info("Starting scheduled direct page crawl")
			var crawlErr error
			if len(directPages) > 0 {
				if err := s.crawler.CrawlPages(ctx, s.tenantID, directPages, false); err != nil {
					crawlErr = err
				}
			}
			if len(directPagesRendered) > 0 {
				if err := s.crawler.CrawlPages(ctx, s.tenantID, directPagesRendered, true); err != nil {
					crawlErr = err
				}
			}
			if crawlErr != nil {
				s.logger.WithError(crawlErr).Warn("Scheduled direct page crawl failed")
				s.health.RecordFailure(directPageSourceRoot)
			} else {
				s.logger.WithField("pages", total).Info("Scheduled direct page crawl completed")
				s.health.RecordSuccess(directPageSourceRoot, total)
			}
		}
	}

	if len(localFiles) > 0 {
		skipLocal := false
		if checkTTL && s.pageCache != nil {
			lastFetched, err := s.pageCache.LastFetchedForSource(ctx, s.tenantID, localFileSourceRoot)
			if err == nil && lastFetched != nil && time.Since(*lastFetched) < s.interval {
				s.logger.WithField("source", localFileSourceRoot).
					WithField("last_crawl", lastFetched.Format(time.RFC3339)).
					WithField("ttl", s.interval.String()).
					Info("Skipping local files — ingested recently")
				skipLocal = true
			}
		}
		if !skipLocal {
			total := len(localFiles)
			s.logger.WithField("files", total).Info("Starting local file ingestion")
			if err := s.crawler.CrawlLocalFiles(ctx, s.tenantID, localFiles); err != nil {
				s.logger.WithError(err).Warn("Local file ingestion failed")
				s.health.RecordFailure(localFileSourceRoot)
			} else {
				s.logger.WithField("files", total).Info("Local file ingestion completed")
				s.health.RecordSuccess(localFileSourceRoot, total)
			}
		}
	}
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
