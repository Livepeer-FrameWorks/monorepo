package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"frameworks/api_consultant/internal/database/skipperdb"
	"frameworks/api_consultant/internal/skipper"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	maxUploadSize       int64 = 10 << 20 // 10 MB
	maxCrawlDuration          = 2 * time.Hour
	maxConcurrentCrawls       = 3
)

var allowedUploadExtensions = map[string]bool{
	".txt":  true,
	".md":   true,
	".html": true,
	".csv":  true,
	".json": true,
	".xml":  true,
}

var (
	crawlSem     = make(chan struct{}, maxConcurrentCrawls)
	activeCrawls sync.Map // jobID → context.CancelFunc
)

type AdminAPI struct {
	db        *sql.DB
	queries   *skipperdb.Queries
	store     *Store
	embedder  *Embedder
	crawler   *Crawler
	pageCache *PageCacheStore
	health    *HealthTracker
	logger    logging.Logger
	now       func() time.Time
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
	Render     bool   `json:"render,omitempty"`
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

func NewAdminAPI(db *sql.DB, store *Store, embedder *Embedder, crawler *Crawler, pageCache *PageCacheStore, logger logging.Logger) (*AdminAPI, error) {
	if db == nil {
		return nil, errors.New("db is required")
	}
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
		db:        db,
		queries:   skipperdb.New(db),
		store:     store,
		embedder:  embedder,
		crawler:   crawler,
		pageCache: pageCache,
		logger:    logger,
		now:       time.Now,
	}, nil
}

// SetHealth attaches a crawl health tracker to the admin API.
func (a *AdminAPI) SetHealth(h *HealthTracker) {
	a.health = h
}

func (a *AdminAPI) RegisterRoutes(router *gin.Engine, jwtSecret []byte, middleware ...gin.HandlerFunc) {
	group := router.Group("/api/skipper/admin")
	group.Use(auth.JWTAuthMiddleware(jwtSecret))
	for _, mw := range middleware {
		group.Use(mw)
	}
	group.Use(operatorOnlyMiddleware())

	group.POST("/crawl", a.handleCrawl)
	group.GET("/crawl", a.handleListCrawlJobs)
	group.GET("/crawl/:id", a.handleCrawlStatus)
	group.DELETE("/crawl/:id", a.handleCancelCrawl)
	group.POST("/pages", a.handlePages)
	group.POST("/upload", a.handleUpload)
	group.GET("/sources", a.handleListSources)
	group.DELETE("/sources", a.handleDeleteSource)
	group.GET("/health", a.handleHealth)
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
	if _, err := validateCrawlURLWithContext(c.Request.Context(), req.SitemapURL); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid sitemap url: %v", err)})
		return
	}
	tenantID, ok := resolveTenantID(c, req.TenantID)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id is required"})
		return
	}

	// Atomic check-and-insert: only creates the job if no running job exists
	// for this tenant + sitemap, preventing the TOCTOU race of SELECT then INSERT.
	jobID := uuid.NewString()
	startedAt := a.now().UTC()
	rows, err := a.queries.CreateCrawlJob(c.Request.Context(), skipperdb.CreateCrawlJobParams{
		ID: jobID, TenantID: tenantID, SitemapUrl: req.SitemapURL, StartedAt: startedAt,
	})
	if err != nil {
		a.logger.WithError(err).Warn("Failed to create crawl job")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create crawl job"})
		return
	}
	if rows == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "a crawl for this sitemap is already running"})
		return
	}

	go a.runCrawl(jobID, tenantID, req.SitemapURL, req.Render)

	c.JSON(http.StatusAccepted, gin.H{"job_id": jobID})
}

func (a *AdminAPI) handlePages(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 10<<20) // 10 MB
	var req pageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url is required"})
		return
	}
	if _, err := validateCrawlURLWithContext(c.Request.Context(), req.URL); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid url: %v", err)})
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

	if header != nil && header.Size > maxUploadSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("file too large (max %d MB)", maxUploadSize>>20)})
		return
	}

	filename := "upload"
	if header != nil && header.Filename != "" {
		filename = filepath.Base(header.Filename)
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if ext != "" && !allowedUploadExtensions[ext] {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unsupported file type %q; allowed: .txt, .md, .html, .csv, .json, .xml", ext)})
		return
	}

	tenantID, ok := resolveTenantID(c, c.PostForm("tenant_id"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id is required"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(file, maxUploadSize+1))
	if err != nil {
		a.logger.WithError(err).Warn("Failed to read upload")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read upload"})
		return
	}
	if int64(len(body)) > maxUploadSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("file too large (max %d MB)", maxUploadSize>>20)})
		return
	}
	content := strings.TrimSpace(string(body))
	if content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file content is empty"})
		return
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
	sourceURL := strings.TrimSpace(c.Query("url"))
	if sourceURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url query parameter is required"})
		return
	}

	if err := a.store.DeleteBySource(c.Request.Context(), tenantID, sourceURL); err != nil {
		a.logger.WithError(err).Warn("Failed to delete knowledge source")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete source"})
		return
	}
	if a.pageCache != nil {
		if err := a.pageCache.DeleteBySource(c.Request.Context(), tenantID, sourceURL); err != nil {
			a.logger.WithError(err).Warn("Failed to delete page cache for source")
		}
	}

	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func (a *AdminAPI) runCrawl(jobID, tenantID, sitemapURL string, render bool) {
	// Store cancel func before blocking on semaphore so the job can be
	// cancelled while queued.
	ctx, cancel := context.WithTimeout(context.Background(), maxCrawlDuration)
	defer cancel()
	activeCrawls.Store(jobID, cancel)
	defer activeCrawls.Delete(jobID)

	crawlSem <- struct{}{}
	defer func() { <-crawlSem }()

	// If cancelled while waiting for the semaphore, bail out early and
	// mark the job so it doesn't stay stuck as "running".
	if ctx.Err() != nil {
		if dbErr := a.queries.FinishRunningCrawlJob(context.Background(), skipperdb.FinishRunningCrawlJobParams{
			Status: "cancelled", Error: sql.NullString{String: "context expired while queued", Valid: true},
			FinishedAt: sql.NullTime{Time: a.now().UTC(), Valid: true}, ID: jobID,
		}); dbErr != nil {
			a.logger.WithError(dbErr).Warn("Failed to update timed-out crawl job")
		}
		return
	}

	_, err := a.crawler.CrawlAndEmbed(ctx, tenantID, sitemapURL, render)
	finished := a.now().UTC()
	status := "completed"
	var errMsg *string
	if err != nil {
		status = "failed"
		s := err.Error()
		errMsg = &s
		a.logger.WithError(err).Warn("Admin crawl failed")
	}
	// Only update if still 'running' — avoids overwriting 'cancelled' set by handleCancelCrawl.
	var errorValue sql.NullString
	if errMsg != nil {
		errorValue = sql.NullString{String: *errMsg, Valid: true}
	}
	if dbErr := a.queries.FinishRunningCrawlJob(context.Background(), skipperdb.FinishRunningCrawlJobParams{
		Status: status, Error: errorValue, FinishedAt: sql.NullTime{Time: finished, Valid: true}, ID: jobID,
	}); dbErr != nil {
		a.logger.WithError(dbErr).WithField("job_id", jobID).Warn("Failed to update crawl job status")
	}
}

