package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/logging"

	"golang.org/x/net/html"
	"golang.org/x/sync/errgroup"
)

const (
	defaultMinCrawlDelay = 2 * time.Second
	maxCrawlDelay        = 10 * time.Second
	maxPageBytes         = 10 << 20 // 10 MB
	maxErrorBodyBytes    = 1 << 20  // 1 MB
	emptyShellThreshold  = 10       // word count below which content is likely an SPA shell
)

type Page struct {
	URL        string
	Title      string
	Content    string
	LastMod    string
	ChangeFreq string
	Priority   float64
}

type FetchResult struct {
	Title       string
	Content     string
	ContentHash string
	ETag        string
	LastMod     string
	NotModified bool
	RawHTML     []byte // original response body for SPA detection heuristics
	RawSize     int64  // response body length for HEAD-check optimization
}

type DocumentEmbedder interface {
	EmbedDocument(ctx context.Context, url, title, content string) ([]Chunk, error)
}

type KnowledgeStore interface {
	Upsert(ctx context.Context, chunks []Chunk) error
	DeleteBySource(ctx context.Context, tenantID, sourceURL string) error
}

type PageStatus int

const (
	PageFetched     PageStatus = iota
	PageSkipped304             // 304 Not Modified
	PageSkippedHash            // content hash unchanged
	PageSkippedTTL             // lastmod within TTL
	PageFailed
	PageEmbedded
	PageDisallowed
)

type CrawlResult struct {
	Fetched     int
	Skipped304  int
	SkippedHash int
	SkippedTTL  int
	Failed      int
	Embedded    int
	Disallowed  int
}

func (r *CrawlResult) add(s PageStatus) {
	switch s {
	case PageFetched:
		r.Fetched++
	case PageSkipped304:
		r.Skipped304++
	case PageSkippedHash:
		r.SkippedHash++
	case PageSkippedTTL:
		r.SkippedTTL++
	case PageFailed:
		r.Failed++
	case PageEmbedded:
		r.Embedded++
	case PageDisallowed:
		r.Disallowed++
	}
}

const robotsCacheTTL = 1 * time.Hour

type robotsRules struct {
	delay     time.Duration
	disallow  []string // path prefixes
	fetchedAt time.Time
}

// PageRenderer renders JavaScript-heavy pages via a headless browser.
type PageRenderer interface {
	Render(ctx context.Context, pageURL string) (htmlContent string, err error)
	Close()
}

type Crawler struct {
	client            *http.Client
	embedder          DocumentEmbedder
	store             KnowledgeStore
	pageCache         *PageCacheStore
	renderer          PageRenderer
	logger            logging.Logger
	userAgent         string
	minCrawlDelay     time.Duration
	robotsCache       map[string]*robotsRules
	mu                sync.Mutex
	skipURLValidation bool // for tests that use httptest (localhost)
	linkDiscovery     bool
}

type CrawlerOption func(*Crawler)

func WithPageCache(cache *PageCacheStore) CrawlerOption {
	return func(c *Crawler) { c.pageCache = cache }
}

func withSkipURLValidation() CrawlerOption {
	return func(c *Crawler) { c.skipURLValidation = true }
}

func WithLogger(logger logging.Logger) CrawlerOption {
	return func(c *Crawler) { c.logger = logger }
}

func WithMinCrawlDelay(d time.Duration) CrawlerOption {
	return func(c *Crawler) { c.minCrawlDelay = d }
}

func WithRenderer(r PageRenderer) CrawlerOption {
	return func(c *Crawler) { c.renderer = r }
}

func WithLinkDiscovery(enabled bool) CrawlerOption {
	return func(c *Crawler) { c.linkDiscovery = enabled }
}

func (c *Crawler) Close() {
	if c.renderer != nil {
		c.renderer.Close()
	}
}

