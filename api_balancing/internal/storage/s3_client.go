package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"frameworks/pkg/logging"
)

// S3Config holds configuration for the S3 client
type S3Config struct {
	Bucket    string // S3 bucket name
	Prefix    string // Key prefix for all operations
	Region    string // AWS region (default: us-east-1)
	Endpoint  string // Custom endpoint for S3-compatible storage (MinIO, etc.)
	AccessKey string // AWS access key (optional, uses IAM roles if empty)
	SecretKey string // AWS secret key (optional, uses IAM roles if empty)
}

// S3Client provides S3 operations for Foghorn cold storage management
// This client holds credentials and should NEVER be deployed to edge nodes.
// Edge nodes receive presigned URLs instead.
type S3Client struct {
	client        *s3.Client
	presignClient *s3.PresignClient
	config        S3Config
	logger        logging.Logger
}

// NewS3Client creates a new S3 client with the given configuration.
// This should only be called from Foghorn (trusted infrastructure).
func NewS3Client(cfg S3Config, logger logging.Logger) (*S3Client, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("S3 bucket is required")
	}

	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}

	// Build AWS config options
	var opts []func(*config.LoadOptions) error
	opts = append(opts, config.WithRegion(cfg.Region))

	// Use explicit credentials if provided, otherwise use default credential chain (IAM roles)
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}

	awsCfg, err := config.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Create S3 client with optional custom endpoint
	var s3Opts []func(*s3.Options)
	if cfg.Endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true // Required for MinIO and most S3-compatible storage
		})
	}

	client := s3.NewFromConfig(awsCfg, s3Opts...)
	presignClient := s3.NewPresignClient(client)

	logger.WithFields(logging.Fields{
		"bucket":   cfg.Bucket,
		"prefix":   cfg.Prefix,
		"region":   cfg.Region,
		"endpoint": cfg.Endpoint,
	}).Info("S3 client initialized (Foghorn - credentials secured)")

	return &S3Client{
		client:        client,
		presignClient: presignClient,
		config:        cfg,
		logger:        logger,
	}, nil
}

// fullKey returns the full S3 key including prefix
func (c *S3Client) fullKey(key string) string {
	if c.config.Prefix == "" {
		return key
	}
	return strings.TrimSuffix(c.config.Prefix, "/") + "/" + strings.TrimPrefix(key, "/")
}

// GeneratePresignedPUT generates a presigned URL for uploading an object.
// The URL is time-limited and scoped to this specific object.
// Send this URL to Helmsman for secure uploads without exposing credentials.
func (c *S3Client) GeneratePresignedPUT(key string, expiry time.Duration) (string, error) {
	if expiry == 0 {
		expiry = 15 * time.Minute // Default 15 minutes
	}

	fullKey := c.fullKey(key)

	req, err := c.presignClient.PresignPutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(c.config.Bucket),
		Key:    aws.String(fullKey),
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned PUT URL: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"bucket": c.config.Bucket,
		"key":    fullKey,
		"expiry": expiry,
	}).Info("Generated presigned PUT URL")

	return req.URL, nil
}

// GeneratePresignedGET generates a presigned URL for downloading an object.
// The URL is time-limited and scoped to this specific object.
// Send this URL to Helmsman for secure downloads without exposing credentials.
func (c *S3Client) GeneratePresignedGET(key string, expiry time.Duration) (string, error) {
	if expiry == 0 {
		expiry = 15 * time.Minute // Default 15 minutes
	}

	fullKey := c.fullKey(key)

	req, err := c.presignClient.PresignGetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(c.config.Bucket),
		Key:    aws.String(fullKey),
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned GET URL: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"bucket": c.config.Bucket,
		"key":    fullKey,
		"expiry": expiry,
	}).Info("Generated presigned GET URL")

	return req.URL, nil
}

// GeneratePresignedURLsForDVR generates presigned URLs for all segments of a DVR recording.
// Returns a map of segment key -> presigned URL for efficient batch operations.
func (c *S3Client) GeneratePresignedURLsForDVR(dvrPrefix string, isUpload bool, expiry time.Duration) (map[string]string, error) {
	if expiry == 0 {
		expiry = 30 * time.Minute // Longer expiry for DVR operations
	}

	// List all objects under the DVR prefix
	keys, err := c.ListPrefix(context.Background(), dvrPrefix)
	if err != nil {
		return nil, err
	}

	urls := make(map[string]string)
	for _, key := range keys {
		var url string
		var err error
		if isUpload {
			url, err = c.GeneratePresignedPUT(key, expiry)
		} else {
			url, err = c.GeneratePresignedGET(key, expiry)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to generate presigned URL for %s: %w", key, err)
		}
		urls[key] = url
	}

	return urls, nil
}

// Delete removes an object from S3 (called from Foghorn, not edge nodes)
func (c *S3Client) Delete(ctx context.Context, key string) error {
	fullKey := c.fullKey(key)

	_, err := c.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.config.Bucket),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		return fmt.Errorf("failed to delete from S3: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"bucket": c.config.Bucket,
		"key":    fullKey,
	}).Info("Deleted file from S3")

	return nil
}

