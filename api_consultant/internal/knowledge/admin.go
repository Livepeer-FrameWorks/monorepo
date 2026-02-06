package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"frameworks/pkg/auth"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const maxUploadSize int64 = 10 << 20 // 10 MB

var allowedUploadExtensions = map[string]bool{
	".txt":  true,
	".md":   true,
	".html": true,
	".csv":  true,
	".json": true,
	".xml":  true,
}

type AdminAPI struct {
	db       *sql.DB
	store    *Store
	embedder *Embedder
	crawler  *Crawler
	logger   logging.Logger
	now      func() time.Time
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

func NewAdminAPI(db *sql.DB, store *Store, embedder *Embedder, crawler *Crawler, logger logging.Logger) (*AdminAPI, error) {
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
		db:       db,
		store:    store,
		embedder: embedder,
		crawler:  crawler,
		logger:   logger,
		now:      time.Now,
	}, nil
}

func (a *AdminAPI) RegisterRoutes(router *gin.Engine, jwtSecret []byte) {
	group := router.Group("/api/skipper/admin")
	group.Use(auth.JWTAuthMiddleware(jwtSecret))
	group.Use(operatorOnlyMiddleware())

	group.POST("/crawl", a.handleCrawl)
	group.GET("/crawl/:id", a.handleCrawlStatus)
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
	startedAt := a.now().UTC()
	_, err := a.db.ExecContext(c.Request.Context(),
		`INSERT INTO skipper.skipper_crawl_jobs (id, tenant_id, sitemap_url, status, started_at) VALUES ($1, $2, $3, 'running', $4)`,
		jobID, tenantID, req.SitemapURL, startedAt)
	if err != nil {
		a.logger.WithError(err).Warn("Failed to create crawl job")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create crawl job"})
		return
	}

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
	finished := a.now().UTC()
	status := "completed"
	var errMsg *string
	if err != nil {
		status = "failed"
		s := err.Error()
		errMsg = &s
		a.logger.WithError(err).Warn("Admin crawl failed")
	}
	if _, dbErr := a.db.ExecContext(context.Background(),
		`UPDATE skipper.skipper_crawl_jobs SET status = $1, error = $2, finished_at = $3 WHERE id = $4`,
		status, errMsg, finished, jobID); dbErr != nil {
		a.logger.WithError(dbErr).WithField("job_id", jobID).Warn("Failed to update crawl job status")
	}
}

func (a *AdminAPI) handleCrawlStatus(c *gin.Context) {
	jobID := c.Param("id")
	if strings.TrimSpace(jobID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "job id is required"})
		return
	}
	var job CrawlJob
	var errMsg sql.NullString
	var finishedAt sql.NullTime
	err := a.db.QueryRowContext(c.Request.Context(),
		`SELECT id, tenant_id, sitemap_url, status, error, started_at, finished_at FROM skipper.skipper_crawl_jobs WHERE id = $1`,
		jobID).Scan(&job.ID, &job.TenantID, &job.SitemapURL, &job.Status, &errMsg, &job.StartedAt, &finishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	if err != nil {
		a.logger.WithError(err).Warn("Failed to fetch crawl job")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch job"})
		return
	}
	if errMsg.Valid {
		job.Error = errMsg.String
	}
	if finishedAt.Valid {
		job.FinishedAt = &finishedAt.Time
	}
	c.JSON(http.StatusOK, job)
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
