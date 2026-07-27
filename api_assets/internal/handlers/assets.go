package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"

	"frameworks/api_assets/internal/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

const (
	defaultAssetCacheMaxAge = 30 * time.Second
	liveSpriteCacheMaxAge   = 30 * time.Second
	// thumbnailVersionTTL bounds how long a resolved active-version is trusted before a re-pull confirms it, so
	// a missed push self-heals. Publishes push a fresh version and reset this; live streams (frequent publishes)
	// stay warm and never re-pull.
	thumbnailVersionTTL = 60 * time.Second
	// defaultFoghornResolveURL is the in-cell Foghorn address (Privateer mesh DNS, cluster-scoped) Chandler uses
	// to resolve asset_key -> active version on a cold miss. Overridable via FOGHORN_INTERNAL_URL.
	defaultFoghornResolveURL = "http://foghorn.internal:18008"
)

// versionEntry caches a resolved active version. version=="" means "resolved: no version" → legacy fallback.
type versionEntry struct {
	version    string
	resolvedAt time.Time
}

type assetPolicy struct {
	contentType  string
	cacheControl string
	cacheMaxAge  time.Duration
}

var allowedFiles = map[string]assetPolicy{
	"poster.jpg": {
		contentType:  "image/jpeg",
		cacheControl: "public, max-age=30",
		cacheMaxAge:  defaultAssetCacheMaxAge,
	},
	"sprite.jpg": {
		contentType:  "image/jpeg",
		cacheControl: "public, no-cache",
		cacheMaxAge:  liveSpriteCacheMaxAge,
	},
	"sprite.vtt": {
		contentType:  "text/vtt; charset=utf-8",
		cacheControl: "public, no-cache",
		cacheMaxAge:  liveSpriteCacheMaxAge,
	},
}

type S3Config struct {
	Bucket       string
	Prefix       string
	Region       string
	Endpoint     string
	AccessKey    string
	SecretKey    string
	ServiceToken string
}

// S3Getter abstracts the S3 GetObject call for testability.
type S3Getter interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

type AssetHandler struct {
	s3           S3Getter
	bucket       string
	prefix       string
	serviceToken string
	cache        *cache.LRU
	logger       logging.Logger

	cacheHits   prometheus.Counter
	cacheMisses prometheus.Counter
	s3Errors    prometheus.Counter

	// In-cell thumbnail version resolution: asset_key -> active version, updated by the push (invalidate) fast
	// path and confirmed by a cold-miss pull to the cell's Foghorn (foghornResolveURL).
	versionsMu        sync.RWMutex
	activeVersions    map[string]versionEntry
	foghornResolveURL string
	httpClient        *http.Client
	// resolveVersionFn overrides the HTTP cold-miss pull (test seam). Returns (version, resolved, gone); nil in
	// production, where pullActiveVersion performs the real in-cell Foghorn call.
	resolveVersionFn func(ctx context.Context, assetKey string) (string, bool, bool)
}

func NewAssetHandler(cfg S3Config, lru *cache.LRU, logger logging.Logger, cacheHits, cacheMisses, s3Errors prometheus.Counter) (*AssetHandler, error) {
	var opts []func(*awsconfig.LoadOptions) error
	opts = append(opts, awsconfig.WithRegion(cfg.Region))

	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	var s3Opts []func(*s3.Options)
	if cfg.Endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true
		})
	}

	client := s3.NewFromConfig(awsCfg, s3Opts...)

	resolveURL := strings.TrimRight(strings.TrimSpace(os.Getenv("FOGHORN_INTERNAL_URL")), "/")
	if resolveURL == "" {
		resolveURL = defaultFoghornResolveURL
	}
	// The in-cell version resolver is REQUIRED: without a SERVICE_TOKEN, Chandler cannot ask Foghorn which
	// version an asset serves, so every cold read FAILS CLOSED (503) rather than guessing the legacy key. This is
	// Chandler's HALF of a Chandler-before-Foghorn rollout — it makes a misconfigured or old Chandler surface
	// 503s instead of serving stale legacy bytes, but it does NOT stop a newer Foghorn from publishing versioned
	// objects while an old Chandler is still deployed; that ordering is an OPERATOR obligation (see the operator
	// upgrade guide's Chandler/Foghorn ordering). Warn loudly so the misconfiguration is visible at boot.
	if strings.TrimSpace(cfg.ServiceToken) == "" {
		logger.Warn("Chandler started WITHOUT SERVICE_TOKEN: the thumbnail version resolver is unusable, so versioned thumbnail reads will fail closed (503). Set SERVICE_TOKEN and FOGHORN_INTERNAL_URL for a version-capable deployment.")
	}

	return &AssetHandler{
		s3:                client,
		bucket:            cfg.Bucket,
		prefix:            cfg.Prefix,
		serviceToken:      cfg.ServiceToken,
		cache:             lru,
		logger:            logger,
		cacheHits:         cacheHits,
		cacheMisses:       cacheMisses,
		s3Errors:          s3Errors,
		activeVersions:    map[string]versionEntry{},
		foghornResolveURL: resolveURL,
		httpClient:        &http.Client{Timeout: 2 * time.Second},
	}, nil
}

