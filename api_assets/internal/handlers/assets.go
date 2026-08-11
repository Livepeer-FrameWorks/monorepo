package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"

	"frameworks/api_assets/internal/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mediakeys"
)

const (
	defaultAssetCacheMaxAge = 30 * time.Second
	liveSpriteCacheMaxAge   = 30 * time.Second
)

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

// AssetHandler is a dumb static-object server over ONE immutable S3 backend: a request path maps DETERMINISTICALLY to
// an object key, which it streams and caches. It performs no version resolution, no Foghorn call, and knows nothing
// about tenants, catalogs, or publication — see docs/architecture/thumbnails.md.
type AssetHandler struct {
	s3           S3Getter
	bucket       string
	prefix       string
	serviceToken string // gates the internal cache-invalidation endpoint only
	cache        *cache.LRU
	logger       logging.Logger

	cacheHits   prometheus.Counter
	cacheMisses prometheus.Counter
	s3Errors    prometheus.Counter

	// storeReachableFn overrides the /ready store probe (test seam); nil in production, where probeStoreReachable
	// performs the real bounded GetObject against the immutable backend.
	storeReachableFn func(ctx context.Context) bool
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

	// SERVICE_TOKEN gates only the internal cache-invalidation endpoint; without it that route is unregistered and
	// Chandler self-heals published/deleted objects via the object cache TTL. Serving does not need the token.
	if strings.TrimSpace(cfg.ServiceToken) == "" {
		logger.Warn("Chandler started WITHOUT SERVICE_TOKEN: the internal cache-invalidation endpoint is disabled; published/deleted objects self-heal via the cache TTL instead of an immediate push.")
	}

	return &AssetHandler{
		s3:           client,
		bucket:       cfg.Bucket,
		prefix:       cfg.Prefix,
		serviceToken: cfg.ServiceToken,
		cache:        lru,
		logger:       logger,
		cacheHits:    cacheHits,
		cacheMisses:  cacheMisses,
		s3Errors:     s3Errors,
	}, nil
}

func (h *AssetHandler) RegisterRoutes(router *gin.Engine) {
	// /assets/{assetKey}/{file} is Chandler's SOLE public contract: it maps DETERMINISTICALLY to the object key
	// thumbnails/{assetKey}/{file} and serves it, with no version resolution and no Foghorn call. No other route
	// (e.g. a namespaced /static/{ns}/…) is registered — Chandler serves only /assets. See
	// docs/architecture/thumbnails.md.
	router.OPTIONS("/assets/:assetKey/:file", h.handleAssetOptions)
	router.GET("/assets/:assetKey/:file", h.handleGetAsset)
	router.HEAD("/assets/:assetKey/:file", h.handleGetAsset)
	// Readiness: proves ONLY that this instance can read its immutable backend (store probe). No resolver, no Foghorn.
	router.GET("/ready", h.handleReady)
	if h.serviceToken != "" {
		router.POST("/internal/assets/cache/invalidate", auth.ServiceAuthMiddleware(h.serviceToken), h.handleInvalidateCache)
	}
}

// handleReady reports whether this Chandler can read its immutable backend — the only thing a dumb static-object
// server needs to prove. A reachable store is 200; anything else is 503. No resolver, no Foghorn, no publication
// coupling (docs/architecture/thumbnails.md).
func (h *AssetHandler) handleReady(c *gin.Context) {
	if h.probeStoreReachable(c.Request.Context()) {
		c.JSON(http.StatusOK, gin.H{"ready": true, "store": true})
		return
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{"ready": false, "store": false})
}