func NewCrawler(client *http.Client, embedder DocumentEmbedder, store KnowledgeStore, opts ...CrawlerOption) (*Crawler, error) {
	if embedder == nil {
		return nil, errors.New("embedder is required")
	}
	if store == nil {
		return nil, errors.New("store is required")
	}
	if client == nil {
		client = &http.Client{
			Timeout:   30 * time.Second,
			Transport: NewSSRFSafeTransport(),
		}
	}
	c := &Crawler{
		client:        client,
		embedder:      embedder,
		store:         store,
		userAgent:     "SkipperBot/1.0",
		minCrawlDelay: defaultMinCrawlDelay,
		robotsCache:   make(map[string]*robotsRules),
	}
	for _, opt := range opts {
		opt(c)
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("stopped after 5 redirects")
		}
		if !c.skipURLValidation {
			if _, err := validateCrawlURL(req.URL.String()); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			req.Header.Del("If-None-Match")
			req.Header.Del("If-Modified-Since")
		}
		return nil
	}
	return c, nil
}

func (c *Crawler) CrawlSitemap(ctx context.Context, sitemapURL string) ([]Page, error) {
	if sitemapURL == "" {
		return nil, errors.New("sitemap url is required")
	}
	const maxSitemapFetches = 500
	queue := []string{sitemapURL}
	visited := make(map[string]bool)
	var pages []Page

	for len(queue) > 0 {
		if len(visited) >= maxSitemapFetches {
			break
		}
		current := queue[0]
		queue = queue[1:]
		if visited[current] {
			continue
		}
		visited[current] = true

		if !c.skipURLValidation {
			if _, err := validateCrawlURL(current); err != nil {
				if c.logger != nil {
					c.logger.WithField("url", current).WithField("error", err.Error()).Warn("Sub-sitemap URL blocked by SSRF check, skipping")
				}
				continue
			}
		}

		data, err := c.fetchURL(ctx, current)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if c.logger != nil {
				c.logger.WithField("url", current).WithError(err).Warn("Failed to fetch sub-sitemap, continuing")
			}
			continue
		}
		sitemapLinks, pageLinks, err := parseSitemapXML(data)
		if err != nil {
			if c.logger != nil {
				c.logger.WithField("url", current).WithError(err).Warn("Failed to parse sub-sitemap, continuing")
			}
			continue
		}
		queue = append(queue, sitemapLinks...)
		pages = append(pages, pageLinks...)
	}

	const maxPagesPerSitemap = 5000
	if len(pages) > maxPagesPerSitemap {
		if c.logger != nil {
			c.logger.WithField("sitemap", sitemapURL).WithField("found", len(pages)).WithField("cap", maxPagesPerSitemap).Warn("Sitemap page count exceeds limit, truncating")
		}
		pages = pages[:maxPagesPerSitemap]
	}

	return pages, nil
}

func (c *Crawler) FetchPage(ctx context.Context, pageURL string) (string, string, error) {
	if pageURL == "" {
		return "", "", errors.New("page url is required")
	}
	data, err := c.fetchURL(ctx, pageURL)
	if err != nil {
		return "", "", fmt.Errorf("fetch page %s: %w", pageURL, err)
	}
	title, content := extractContent(data, pageURL)
	return title, content, nil
}

