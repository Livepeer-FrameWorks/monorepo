package knowledge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/auth"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type AdminAPI struct {
	store    *Store
	embedder *Embedder
	crawler  *Crawler
	logger   logging.Logger
	now      func() time.Time

	jobsMu sync.Mutex
	jobs   map[string]*CrawlJob
}

type CrawlJob struct {
	ID         string     `json:"id"`
	SitemapURL string     `json:"sitemap_url"`
	TenantID   string     `json:"tenant_id"`
	Status     string     `json:"status"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type crawlRequest struct {
	SitemapURL string `json:"sitemap_url"`
	TenantID   string `json:"tenant_id"`
}

type pageRequest struct {
	URL      string `json:"url"`
	Title    string `json:"title"`
	Content  string `json:"content"`
	TenantID string `json:"tenant_id"`
}

type uploadResponse struct {
	SourceURL string `json:"source_url"`
	Chunks    int    `json:"chunks"`
}

type pageResponse struct {
	SourceURL string `json:"source_url"`
	Chunks    int    `json:"chunks"`
}

func NewAdminAPI(store *Store, embedder *Embedder, crawler *Crawler, logger logging.Logger) (*AdminAPI, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	if embedder == nil {
		return nil, errors.New("embedder is required")
	}
	if crawler == nil {
		return nil, errors.New("crawler is required")
	}
	return &AdminAPI{
		store:    store,
		embedder: embedder,
		crawler:  crawler,
		logger:   logger,
		now:      time.Now,
		jobs:     make(map[string]*CrawlJob),
	}, nil
}

func (a *AdminAPI) RegisterRoutes(router *gin.Engine, jwtSecret []byte) {
	group := router.Group("/api/skipper/admin")
	group.Use(auth.JWTAuthMiddleware(jwtSecret))
	group.Use(operatorOnlyMiddleware())

	group.POST("/crawl", a.handleCrawl)
	group.POST("/pages", a.handlePages)
	group.POST("/upload", a.handleUpload)
	group.GET("/sources", a.handleListSources)
	group.DELETE("/sources/:url", a.handleDeleteSource)
}

func (a *AdminAPI) handleCrawl(c *gin.Context) {
	var req crawlRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(req.SitemapURL) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sitemap_url is required"})
		return
	}
	tenantID, ok := resolveTenantID(c, req.TenantID)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id is required"})
		return
	}

	jobID := uuid.NewString()
	job := &CrawlJob{
		ID:         jobID,
		SitemapURL: req.SitemapURL,
		TenantID:   tenantID,
		Status:     "running",
		StartedAt:  a.now().UTC(),
	}
	a.saveJob(job)

	go a.runCrawl(jobID, tenantID, req.SitemapURL)

	c.JSON(http.StatusAccepted, gin.H{"job_id": jobID})
}

func (a *AdminAPI) handlePages(c *gin.Context) {
	var req pageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url is required"})
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content is required"})
		return
	}

	tenantID, ok := resolveTenantID(c, req.TenantID)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id is required"})
		return
	}

	chunks, err := a.embedder.EmbedDocument(c.Request.Context(), req.URL, req.Title, req.Content)
	if err != nil {
		a.logger.WithError(err).Warn("Failed to embed admin page")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to embed page"})
		return
	}
	ingestedAt := a.now().UTC()
	applyIngestionMetadata(chunks, tenantID, req.URL, req.URL, "page", ingestedAt, nil)

	if err := a.store.Upsert(c.Request.Context(), chunks); err != nil {
		a.logger.WithError(err).Warn("Failed to store admin page")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store page"})
		return
	}

	c.JSON(http.StatusOK, pageResponse{
		SourceURL: req.URL,
		Chunks:    len(chunks),
	})
}

func (a *AdminAPI) handleUpload(c *gin.Context) {
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is required"})
		return
	}
	defer func() { _ = file.Close() }()

	tenantID, ok := resolveTenantID(c, c.PostForm("tenant_id"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id is required"})
		return
	}

	body, err := io.ReadAll(file)
	if err != nil {
		a.logger.WithError(err).Warn("Failed to read upload")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read upload"})
		return
	}
	content := strings.TrimSpace(string(body))
	if content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file content is empty"})
		return
	}

	filename := "upload"
	if header != nil && header.Filename != "" {
		filename = header.Filename
	}
	sourceURL := fmt.Sprintf("upload://%s", filename)
	extra := map[string]any{"filename": filename}

	chunks, err := a.embedder.EmbedDocument(c.Request.Context(), sourceURL, filename, content)
	if err != nil {
		a.logger.WithError(err).Warn("Failed to embed upload")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to embed upload"})
		return
	}
	ingestedAt := a.now().UTC()
	applyIngestionMetadata(chunks, tenantID, sourceURL, sourceURL, "upload", ingestedAt, extra)

	if err := a.store.Upsert(c.Request.Context(), chunks); err != nil {
		a.logger.WithError(err).Warn("Failed to store upload")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store upload"})
		return
	}

	c.JSON(http.StatusOK, uploadResponse{
		SourceURL: sourceURL,
		Chunks:    len(chunks),
	})
}

func (a *AdminAPI) handleListSources(c *gin.Context) {
	tenantID, ok := resolveTenantID(c, c.Query("tenant_id"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id is required"})
		return
	}
	sources, err := a.store.ListSources(c.Request.Context(), tenantID)
	if err != nil {
		a.logger.WithError(err).Warn("Failed to list knowledge sources")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list sources"})
		return
	}

	type sourceResponse struct {
		SourceURL   string  `json:"source_url"`
		PageCount   int     `json:"page_count"`
		LastCrawlAt *string `json:"last_crawl_at,omitempty"`
	}

	response := make([]sourceResponse, 0, len(sources))
	for _, source := range sources {
		var lastCrawl *string
		if source.LastCrawlAt != nil {
			formatted := source.LastCrawlAt.UTC().Format(time.RFC3339)
			lastCrawl = &formatted
		}
		response = append(response, sourceResponse{
			SourceURL:   source.SourceURL,
			PageCount:   source.PageCount,
			LastCrawlAt: lastCrawl,
		})
	}

	c.JSON(http.StatusOK, response)
}

func (a *AdminAPI) handleDeleteSource(c *gin.Context) {
	tenantID, ok := resolveTenantID(c, c.Query("tenant_id"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id is required"})
		return
	}
	rawURL := c.Param("url")
	if strings.TrimSpace(rawURL) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url is required"})
		return
	}
	sourceURL, err := url.PathUnescape(rawURL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid url"})
		return
	}

	if err := a.store.DeleteBySource(c.Request.Context(), tenantID, sourceURL); err != nil {
		a.logger.WithError(err).Warn("Failed to delete knowledge source")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete source"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func (a *AdminAPI) runCrawl(jobID, tenantID, sitemapURL string) {
	err := a.crawler.CrawlAndEmbed(context.Background(), tenantID, sitemapURL)
	a.jobsMu.Lock()
	job, ok := a.jobs[jobID]
	if ok {
		finished := a.now().UTC()
		job.FinishedAt = &finished
		if err != nil {
			job.Status = "failed"
			job.Error = err.Error()
		} else {
			job.Status = "completed"
		}
	}
	a.jobsMu.Unlock()
	if err != nil {
		a.logger.WithError(err).Warn("Admin crawl failed")
	}
}

func (a *AdminAPI) saveJob(job *CrawlJob) {
	a.jobsMu.Lock()
	a.jobs[job.ID] = job
	a.jobsMu.Unlock()
}

func operatorOnlyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		role := c.GetString(string(ctxkeys.KeyRole))
		if !isOperatorRole(role) {
			c.JSON(http.StatusForbidden, gin.H{"error": "operator access required"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func isOperatorRole(role string) bool {
	switch strings.ToLower(role) {
	case "admin", "operator", "provider", "service":
		return true
	default:
		return false
	}
}

func resolveTenantID(c *gin.Context, explicit string) (string, bool) {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit), true
	}
	if header := strings.TrimSpace(c.GetHeader("X-Tenant-Id")); header != "" {
		return header, true
	}
	if tenantID := strings.TrimSpace(c.GetString(string(ctxkeys.KeyTenantID))); tenantID != "" {
		return tenantID, true
	}
	return "", false
}

func applyIngestionMetadata(chunks []Chunk, tenantID, sourceRoot, pageURL, sourceType string, ingestedAt time.Time, extra map[string]any) {
	ingested := ingestedAt.UTC().Format(time.RFC3339)
	for i := range chunks {
		chunks[i].TenantID = tenantID
		if chunks[i].Metadata == nil {
			chunks[i].Metadata = make(map[string]any)
		}
		for key, value := range extra {
			chunks[i].Metadata[key] = value
		}
		chunks[i].Metadata["source_root"] = sourceRoot
		chunks[i].Metadata["page_url"] = pageURL
		chunks[i].Metadata["source_type"] = sourceType
		chunks[i].Metadata["ingested_at"] = ingested
	}
}