// DeletePrefix removes all objects with the given prefix (for DVR directories)
func (c *S3Client) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	fullPrefix := c.fullKey(prefix)
	deleted := 0

	// List and delete in batches
	paginator := s3.NewListObjectsV2Paginator(c.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.config.Bucket),
		Prefix: aws.String(fullPrefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return deleted, fmt.Errorf("failed to list objects: %w", err)
		}

		if len(page.Contents) == 0 {
			continue
		}

		// Build delete request
		objects := make([]types.ObjectIdentifier, len(page.Contents))
		for i, obj := range page.Contents {
			objects[i] = types.ObjectIdentifier{Key: obj.Key}
		}

		_, err = c.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(c.config.Bucket),
			Delete: &types.Delete{Objects: objects},
		})
		if err != nil {
			return deleted, fmt.Errorf("failed to delete objects: %w", err)
		}

		deleted += len(objects)
	}

	c.logger.WithFields(logging.Fields{
		"bucket":  c.config.Bucket,
		"prefix":  fullPrefix,
		"deleted": deleted,
	}).Info("Deleted objects from S3")

	return deleted, nil
}

// Exists checks if an object exists in S3
func (c *S3Client) Exists(ctx context.Context, key string) (bool, error) {
	fullKey := c.fullKey(key)

	_, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.config.Bucket),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		// Check if it's a "not found" error
		if isNotFoundError(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check object existence: %w", err)
	}

	return true, nil
}

// isNotFoundError checks if the error is a "not found" type error
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "NotFound") ||
		strings.Contains(errStr, "NoSuchKey") ||
		strings.Contains(errStr, "404")
}

// ListPrefix returns all object keys with the given prefix
func (c *S3Client) ListPrefix(ctx context.Context, prefix string) ([]string, error) {
	fullPrefix := c.fullKey(prefix)
	var keys []string

	paginator := s3.NewListObjectsV2Paginator(c.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.config.Bucket),
		Prefix: aws.String(fullPrefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", err)
		}

		for _, obj := range page.Contents {
			// Strip prefix to return relative keys
			relKey := strings.TrimPrefix(*obj.Key, c.config.Prefix)
			relKey = strings.TrimPrefix(relKey, "/")
			keys = append(keys, relKey)
		}
	}

	return keys, nil
}

// GetObjectSize returns the size of an object in bytes
func (c *S3Client) GetObjectSize(ctx context.Context, key string) (int64, error) {
	fullKey := c.fullKey(key)

	resp, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.config.Bucket),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		return 0, fmt.Errorf("failed to get object metadata: %w", err)
	}

	if resp.ContentLength == nil {
		return 0, nil
	}
	return *resp.ContentLength, nil
}

// BuildS3URL returns a full S3 URL for an object (for storage in database)
func (c *S3Client) BuildS3URL(key string) string {
	fullKey := c.fullKey(key)
	return fmt.Sprintf("s3://%s/%s", c.config.Bucket, fullKey)
}

// BuildS3Key builds the S3 key for a clip
func (c *S3Client) BuildClipS3Key(tenantID, streamName, clipHash, format string) string {
	return fmt.Sprintf("clips/%s/%s/%s.%s", tenantID, streamName, clipHash, format)
}

// BuildDVRS3Key builds the S3 key prefix for a DVR recording
func (c *S3Client) BuildDVRS3Key(tenantID, internalName, dvrHash string) string {
	return fmt.Sprintf("dvr/%s/%s/%s", tenantID, internalName, dvrHash)
}

// Bucket returns the configured bucket name
func (c *S3Client) Bucket() string {
	return c.config.Bucket
}

// Prefix returns the configured key prefix
func (c *S3Client) Prefix() string {
	return c.config.Prefix
}

// ============================================================================
// MULTIPART UPLOAD OPERATIONS (for VOD uploads)
// ============================================================================

const (
	// MinPartSize is the minimum part size for S3 multipart uploads (5MB)
	MinPartSize = 5 * 1024 * 1024
	// DefaultPartSize is the default part size (20MB) - good balance of parallelism and requests
	DefaultPartSize = 20 * 1024 * 1024
	// MaxPartCount is the maximum number of parts S3 allows (10,000)
	MaxPartCount = 10000
)

// CompletedPart represents a completed part for multipart upload
type CompletedPart struct {
	PartNumber int
	ETag       string
}

// UploadPart represents a part with its presigned URL
type UploadPart struct {
	PartNumber   int
	PresignedURL string
}

// CalculatePartSize determines optimal part size and count for a given file size.
// Returns partSize (bytes) and partCount.
func CalculatePartSize(totalSize int64) (partSize int64, partCount int) {
	// Start with default part size
	partSize = DefaultPartSize

	// Calculate number of parts with default size
	partCount = int((totalSize + partSize - 1) / partSize)

	// If too many parts, increase part size
	if partCount > MaxPartCount {
		// Calculate minimum part size to stay under limit
		partSize = (totalSize + MaxPartCount - 1) / MaxPartCount
		// Round up to next MB for cleaner numbers
		partSize = ((partSize + 1024*1024 - 1) / (1024 * 1024)) * 1024 * 1024
		partCount = int((totalSize + partSize - 1) / partSize)
	}

	// Ensure minimum part size (except for last part)
	if partSize < MinPartSize && partCount > 1 {
		partSize = MinPartSize
		partCount = int((totalSize + partSize - 1) / partSize)
	}

	return partSize, partCount
}