func (c *Crawler) CrawlAndEmbed(ctx context.Context, tenantID, sitemapURL string, render bool) (*CrawlResult, error) {
	crawlStart := time.Now()
	defer func() {
		crawlDuration.WithLabelValues(normalizeMetricSource(sitemapURL)).Observe(time.Since(crawlStart).Seconds())
	}()

	if strings.TrimSpace(tenantID) == "" {
		return nil, errors.New("tenant id is required")
	}

	pages, err := c.CrawlSitemap(ctx, sitemapURL)
	if err != nil {
		return nil, err
	}
	base, err := url.Parse(sitemapURL)
	if err != nil {
		return nil, fmt.Errorf("parse sitemap url: %w", err)
	}
	rules := c.getRobotsRules(ctx, base)
	crawlDelay := rules.delay

	// Sort pages by priority descending so the most important pages are
	// crawled first when time or context is limited.
	sort.SliceStable(pages, func(i, j int) bool {
		return pages[i].Priority > pages[j].Priority
	})

	var result CrawlResult
	var resultMu sync.Mutex

	var discovered *linkSet
	if c.linkDiscovery {
		discovered = newLinkSet()
	}

	// Track all known URLs (sitemap + discovered) to avoid re-processing.
	known := make(map[string]bool, len(pages))
	for _, p := range pages {
		known[p.URL] = true
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(3)

	for i, page := range pages {
		if gctx.Err() != nil {
			break
		}

		if !c.isAllowedByRobots(rules, page.URL) {
			if c.logger != nil {
				c.logger.WithField("url", page.URL).Info("Skipping page disallowed by robots.txt")
			}
			result.Disallowed++
			continue
		}

		// Rate-limit dispatch from the main goroutine
		if i > 0 && crawlDelay > 0 {
			timer := time.NewTimer(crawlDelay)
			select {
			case <-gctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}

		page := page
		g.Go(func() error {
			status, err := c.processPage(gctx, tenantID, sitemapURL, page.URL, page.LastMod, "sitemap", render, discovered)
			resultMu.Lock()
			result.add(status)
			resultMu.Unlock()
			return err
		})
	}

	if err := g.Wait(); err != nil {
		return &result, err
	}

	// Process discovered links not in the sitemap.
	if discovered != nil {
		var newPages []string
		for _, link := range discovered.list() {
			if known[link] {
				continue
			}
			if !c.isAllowedByRobots(rules, link) {
				continue
			}
			if !c.skipURLValidation {
				if _, valErr := validateCrawlURL(link); valErr != nil {
					continue
				}
			}
			newPages = append(newPages, link)
			if len(newPages) >= maxDiscoveredPages {
				break
			}
		}
		if len(newPages) > 0 {
			linkDiscoveryTotal.Add(float64(len(newPages)))
			if c.logger != nil {
				c.logger.WithField("count", len(newPages)).Info("Processing discovered links")
			}
		}
		g2, gctx2 := errgroup.WithContext(ctx)
		g2.SetLimit(3)
		for i, link := range newPages {
			if gctx2.Err() != nil {
				break
			}
			if i > 0 && crawlDelay > 0 {
				timer := time.NewTimer(crawlDelay)
				select {
				case <-gctx2.Done():
					timer.Stop()
				case <-timer.C:
				}
			}
			link := link
			g2.Go(func() error {
				status, err := c.processPage(gctx2, tenantID, sitemapURL, link, "", "discovered", render, nil)
				resultMu.Lock()
				result.add(status)
				resultMu.Unlock()
				return err
			})
		}
		_ = g2.Wait()
	}

	if c.logger != nil {
		c.logger.
			WithField("sitemap", sitemapURL).
			WithField("embedded", result.Embedded).
			WithField("skipped_304", result.Skipped304).
			WithField("skipped_hash", result.SkippedHash).
			WithField("skipped_ttl", result.SkippedTTL).
			WithField("failed", result.Failed).
			WithField("disallowed", result.Disallowed).
			Info("Crawl cycle summary")
	}

	crawlPagesTotal.WithLabelValues("embedded").Add(float64(result.Embedded))
	crawlPagesTotal.WithLabelValues("skipped_304").Add(float64(result.Skipped304))
	crawlPagesTotal.WithLabelValues("skipped_hash").Add(float64(result.SkippedHash))
	crawlPagesTotal.WithLabelValues("skipped_ttl").Add(float64(result.SkippedTTL))
	crawlPagesTotal.WithLabelValues("failed").Add(float64(result.Failed))
	crawlPagesTotal.WithLabelValues("disallowed").Add(float64(result.Disallowed))

	return &result, nil
}

func (c *Crawler) CrawlPages(ctx context.Context, tenantID string, pageURLs []string, render bool) error {
	if strings.TrimSpace(tenantID) == "" {
		return errors.New("tenant id is required")
	}
	if len(pageURLs) == 0 {
		return nil
	}

	const sourceRoot = "pagelist://direct"
	var currentHost string
	var rules *robotsRules
	var crawlDelay time.Duration

	for i, pageURL := range pageURLs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		parsed, parseErr := url.Parse(pageURL)
		if parseErr != nil {
			if c.logger != nil {
				c.logger.WithField("url", pageURL).WithField("error", parseErr.Error()).Warn("Invalid page URL, skipping")
			}
			continue
		}
		if parsed.Host != currentHost {
			currentHost = parsed.Host
			rules = c.getRobotsRules(ctx, parsed)
			crawlDelay = rules.delay
		}

		if !c.isAllowedByRobots(rules, pageURL) {
			if c.logger != nil {
				c.logger.WithField("url", pageURL).Info("Skipping page disallowed by robots.txt")
			}
			continue
		}

		if i > 0 && crawlDelay > 0 {
			timer := time.NewTimer(crawlDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}

		if _, err := c.processPage(ctx, tenantID, sourceRoot, pageURL, "", "pagelist", render, nil); err != nil {
			return err
		}
	}
	return nil
}

// processPage handles conditional fetch, hash check, embed, and upsert for a single page.
// When render is true and a PageRenderer is available, pages are fetched via headless browser.
// Returns non-nil error only on context cancellation; page-level errors are logged and skipped.
func (c *Crawler) processPage(ctx context.Context, tenantID, sourceRoot, pageURL, lastMod, sourceType string, render bool, discovered *linkSet) (PageStatus, error) {
	if !c.skipURLValidation {
		if _, err := validateCrawlURL(pageURL); err != nil {
			if c.logger != nil {
				c.logger.WithField("url", pageURL).WithField("error", err.Error()).Warn("URL blocked by SSRF check, skipping")
			}
			return PageDisallowed, nil
		}
	}

	var cached *PageCache
	if c.pageCache != nil {
		cached, _ = c.pageCache.Get(ctx, tenantID, pageURL)
	}

	if cached != nil && lastMod != "" {
		if lm, parseErr := time.Parse(time.RFC3339, lastMod); parseErr == nil {
			if cached.LastFetchedAt.After(lm) {
				return PageSkippedTTL, nil
			}
		}
	}

	// Rendered fetch path: use headless browser when explicitly requested.
	// Skip Chrome if a HEAD request shows the page size hasn't changed.
	if render && c.renderer != nil {
		if c.skipRenderViaHEAD(ctx, pageURL, cached) {
			headCheckSkipsTotal.Inc()
			if c.logger != nil {
				c.logger.WithField("url", pageURL).Debug("Skipping render: HEAD size matches cached raw_size")
			}
			return PageSkippedHash, nil
		}
		result, renderErr := c.fetchRendered(ctx, pageURL)
		if renderErr != nil {
			if ctx.Err() != nil {
				return PageFailed, ctx.Err()
			}
			if c.logger != nil {
				c.logger.WithField("url", pageURL).WithField("error", renderErr.Error()).Warn("Rendered fetch failed, falling back to plain")
			}
		} else {
			return c.finishPage(ctx, tenantID, sourceRoot, pageURL, sourceType, result, cached)
		}
	}

	result, fetchErr := c.fetchPageConditional(ctx, pageURL, cached)
	if fetchErr != nil {
		if ctx.Err() != nil {
			return PageFailed, ctx.Err()
		}
		if c.logger != nil {
			c.logger.WithField("url", pageURL).WithField("error", fetchErr.Error()).Warn("Page fetch failed, skipping")
		}
		return PageFailed, nil
	}

	if discovered != nil && len(result.RawHTML) > 0 {
		discovered.addAll(extractLinks(result.RawHTML, pageURL))
	}

	// Auto-detect SPA pages and retry with renderer if available.
	// needsRendering checks raw HTML for framework markers (React, Next.js, etc.);
	// looksLikeEmptyShell catches pages where extraction produced almost no text.
	if !render && c.renderer != nil && !result.NotModified &&
		(needsRendering(result.RawHTML) || looksLikeEmptyShell(result.Content)) {
		if c.skipRenderViaHEAD(ctx, pageURL, cached) {
			headCheckSkipsTotal.Inc()
			if c.logger != nil {
				c.logger.WithField("url", pageURL).Debug("Skipping auto-render: HEAD size matches cached raw_size")
			}
		} else {
			if c.logger != nil {
				c.logger.WithField("url", pageURL).Info("SPA detected, retrying with renderer")
			}
			renderPagesTotal.WithLabelValues("auto_detect").Inc()
			rendered, renderErr := c.fetchRendered(ctx, pageURL)
			if renderErr == nil && !looksLikeEmptyShell(rendered.Content) {
				return c.finishPage(ctx, tenantID, sourceRoot, pageURL, sourceType, rendered, cached)
			}
		}
	}

	return c.finishPage(ctx, tenantID, sourceRoot, pageURL, sourceType, result, cached)
}

// finishPage handles 304/hash-skip checks, embedding, upsert, and cache update.
func (c *Crawler) finishPage(ctx context.Context, tenantID, sourceRoot, pageURL, sourceType string, result FetchResult, cached *PageCache) (PageStatus, error) {
	now := time.Now().UTC()

	if result.NotModified {
		if c.pageCache != nil && cached != nil {
			_ = c.pageCache.Upsert(ctx, PageCache{
				TenantID:      tenantID,
				SourceRoot:    sourceRoot,
				PageURL:       pageURL,
				ContentHash:   cached.ContentHash,
				ETag:          cached.ETag,
				LastModified:  cached.LastModified,
				RawSize:       cached.RawSize,
				LastFetchedAt: now,
			})
		}
		return PageSkipped304, nil
	}

	if cached != nil && cached.ContentHash != "" && result.ContentHash == cached.ContentHash {
		if c.pageCache != nil {
			_ = c.pageCache.Upsert(ctx, PageCache{
				TenantID:      tenantID,
				SourceRoot:    sourceRoot,
				PageURL:       pageURL,
				ContentHash:   result.ContentHash,
				ETag:          result.ETag,
				LastModified:  result.LastMod,
				RawSize:       result.RawSize,
				LastFetchedAt: now,
			})
		}
		return PageSkippedHash, nil
	}

	chunks, embedErr := c.embedder.EmbedDocument(ctx, pageURL, result.Title, result.Content)
	if embedErr != nil {
		if ctx.Err() != nil {
			return PageFailed, ctx.Err()
		}
		if c.logger != nil {
			c.logger.WithField("url", pageURL).WithField("error", embedErr.Error()).Warn("Embed failed, skipping")
		}
		return PageFailed, nil
	}
	applyIngestionMetadata(chunks, tenantID, sourceRoot, pageURL, sourceType, now, nil)
	if err := c.store.Upsert(ctx, chunks); err != nil {
		if ctx.Err() != nil {
			return PageFailed, ctx.Err()
		}
		if c.logger != nil {
			c.logger.WithField("url", pageURL).WithField("error", err.Error()).Warn("Upsert failed, skipping")
		}
		return PageFailed, nil
	}

	if c.pageCache != nil {
		_ = c.pageCache.Upsert(ctx, PageCache{
			TenantID:      tenantID,
			SourceRoot:    sourceRoot,
			PageURL:       pageURL,
			ContentHash:   result.ContentHash,
			ETag:          result.ETag,
			LastModified:  result.LastMod,
			RawSize:       result.RawSize,
			LastFetchedAt: now,
		})
	}
	return PageEmbedded, nil
}

// skipRenderViaHEAD returns true when a lightweight HEAD request confirms
// that a page's Content-Length matches the cached raw_size, suggesting the
// page hasn't changed and Chrome can be skipped.
func (c *Crawler) skipRenderViaHEAD(ctx context.Context, pageURL string, cached *PageCache) bool {
	if cached == nil || cached.ContentHash == "" || cached.RawSize <= 0 {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, pageURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	cl := resp.ContentLength
	if cl <= 0 {
		return false
	}
	return cl == cached.RawSize
}

// fetchRendered uses the headless browser to render a page and extract content.
func (c *Crawler) fetchRendered(ctx context.Context, pageURL string) (FetchResult, error) {
	renderStart := time.Now()
	htmlContent, err := c.renderer.Render(ctx, pageURL)
	renderDuration.Observe(time.Since(renderStart).Seconds())
	if err != nil {
		renderPagesTotal.WithLabelValues("error").Inc()
		return FetchResult{}, fmt.Errorf("render page %s: %w", pageURL, err)
	}
	renderPagesTotal.WithLabelValues("success").Inc()

	title, content := extractContent([]byte(htmlContent), pageURL)
	return FetchResult{
		Title:       title,
		Content:     content,
		ContentHash: contentHash(content),
	}, nil
}

func (c *Crawler) fetchPageConditional(ctx context.Context, pageURL string, cached *PageCache) (FetchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return FetchResult{}, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	if cached != nil {
		if cached.ETag != "" {
			req.Header.Set("If-None-Match", cached.ETag)
		}
		if cached.LastModified != "" {
			req.Header.Set("If-Modified-Since", cached.LastModified)
		}
	}

	resp, err := c.doWithRetry(ctx, req)
	if err != nil {
		return FetchResult{}, fmt.Errorf("fetch page %s: %w", pageURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return FetchResult{NotModified: true}, nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return FetchResult{}, fmt.Errorf("fetch page %s: unexpected status %s: %s", pageURL, resp.Status, strings.TrimSpace(string(body)))
	}

	ct := resp.Header.Get("Content-Type")
	isHTML := ct == "" || strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
	isPlain := strings.Contains(ct, "text/plain") || strings.Contains(ct, "text/markdown") || strings.Contains(ct, "text/x-markdown")
	if !isHTML && !isPlain {
		return FetchResult{}, fmt.Errorf("unsupported content type %q for %s", ct, pageURL)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return FetchResult{}, fmt.Errorf("read page %s: %w", pageURL, err)
	}

	var title, content string
	if isPlain {
		title, content = extractPlainContent(data, pageURL)
	} else {
		title, content = extractContent(data, pageURL)
	}

	return FetchResult{
		Title:       title,
		Content:     content,
		ContentHash: contentHash(content),
		ETag:        resp.Header.Get("ETag"),
		LastMod:     resp.Header.Get("Last-Modified"),
		RawHTML:     data,
		RawSize:     int64(len(data)),
	}, nil
}

func contentHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}

const maxRetries = 3

func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// doWithRetry executes an HTTP request with exponential backoff on transient errors.
// The caller must NOT read or close the response body before calling this —
// on retry the previous response is discarded.
func (c *Crawler) doWithRetry(ctx context.Context, req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<(attempt-1)) * time.Second
			if resp != nil {
				if ra := resp.Header.Get("Retry-After"); ra != "" {
					if secs, parseErr := strconv.Atoi(ra); parseErr == nil && secs > 0 && secs <= 120 {
						backoff = time.Duration(secs) * time.Second
					}
				}
				resp.Body.Close()
			}
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		resp, err = c.client.Do(req)
		if err != nil {
			if !isRetryableError(err) {
				return nil, err
			}
			continue
		}
		if !isRetryableStatus(resp.StatusCode) {
			return resp, nil
		}
	}
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Crawler) fetchURL(ctx context.Context, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.doWithRetry(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return nil, fmt.Errorf("unexpected status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	return io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
}

func parseSitemapXML(data []byte) ([]string, []Page, error) {
	var index sitemapIndex
	if err := xml.Unmarshal(data, &index); err == nil && len(index.Sitemaps) > 0 {
		var links []string
		for _, sm := range index.Sitemaps {
			if sm.Location != "" {
				links = append(links, strings.TrimSpace(sm.Location))
			}
		}
		return links, nil, nil
	}

	var set urlSet
	if err := xml.Unmarshal(data, &set); err != nil {
		return nil, nil, err
	}
	var pages []Page
	for _, entry := range set.URLs {
		if entry.Location != "" {
			pages = append(pages, Page{
				URL:        strings.TrimSpace(entry.Location),
				LastMod:    strings.TrimSpace(entry.LastMod),
				ChangeFreq: strings.TrimSpace(entry.ChangeFreq),
				Priority:   entry.Priority,
			})
		}
	}
	return nil, pages, nil
}

func (c *Crawler) getRobotsRules(ctx context.Context, siteURL *url.URL) *robotsRules {
	if siteURL == nil {
		return &robotsRules{delay: c.minCrawlDelay}
	}
	base := siteURL.Scheme + "://" + siteURL.Host

	c.mu.Lock()
	if rules, ok := c.robotsCache[base]; ok && time.Since(rules.fetchedAt) < robotsCacheTTL {
		c.mu.Unlock()
		return rules
	}
	c.mu.Unlock()

	robotsURL := base + "/robots.txt"
	body, err := c.fetchURL(ctx, robotsURL)
	if err != nil {
		rules := &robotsRules{delay: c.minCrawlDelay, fetchedAt: time.Now()}
		c.mu.Lock()
		c.robotsCache[base] = rules
		c.mu.Unlock()
		return rules
	}
	rules := parseRobotsTxt(string(body), c.userAgent)
	rules.fetchedAt = time.Now()
	if rules.delay < c.minCrawlDelay {
		rules.delay = c.minCrawlDelay
	}
	if rules.delay > maxCrawlDelay {
		rules.delay = maxCrawlDelay
	}

	c.mu.Lock()
	c.robotsCache[base] = rules
	c.mu.Unlock()

	return rules
}

func (c *Crawler) isAllowedByRobots(rules *robotsRules, pageURL string) bool {
	if rules == nil || len(rules.disallow) == 0 {
		return true
	}
	parsed, err := url.Parse(pageURL)
	if err != nil {
		return false
	}
	path := parsed.Path
	for _, prefix := range rules.disallow {
		if strings.HasPrefix(path, prefix) {
			return false
		}
	}
	return true
}

type sitemapIndex struct {
	Sitemaps []sitemapEntry `xml:"sitemap"`
}

type sitemapEntry struct {
	Location string `xml:"loc"`
}

type urlSet struct {
	URLs []urlEntry `xml:"url"`
}

type urlEntry struct {
	Location   string  `xml:"loc"`
	LastMod    string  `xml:"lastmod"`
	ChangeFreq string  `xml:"changefreq"`
	Priority   float64 `xml:"priority"`
}

func normalizeMetricSource(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return "unknown"
}

// matchesUA returns true if the robots.txt agent token matches the crawler's
// user-agent using case-insensitive prefix matching per RFC 9309 §2.2.1.
// e.g. "skipperbot" matches "skipperbot/1.0".
func matchesUA(robotsAgent, crawlerUA string) bool {
	return strings.HasPrefix(crawlerUA, robotsAgent)
}

func parseRobotsTxt(body, userAgent string) *robotsRules {
	userAgent = strings.ToLower(userAgent)

	var wildcardRules robotsRules
	var specificRules robotsRules
	var matchedSpecific bool
	var currentAgents []string
	var lastDirective string

	lines := strings.Split(body, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		directive := strings.ToLower(strings.TrimSpace(parts[0]))
		value := strings.TrimSpace(parts[1])
		switch directive {
		case "user-agent":
			// Per RFC 9309: consecutive user-agent lines form a single group.
			if lastDirective == "user-agent" {
				currentAgents = append(currentAgents, strings.ToLower(value))
			} else {
				currentAgents = []string{strings.ToLower(value)}
			}
		case "crawl-delay":
			if len(currentAgents) == 0 {
				continue
			}
			parsed, err := time.ParseDuration(value + "s")
			if err != nil {
				continue
			}
			for _, agent := range currentAgents {
				if matchesUA(agent, userAgent) {
					specificRules.delay = parsed
					matchedSpecific = true
				} else if agent == "*" {
					wildcardRules.delay = parsed
				}
			}
		case "disallow":
			if len(currentAgents) == 0 || value == "" {
				continue
			}
			for _, agent := range currentAgents {
				if matchesUA(agent, userAgent) {
					specificRules.disallow = append(specificRules.disallow, value)
					matchedSpecific = true
				} else if agent == "*" {
					wildcardRules.disallow = append(wildcardRules.disallow, value)
				}
			}
		}
		lastDirective = directive
	}

	if matchedSpecific {
		return &specificRules
	}
	return &wildcardRules
}

func extractTitle(node *html.Node) string {
	var titleNode *html.Node
	var findTitle func(*html.Node)
	findTitle = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "title" {
			titleNode = n
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if titleNode != nil {
				return
			}
			findTitle(child)
		}
	}
	findTitle(node)
	if titleNode == nil {
		return ""
	}
	// Walk all text nodes inside <title> to handle <title>Part <span>Two</span></title>
	var buf strings.Builder
	var collectText func(*html.Node)
	collectText = func(n *html.Node) {
		if n.Type == html.TextNode {
			buf.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			collectText(child)
		}
	}
	collectText(titleNode)
	return strings.TrimSpace(buf.String())
}

func extractReadableText(node *html.Node) string {
	var builder strings.Builder

	var walker func(*html.Node)
	walker = func(n *html.Node) {
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			switch tag {
			case "script", "style", "noscript", "nav", "footer", "header", "aside", "form", "template":
				return
			case "h1", "h2", "h3", "h4", "h5", "h6":
				builder.WriteString("\n\n")
				builder.WriteString(strings.Repeat("#", headingLevel(tag)))
				builder.WriteString(" ")
			case "p", "div", "section", "article", "li", "pre", "blockquote":
				builder.WriteString("\n\n")
			}
			// Skip hidden elements and aria-hidden="true"
			if hasAttr(n, "hidden") || attrVal(n, "aria-hidden") == "true" {
				return
			}
			// Skip complementary/banner landmark roles
			role := attrVal(n, "role")
			if role == "complementary" || role == "banner" || role == "navigation" {
				return
			}
		}
		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				builder.WriteString(text)
				builder.WriteString(" ")
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walker(child)
		}
	}
	walker(node)

	return normalizeContent(builder.String())
}

