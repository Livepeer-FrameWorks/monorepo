package knowledge

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

type Page struct {
	URL     string
	Title   string
	Content string
}

type DocumentEmbedder interface {
	EmbedDocument(ctx context.Context, url, title, content string) ([]Chunk, error)
}

type KnowledgeStore interface {
	Upsert(ctx context.Context, chunks []Chunk) error
	DeleteBySource(ctx context.Context, tenantID, sourceURL string) error
}

type Crawler struct {
	client      *http.Client
	embedder    DocumentEmbedder
	store       KnowledgeStore
	userAgent   string
	robotsCache map[string]time.Duration
	mu          sync.Mutex
}

func NewCrawler(client *http.Client, embedder DocumentEmbedder, store KnowledgeStore) (*Crawler, error) {
	if embedder == nil {
		return nil, errors.New("embedder is required")
	}
	if store == nil {
		return nil, errors.New("store is required")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Crawler{
		client:      client,
		embedder:    embedder,
		store:       store,
		userAgent:   "SkipperBot/1.0",
		robotsCache: make(map[string]time.Duration),
	}, nil
}

func (c *Crawler) CrawlSitemap(ctx context.Context, sitemapURL string) ([]Page, error) {
	if sitemapURL == "" {
		return nil, errors.New("sitemap url is required")
	}
	queue := []string{sitemapURL}
	visited := make(map[string]bool)
	var pages []Page

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if visited[current] {
			continue
		}
		visited[current] = true

		data, err := c.fetchURL(ctx, current)
		if err != nil {
			return nil, fmt.Errorf("fetch sitemap %s: %w", current, err)
		}
		sitemapLinks, urlLinks, err := parseSitemapXML(data)
		if err != nil {
			return nil, fmt.Errorf("parse sitemap %s: %w", current, err)
		}
		queue = append(queue, sitemapLinks...)
		for _, link := range urlLinks {
			pages = append(pages, Page{URL: link})
		}
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
	node, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return "", "", fmt.Errorf("parse html %s: %w", pageURL, err)
	}
	return extractTitle(node), extractReadableText(node), nil
}

func (c *Crawler) CrawlAndEmbed(ctx context.Context, tenantID, sitemapURL string) error {
	if strings.TrimSpace(tenantID) == "" {
		return errors.New("tenant id is required")
	}

	pages, err := c.CrawlSitemap(ctx, sitemapURL)
	if err != nil {
		return err
	}
	base, err := url.Parse(sitemapURL)
	if err != nil {
		return fmt.Errorf("parse sitemap url: %w", err)
	}
	crawlDelay := c.getCrawlDelay(ctx, base)

	for i, page := range pages {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
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

		title, content, err := c.FetchPage(ctx, page.URL)
		if err != nil {
			return err
		}
		chunks, err := c.embedder.EmbedDocument(ctx, page.URL, title, content)
		if err != nil {
			return err
		}
		for i := range chunks {
			chunks[i].TenantID = tenantID
		}
		if err := c.store.Upsert(ctx, chunks); err != nil {
			return err
		}
	}

	return nil
}

func (c *Crawler) fetchURL(ctx context.Context, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	return io.ReadAll(resp.Body)
}

func parseSitemapXML(data []byte) ([]string, []string, error) {
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
	var urls []string
	for _, entry := range set.URLs {
		if entry.Location != "" {
			urls = append(urls, strings.TrimSpace(entry.Location))
		}
	}
	return nil, urls, nil
}

func (c *Crawler) getCrawlDelay(ctx context.Context, sitemapURL *url.URL) time.Duration {
	if sitemapURL == nil {
		return 0
	}
	base := sitemapURL.Scheme + "://" + sitemapURL.Host

	c.mu.Lock()
	if delay, ok := c.robotsCache[base]; ok {
		c.mu.Unlock()
		return delay
	}
	c.mu.Unlock()

	robotsURL := base + "/robots.txt"
	body, err := c.fetchURL(ctx, robotsURL)
	if err != nil {
		return 0
	}
	delay := parseCrawlDelay(string(body), c.userAgent)

	c.mu.Lock()
	c.robotsCache[base] = delay
	c.mu.Unlock()

	return delay
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
	Location string `xml:"loc"`
}

func parseCrawlDelay(body, userAgent string) time.Duration {
	var delay time.Duration
	var currentAgents []string
	var matchedSpecific bool
	userAgent = strings.ToLower(userAgent)

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
			currentAgents = []string{strings.ToLower(value)}
		case "crawl-delay":
			if len(currentAgents) == 0 {
				continue
			}
			parsed, err := time.ParseDuration(value + "s")
			if err != nil {
				continue
			}
			for _, agent := range currentAgents {
				if agent == userAgent {
					delay = parsed
					matchedSpecific = true
					break
				}
				if agent == "*" && !matchedSpecific {
					delay = parsed
				}
			}
		}
	}

	return delay
}

func extractTitle(node *html.Node) string {
	var title string
	var walker func(*html.Node)
	walker = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "title" && n.FirstChild != nil {
			title = strings.TrimSpace(n.FirstChild.Data)
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if title != "" {
				return
			}
			walker(child)
		}
	}
	walker(node)
	return title
}

func extractReadableText(node *html.Node) string {
	var builder strings.Builder

	var walker func(*html.Node)
	walker = func(n *html.Node) {
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			switch tag {
			case "script", "style", "noscript", "nav", "footer", "header", "aside":
				return
			case "h1", "h2", "h3", "h4", "h5", "h6":
				builder.WriteString("\n\n")
				builder.WriteString(strings.Repeat("#", headingLevel(tag)))
				builder.WriteString(" ")
			case "p", "div", "section", "article", "li", "pre", "blockquote":
				builder.WriteString("\n\n")
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