// probeStoreReachable does a bounded GetObject of the readiness sentinel (mediakeys.ReadinessSentinelKey, written by
// Foghorn) and FULLY READS its body: readiness requires the whole tiny object to arrive, so a mid-body transport
// failure — not just a successful response header — is caught. Any failure (sentinel absent, AccessDenied, wrong
// bucket/endpoint, bad credentials, a truncated/failed body read, empty body) is NOT ready. The test seam
// storeReachableFn overrides it.
func (h *AssetHandler) probeStoreReachable(ctx context.Context) bool {
	if h.storeReachableFn != nil {
		return h.storeReachableFn(ctx)
	}
	if h.s3 == nil {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := h.s3.GetObject(probeCtx, &s3.GetObjectInput{
		Bucket: aws.String(h.bucket),
		Key:    aws.String(h.fullKey(mediakeys.ReadinessSentinelKey)),
	})
	if err != nil {
		return false
	}
	defer out.Body.Close() //nolint:errcheck
	// Read the whole (tiny) object, bounded, so a stream that fails mid-body cannot report ready. A non-empty read is
	// the proof; the exact content is not asserted (Foghorn owns it).
	data, rErr := io.ReadAll(io.LimitReader(out.Body, 256))
	return rErr == nil && len(data) > 0
}

// isNotFoundS3Error reports whether err is the object-store's AUTHORITATIVE "no such object" (typed NoSuchKey/NotFound,
// or an APIError whose code says so). Everything else — a timeout, credential failure, throttle, wrong endpoint, or
// outage — is NOT proof of absence and must be treated as a backend failure, never a 404.
func isNotFoundS3Error(err error) bool {
	var noKey *s3types.NoSuchKey
	var notFound *s3types.NotFound
	if errors.As(err, &noKey) || errors.As(err, &notFound) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}

func (h *AssetHandler) handleAssetOptions(c *gin.Context) {
	setAssetCORSHeaders(c)
	c.Status(http.StatusNoContent)
}

type invalidateCacheRequest struct {
	AssetKey string   `json:"assetKey"`
	Files    []string `json:"files"`
}

// handleGetAsset serves /assets/{assetKey}/{file} — Chandler's public contract. It maps the path DETERMINISTICALLY to
// the thumbnails key `thumbnails/{assetKey}/{file}` and serves it, with NO version resolution and NO Foghorn call.
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

	h.serveObjectKey(c, h.fullKey(path.Join("thumbnails", assetKey, file)), policy)
}

// serveObjectKey serves one exact S3 object (cache-or-fetch) under the given policy. It performs NO version
// resolution and NO Foghorn call — the caller supplies the resolved key. It is key-generic, not thumbnails-specific.
func (h *AssetHandler) serveObjectKey(c *gin.Context, s3Key string, policy assetPolicy) {
	if data, ct, hit := h.cache.GetFresh(s3Key, policy.cacheMaxAge); hit {
		h.cacheHits.Inc()
		c.Header("Cache-Control", policy.cacheControl)
		c.Data(http.StatusOK, ct, data)
		return
	}
	h.cacheMisses.Inc()

	out, err := h.s3.GetObject(c.Request.Context(), &s3.GetObjectInput{
		Bucket: aws.String(h.bucket),
		Key:    aws.String(s3Key),
	})
	if err != nil {
		h.s3Errors.Inc()
		// Distinguish "object absent" (404) from "backend cannot serve" (503). A typed NoSuchKey/NotFound is the
		// object genuinely not existing; a timeout, credential failure, throttle, or outage is NOT proof of absence
		// and must not be cached-as-missing or reported as 404 (which a CDN would cache).
		if isNotFoundS3Error(err) {
			h.logger.WithError(err).WithField("key", s3Key).Debug("S3 object not found")
			c.Status(http.StatusNotFound)
		} else {
			h.logger.WithError(err).WithField("key", s3Key).Warn("S3 backend failure serving object")
			c.Status(http.StatusServiceUnavailable)
		}
		return
	}
	defer out.Body.Close()

	data, err := io.ReadAll(out.Body)
	if err != nil {
		h.s3Errors.Inc()
		// A mid-stream body read failure is a backend/transport fault (the object exists — GetObject already
		// succeeded), not a bug in this handler: report 503 like the GetObject failure above, never 500.
		h.logger.WithError(err).WithField("key", s3Key).Warn("Failed to read S3 object body")
		c.Status(http.StatusServiceUnavailable)
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

	invalidated := 0
	for _, file := range files {
		file = strings.TrimSpace(file)
		if _, ok := allowedFiles[file]; !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file"})
			return
		}
		// Evict the deterministic key so the next request re-pulls the freshly-published (or deleted) object from S3.
		if h.cache.Delete(h.fullKey(path.Join("thumbnails", assetKey, file))) {
			invalidated++
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
