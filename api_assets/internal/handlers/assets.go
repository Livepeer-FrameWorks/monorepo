package handlers

import (
	"context"
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
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"

	"frameworks/api_assets/internal/cache"
	"frameworks/pkg/auth"
	"frameworks/pkg/logging"
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
	router.GET("/assets/:assetKey/:file", h.handleGetAsset)
	if h.serviceToken != "" {
		router.POST("/internal/assets/cache/invalidate", auth.ServiceAuthMiddleware(h.serviceToken), h.handleInvalidateCache)
	}
}

type invalidateCacheRequest struct {
	AssetKey string   `json:"assetKey"`
	Files    []string `json:"files"`
}

func (h *AssetHandler) handleGetAsset(c *gin.Context) {
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

	s3Key := h.fullKey(path.Join("thumbnails", assetKey, file))

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
		s3Key := h.fullKey(path.Join("thumbnails", assetKey, file))
		if h.cache.Delete(s3Key) {
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