func (a *AdminAPI) handleCrawlStatus(c *gin.Context) {
	jobID := c.Param("id")
	if strings.TrimSpace(jobID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "job id is required"})
		return
	}
	tenantID, ok := resolveTenantID(c, "")
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id is required"})
		return
	}
	row, err := a.queries.GetCrawlJob(c.Request.Context(), skipperdb.GetCrawlJobParams{ID: jobID, TenantID: tenantID})
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	if err != nil {
		a.logger.WithError(err).Warn("Failed to fetch crawl job")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch job"})
		return
	}
	job := crawlJobFromRow(row)
	c.JSON(http.StatusOK, job)
}

func (a *AdminAPI) handleListCrawlJobs(c *gin.Context) {
	tenantID, ok := resolveTenantID(c, c.Query("tenant_id"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id is required"})
		return
	}

	rows, err := a.queries.ListCrawlJobs(c.Request.Context(), tenantID)
	if err != nil {
		a.logger.WithError(err).Warn("Failed to list crawl jobs")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list crawl jobs"})
		return
	}
	var jobs []CrawlJob
	for _, row := range rows {
		jobs = append(jobs, crawlJobFromRow(row))
	}

	if jobs == nil {
		jobs = []CrawlJob{}
	}
	c.JSON(http.StatusOK, jobs)
}

func (a *AdminAPI) handleCancelCrawl(c *gin.Context) {
	jobID := c.Param("id")
	if strings.TrimSpace(jobID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "job id is required"})
		return
	}
	tenantID, ok := resolveTenantID(c, "")
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenant_id is required"})
		return
	}

	// Verify the job belongs to this tenant
	status, err := a.queries.GetCrawlJobStatus(c.Request.Context(), skipperdb.GetCrawlJobStatusParams{ID: jobID, TenantID: tenantID})
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	if err != nil {
		a.logger.WithError(err).Warn("Failed to fetch crawl job for cancellation")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch job"})
		return
	}
	if status != "running" {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("job is %s, not running", status)})
		return
	}

	if cancelFn, loaded := activeCrawls.LoadAndDelete(jobID); loaded {
		if cancel, ok := cancelFn.(context.CancelFunc); ok {
			cancel()
		}
	}

	now := a.now().UTC()
	if dbErr := a.queries.CancelRunningCrawlJob(c.Request.Context(), skipperdb.CancelRunningCrawlJobParams{
		FinishedAt: sql.NullTime{Time: now, Valid: true}, ID: jobID,
	}); dbErr != nil {
		a.logger.WithError(dbErr).Warn("Failed to update cancelled crawl job")
	}

	c.JSON(http.StatusOK, gin.H{"status": "cancelled"})
}

func crawlJobFromRow(row skipperdb.SkipperSkipperCrawlJob) CrawlJob {
	job := CrawlJob{
		ID: row.ID, SitemapURL: row.SitemapUrl, TenantID: row.TenantID,
		Status: row.Status, Error: row.Error.String, StartedAt: row.StartedAt,
	}
	if row.FinishedAt.Valid {
		job.FinishedAt = &row.FinishedAt.Time
	}
	return job
}

func operatorOnlyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		role := skipper.GetRole(c.Request.Context())
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
	ctxTenant := strings.TrimSpace(skipper.GetTenantID(c.Request.Context()))
	role := strings.ToLower(skipper.GetRole(c.Request.Context()))

	// Only the internal "service" role may target a different tenant.
	if role == "service" {
		if t := strings.TrimSpace(explicit); t != "" {
			return t, true
		}
		if t := strings.TrimSpace(c.GetHeader("X-Tenant-Id")); t != "" {
			return t, true
		}
	}

	if ctxTenant != "" {
		return ctxTenant, true
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

func (a *AdminAPI) handleHealth(c *gin.Context) {
	if a.health == nil {
		c.JSON(http.StatusOK, gin.H{"sources": []any{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sources": a.health.Snapshot()})
}