// CreateMultipartUpload initiates a multipart upload and returns the upload ID.
// This should be called at the start of a VOD upload process.
func (c *S3Client) CreateMultipartUpload(ctx context.Context, key string, contentType string) (string, error) {
	fullKey := c.fullKey(key)

	input := &s3.CreateMultipartUploadInput{
		Bucket: aws.String(c.config.Bucket),
		Key:    aws.String(fullKey),
	}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}

	resp, err := c.client.CreateMultipartUpload(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to create multipart upload: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"bucket":      c.config.Bucket,
		"key":         fullKey,
		"upload_id":   *resp.UploadId,
		"contentType": contentType,
	}).Info("Created multipart upload")

	return *resp.UploadId, nil
}

// GeneratePresignedUploadPart generates a presigned URL for uploading a single part.
// Part numbers are 1-indexed (S3 requirement).
func (c *S3Client) GeneratePresignedUploadPart(key, uploadID string, partNumber int, expiry time.Duration) (string, error) {
	if expiry == 0 {
		expiry = 2 * time.Hour // Longer expiry for multipart uploads
	}

	fullKey := c.fullKey(key)

	req, err := c.presignClient.PresignUploadPart(context.Background(), &s3.UploadPartInput{
		Bucket:     aws.String(c.config.Bucket),
		Key:        aws.String(fullKey),
		UploadId:   aws.String(uploadID),
		PartNumber: aws.Int32(int32(partNumber)),
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned upload part URL: %w", err)
	}

	return req.URL, nil
}

// GeneratePresignedUploadParts generates presigned URLs for all parts of a multipart upload.
// Returns a slice of UploadPart structs with part numbers and presigned URLs.
func (c *S3Client) GeneratePresignedUploadParts(key, uploadID string, partCount int, expiry time.Duration) ([]UploadPart, error) {
	parts := make([]UploadPart, partCount)

	for i := 1; i <= partCount; i++ {
		url, err := c.GeneratePresignedUploadPart(key, uploadID, i, expiry)
		if err != nil {
			return nil, fmt.Errorf("failed to generate presigned URL for part %d: %w", i, err)
		}
		parts[i-1] = UploadPart{
			PartNumber:   i,
			PresignedURL: url,
		}
	}

	c.logger.WithFields(logging.Fields{
		"bucket":     c.config.Bucket,
		"key":        key,
		"upload_id":  uploadID,
		"part_count": partCount,
		"expiry":     expiry,
	}).Info("Generated presigned URLs for multipart upload")

	return parts, nil
}

// CompleteMultipartUpload finalizes a multipart upload after all parts are uploaded.
// Parts must include PartNumber and ETag for each uploaded part.
func (c *S3Client) CompleteMultipartUpload(ctx context.Context, key, uploadID string, parts []CompletedPart) error {
	fullKey := c.fullKey(key)

	// Convert to S3 types
	completedParts := make([]types.CompletedPart, len(parts))
	for i, p := range parts {
		completedParts[i] = types.CompletedPart{
			PartNumber: aws.Int32(int32(p.PartNumber)),
			ETag:       aws.String(p.ETag),
		}
	}

	_, err := c.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(c.config.Bucket),
		Key:      aws.String(fullKey),
		UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: completedParts,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to complete multipart upload: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"bucket":    c.config.Bucket,
		"key":       fullKey,
		"upload_id": uploadID,
		"parts":     len(parts),
	}).Info("Completed multipart upload")

	return nil
}

// AbortMultipartUpload cancels an in-progress multipart upload.
// This should be called if the upload fails or is cancelled.
func (c *S3Client) AbortMultipartUpload(ctx context.Context, key, uploadID string) error {
	fullKey := c.fullKey(key)

	_, err := c.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(c.config.Bucket),
		Key:      aws.String(fullKey),
		UploadId: aws.String(uploadID),
	})
	if err != nil {
		return fmt.Errorf("failed to abort multipart upload: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"bucket":    c.config.Bucket,
		"key":       fullKey,
		"upload_id": uploadID,
	}).Info("Aborted multipart upload")

	return nil
}

// BuildVodS3Key builds the S3 key for a VOD upload
func (c *S3Client) BuildVodS3Key(tenantID, artifactHash, filename string) string {
	// Extract extension from filename
	ext := "mp4" // default
	if idx := strings.LastIndex(filename, "."); idx >= 0 && idx < len(filename)-1 {
		ext = filename[idx+1:]
	}
	return fmt.Sprintf("vod/%s/%s/%s.%s", tenantID, artifactHash, artifactHash, ext)
}
