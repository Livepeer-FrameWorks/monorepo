package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"

	"frameworks/api_assets/internal/cache"
	"frameworks/pkg/logging"
)

var allowedFiles = map[string]string{
	"poster.jpg": "image/jpeg",
	"sprite.jpg": "image/jpeg",
	"sprite.vtt": "text/vtt; charset=utf-8",
}

type S3Config struct {
	Bucket    string
	Prefix    string
	Region    string
	Endpoint  string
	AccessKey string
	SecretKey string
}

type AssetHandler struct {
	s3     *s3.Client
	bucket string
	prefix string
	cache  *cache.LRU
	logger logging.Logger

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
		s3:          client,
		bucket:      cfg.Bucket,
		prefix:      cfg.Prefix,
		cache:       lru,
		logger:      logger,
		cacheHits:   cacheHits,
		cacheMisses: cacheMisses,
		s3Errors:    s3Errors,
	}, nil
}

func (h *AssetHandler) RegisterRoutes(router *gin.Engine) {
	router.GET("/assets/:playbackId/:file", h.handleGetAsset)
}

func (h *AssetHandler) handleGetAsset(c *gin.Context) {
	playbackID := c.Param("playbackId")
	file := c.Param("file")

	contentType, ok := allowedFiles[file]
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}

	if h.bucket == "" {
		c.Status(http.StatusServiceUnavailable)
		return
	}

	// Reject path traversal
	if strings.Contains(playbackID, "/") || strings.Contains(playbackID, "..") {
		c.Status(http.StatusBadRequest)
		return
	}

	s3Key := h.fullKey(path.Join("thumbnails", playbackID, file))

	// Check cache
	if data, ct, hit := h.cache.Get(s3Key); hit {
		h.cacheHits.Inc()
		c.Header("Cache-Control", "public, max-age=30")
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

	h.cache.Put(s3Key, data, contentType)

	c.Header("Cache-Control", "public, max-age=30")
	c.Data(http.StatusOK, contentType, data)
}

func (h *AssetHandler) fullKey(key string) string {
	if h.prefix == "" {
		return key
	}
	return strings.TrimSuffix(h.prefix, "/") + "/" + key
}