// resolveActiveVersion returns (version, resolved). resolved=false means the active version is UNKNOWN right now
// (the in-cell Foghorn is unreachable AND nothing is cached) — the caller must FAIL CLOSED rather than guess the
// legacy un-versioned key, which could serve stale/wrong bytes for an asset that actually has a published
// version. When resolved=true, an empty version means the asset genuinely has no published version yet (serve
// the legacy object; migration/never-published fallback). A stale cached mapping is still "resolved" (a
// last-known version beats a guess).
func (h *AssetHandler) resolveActiveVersion(ctx context.Context, assetKey string) (version string, resolved bool, gone bool) {
	h.versionsMu.RLock()
	entry, ok := h.activeVersions[assetKey]
	h.versionsMu.RUnlock()
	if ok && time.Since(entry.resolvedAt) < thumbnailVersionTTL {
		return entry.version, true, false
	}

	pull := h.pullActiveVersion
	if h.resolveVersionFn != nil {
		pull = h.resolveVersionFn
	}
	pulled, ok2, isGone := pull(ctx, assetKey)
	if isGone {
		// The parent artifact is terminal — the asset is GONE. EVICT any cached mapping (so a since-published
		// version isn't served from the map after deletion) and tell the caller to 404 rather than serve legacy.
		h.versionsMu.Lock()
		delete(h.activeVersions, assetKey)
		h.versionsMu.Unlock()
		return "", true, true
	}
	if !ok2 {
		// Prefer the last-known (even stale) mapping over a guess.
		if ok {
			return entry.version, true, false
		}
		// The version is UNKNOWN — the resolver is unusable (unconfigured: missing SERVICE_TOKEN / URL) or
		// unreachable. FAIL CLOSED: a missing resolver is NOT positive proof that the asset has no version, and
		// serving the legacy key here would hand back a stale object (or 404) for an asset Foghorn published a
		// versioned object for. Only an AUTHORITATIVE resolver response (below) may select the legacy key. The
		// resolver is required by contract; Chandler warns loudly at startup if it isn't configured.
		return "", false, false
	}
	h.versionsMu.Lock()
	h.activeVersions[assetKey] = versionEntry{version: pulled, resolvedAt: time.Now()}
	h.versionsMu.Unlock()
	return pulled, true, false
}