func hasAttr(n *html.Node, key string) bool {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return true
		}
	}
	return false
}

func attrVal(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func headingLevel(tag string) int {
	switch tag {
	case "h1":
		return 1
	case "h2":
		return 2
	case "h3":
		return 3
	case "h4":
		return 4
	case "h5":
		return 5
	case "h6":
		return 6
	default:
		return 1
	}
}

func normalizeContent(content string) string {
	lines := strings.Split(content, "\n")
	var cleaned []string
	blank := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if !blank {
				cleaned = append(cleaned, "")
				blank = true
			}
			continue
		}
		blank = false
		cleaned = append(cleaned, trimmed)
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

const maxDiscoveredPages = 500
const maxLinksPerPage = 200

type linkSet struct {
	mu   sync.Mutex
	urls map[string]bool
}

func newLinkSet() *linkSet {
	return &linkSet{urls: make(map[string]bool)}
}

func (ls *linkSet) addAll(urls []string) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	for _, u := range urls {
		ls.urls[u] = true
	}
}

func (ls *linkSet) list() []string {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	out := make([]string, 0, len(ls.urls))
	for u := range ls.urls {
		out = append(out, u)
	}
	return out
}

// extractLinks parses HTML and returns unique same-domain links.
func extractLinks(data []byte, baseURL string) []string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil
	}
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return nil
	}

	seen := make(map[string]bool)
	var links []string

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, attr := range n.Attr {
				if attr.Key != "href" {
					continue
				}
				href := strings.TrimSpace(attr.Val)
				if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:") || strings.HasPrefix(href, "mailto:") {
					continue
				}
				resolved, resolveErr := base.Parse(href)
				if resolveErr != nil {
					continue
				}
				// Same host only
				if resolved.Host != base.Host {
					continue
				}
				resolved.Fragment = ""
				resolved.RawQuery = ""
				canonical := resolved.String()
				if !seen[canonical] {
					seen[canonical] = true
					links = append(links, canonical)
					if len(links) >= maxLinksPerPage {
						return
					}
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return links
}