// pullActiveVersion asks the in-cell Foghorn (foghorn.internal) for the asset's serving decision. resolved=false
// on any transport/auth failure so the caller keeps its last-known mapping instead of caching a bad answer.
// gone=true means the parent artifact is terminal (the asset is GONE) — the caller must serve nothing.
func (h *AssetHandler) pullActiveVersion(ctx context.Context, assetKey string) (version string, resolved bool, gone bool) {
	if h.foghornResolveURL == "" || h.serviceToken == "" {
		return "", false, false
	}
	reqURL := h.foghornResolveURL + "/internal/thumbnails/active-version?assetKey=" + url.QueryEscape(assetKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", false, false
	}
	req.Header.Set("Authorization", "Bearer "+h.serviceToken)
	resp, err := h.httpClient.Do(req)
	if err != nil {
		h.logger.WithError(err).WithField("asset_key", assetKey).Debug("Cold-miss thumbnail version pull failed; keeping last-known")
		return "", false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false, false
	}
	var body struct {
		State         string `json:"state"`
		ActiveVersion string `json:"activeVersion"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false, false
	}
	if body.State == "gone" {
		return "", true, true
	}
	return body.ActiveVersion, true, false
}

func (h *AssetHandler) RegisterRoutes(router *gin.Engine) {
	router.OPTIONS("/assets/:assetKey/:file", h.handleAssetOptions)
	router.GET("/assets/:assetKey/:file", h.handleGetAsset)
	router.HEAD("/assets/:assetKey/:file", h.handleGetAsset)
	if h.serviceToken != "" {
		router.POST("/internal/assets/cache/invalidate", auth.ServiceAuthMiddleware(h.serviceToken), h.handleInvalidateCache)
	}
}

func (h *AssetHandler) handleAssetOptions(c *gin.Context) {
	setAssetCORSHeaders(c)
	c.Status(http.StatusNoContent)
}

type invalidateCacheRequest struct {
	AssetKey string   `json:"assetKey"`
	Files    []string `json:"files"`
	// ActiveVersion, when present, is the newly-published version this asset now serves. Chandler updates its
	// in-memory map from it (the push fast path) so subsequent GETs serve the new object without a cold-miss pull.
	ActiveVersion string `json:"activeVersion,omitempty"`
}

func (h *AssetHandler) handleGetAsset(c *gin.Context) {
	setAssetCORSHeaders(c)
	assetKey := c.Param("assetKey")
	file := c.Param("file")

	policy, ok := allowedFiles[file]
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}

	if h.bucket == "" {
		c.Status(http.StatusServiceUnavailable)
		return
	}

	// Reject path traversal
	if strings.Contains(assetKey, "/") || strings.Contains(assetKey, "..") {
		c.Status(http.StatusBadRequest)
		return
	}

	// Resolve the active version and serve the immutable versioned object; fall back to the legacy un-versioned
	// key ONLY when the asset genuinely has no published version (migration). The public URL is unchanged either
	// way. If the version is UNRESOLVABLE (in-cell Foghorn unreachable and nothing cached), fail closed rather
	// than serve a possibly-stale legacy object for what might be a versioned asset.
	version, resolved, gone := h.resolveActiveVersion(c.Request.Context(), assetKey)
	if gone {
		// Parent artifact is terminal — the asset is GONE. Do NOT serve a surviving legacy object; 404.
		c.Status(http.StatusNotFound)
		return
	}
	if !resolved {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	var s3Key string
	if version != "" && !strings.Contains(version, "/") && !strings.Contains(version, "..") {
		s3Key = h.fullKey(path.Join("thumbnails", assetKey, "v", version, file))
	} else {
		s3Key = h.fullKey(path.Join("thumbnails", assetKey, file))
	}

	// Check cache
	if data, ct, hit := h.cache.GetFresh(s3Key, policy.cacheMaxAge); hit {
		h.cacheHits.Inc()
		c.Header("Cache-Control", policy.cacheControl)
		c.Data(http.StatusOK, ct, data)
		return
	}
	h.cacheMisses.Inc()

	// Fetch from S3
	out, err := h.s3.GetObject(c.Request.Context(), &s3.GetObjectInput{
		Bucket: aws.String(h.bucket),
		Key:    aws.String(s3Key),
	})
	if err != nil {
		h.s3Errors.Inc()
		h.logger.WithError(err).WithField("key", s3Key).Debug("S3 GetObject failed")
		c.Status(http.StatusNotFound)
		return
	}
	defer out.Body.Close()

	data, err := io.ReadAll(out.Body)
	if err != nil {
		h.s3Errors.Inc()
		h.logger.WithError(err).WithField("key", s3Key).Warn("Failed to read S3 object body")
		c.Status(http.StatusInternalServerError)
		return
	}

	h.cache.Put(s3Key, data, policy.contentType)

	c.Header("Cache-Control", policy.cacheControl)
	c.Data(http.StatusOK, policy.contentType, data)
}

func setAssetCORSHeaders(c *gin.Context) {
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "DNT,User-Agent,X-Requested-With,If-Modified-Since,Cache-Control,Content-Type,Range")
	c.Header("Access-Control-Expose-Headers", "Content-Length,Content-Range,Accept-Ranges")
}

func (h *AssetHandler) handleInvalidateCache(c *gin.Context) {
	var req invalidateCacheRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	assetKey := strings.TrimSpace(req.AssetKey)
	if assetKey == "" || strings.Contains(assetKey, "/") || strings.Contains(assetKey, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid assetKey"})
		return
	}

	files := req.Files
	if len(files) == 0 {
		files = make([]string, 0, len(allowedFiles))
		for file := range allowedFiles {
			files = append(files, file)
		}
	}

	// Push fast path: record the newly-published active version so subsequent GETs serve the new object without
	// a cold-miss pull. Capture the prior version so its now-superseded cache entries can be freed.
	newVersion := strings.TrimSpace(req.ActiveVersion)
	var oldVersion string
	if newVersion != "" && !strings.Contains(newVersion, "/") && !strings.Contains(newVersion, "..") {
		h.versionsMu.Lock()
		oldVersion = h.activeVersions[assetKey].version
		h.activeVersions[assetKey] = versionEntry{version: newVersion, resolvedAt: time.Now()}
		h.versionsMu.Unlock()
	}

	invalidated := 0
	for _, file := range files {
		file = strings.TrimSpace(file)
		if _, ok := allowedFiles[file]; !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file"})
			return
		}
		// Evict the legacy un-versioned entry and any superseded prior version. The new version's key is fresh
		// (a natural cache miss), so it doesn't need eviction.
		if h.cache.Delete(h.fullKey(path.Join("thumbnails", assetKey, file))) {
			invalidated++
		}
		if oldVersion != "" {
			if h.cache.Delete(h.fullKey(path.Join("thumbnails", assetKey, "v", oldVersion, file))) {
				invalidated++
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"invalidated": invalidated})
}

func (h *AssetHandler) fullKey(key string) string {
	if h.prefix == "" {
		return key
	}
	return strings.TrimSuffix(h.prefix, "/") + "/" + key
}
