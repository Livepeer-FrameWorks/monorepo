package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// s3PartSize is the multipart chunk size. Objects smaller than one part are stored with a single PutObject. At
// 64 MiB the S3 limit of 10,000 parts allows objects of about 640 GiB. Tests lower it to exercise multipart uploads.
var s3PartSize = 64 << 20

// s3API is the part of *s3.Client a backup location uses.
type s3API interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	CreateMultipartUpload(ctx context.Context, params *s3.CreateMultipartUploadInput, optFns ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error)
	UploadPart(ctx context.Context, params *s3.UploadPartInput, optFns ...func(*s3.Options)) (*s3.UploadPartOutput, error)
	CompleteMultipartUpload(ctx context.Context, params *s3.CompleteMultipartUploadInput, optFns ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error)
	AbortMultipartUpload(ctx context.Context, params *s3.AbortMultipartUploadInput, optFns ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error)
}

// S3Prefix is a backup location under an S3 bucket prefix.
type S3Prefix struct {
	Bucket string
	Prefix string
	client s3API
}

// parseS3Location reads s3://bucket/prefix. Credentials, region and endpoint come from the standard AWS SDK
// configuration (AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY or a profile, AWS_REGION, AWS_ENDPOINT_URL_S3 or
// AWS_ENDPOINT_URL). A custom endpoint switches to path-style addressing, which S3-compatible stores such as
// Hetzner Object Storage, Cloudflare R2 and MinIO accept.
func parseS3Location(raw string) (Location, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("backup location %q: %w", raw, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("backup location %q: missing bucket", raw)
	}
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration for %s: %w", raw, err)
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	customEndpoint := strings.TrimSpace(os.Getenv("AWS_ENDPOINT_URL_S3")) != "" || strings.TrimSpace(os.Getenv("AWS_ENDPOINT_URL")) != ""
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = customEndpoint
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
	return NewS3Prefix(client, u.Host, strings.Trim(u.Path, "/")), nil
}

// NewS3Prefix builds a location around an S3 client.
func NewS3Prefix(client s3API, bucket, prefix string) S3Prefix {
	return S3Prefix{Bucket: bucket, Prefix: strings.Trim(prefix, "/"), client: client}
}

func (p S3Prefix) String() string {
	if p.Prefix == "" {
		return "s3://" + p.Bucket
	}
	return "s3://" + p.Bucket + "/" + p.Prefix
}

// Child returns the location under a sub-prefix.
func (p S3Prefix) Child(name string) Location {
	return S3Prefix{Bucket: p.Bucket, Prefix: strings.Trim(path.Join(p.Prefix, name), "/"), client: p.client}
}

func (p S3Prefix) key(name string) (string, error) {
	clean := path.Clean("/" + name)
	if clean == "/" {
		return "", fmt.Errorf("invalid backup object name %q", name)
	}
	return strings.TrimPrefix(path.Join(p.Prefix, clean), "/"), nil
}

// Create buffers one part at a time and uploads it; the object exists only after Commit.
func (p S3Prefix) Create(ctx context.Context, name string) (ObjectWriter, error) {
	key, err := p.key(name)
	if err != nil {
		return nil, err
	}
	return &s3Writer{ctx: ctx, api: p.client, bucket: p.Bucket, key: key}, nil
}

// Open streams an object.
func (p S3Prefix) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	key, err := p.key(name)
	if err != nil {
		return nil, err
	}
	out, err := p.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(p.Bucket), Key: aws.String(key)})
	if err != nil {
		var noKey *s3types.NoSuchKey
		var apiErr smithy.APIError
		if errors.As(err, &noKey) || (errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound")) {
			return nil, fmt.Errorf("s3://%s/%s: %w", p.Bucket, key, ErrNotFound)
		}
		return nil, fmt.Errorf("get s3://%s/%s: %w", p.Bucket, key, err)
	}
	return out.Body, nil
}

type s3Writer struct {
	ctx      context.Context
	api      s3API
	bucket   string
	key      string
	buf      bytes.Buffer
	uploadID string
	parts    []s3types.CompletedPart
	err      error
}

func (w *s3Writer) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	w.buf.Write(p)
	for w.buf.Len() >= s3PartSize {
		if err := w.uploadPart(w.buf.Next(s3PartSize)); err != nil {
			w.err = err
			return 0, err
		}
	}
	return len(p), nil
}

func (w *s3Writer) uploadPart(data []byte) error {
	if w.uploadID == "" {
		out, err := w.api.CreateMultipartUpload(w.ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(w.bucket), Key: aws.String(w.key)})
		if err != nil {
			return fmt.Errorf("start multipart upload of s3://%s/%s: %w", w.bucket, w.key, err)
		}
		w.uploadID = aws.ToString(out.UploadId)
	}
	number := int32(len(w.parts) + 1) //nolint:gosec // bounded by the 10,000-part S3 limit
	out, err := w.api.UploadPart(w.ctx, &s3.UploadPartInput{
		Bucket: aws.String(w.bucket), Key: aws.String(w.key), UploadId: aws.String(w.uploadID),
		PartNumber: aws.Int32(number), Body: bytes.NewReader(data), ContentLength: aws.Int64(int64(len(data))),
	})
	if err != nil {
		return fmt.Errorf("upload part %d of s3://%s/%s: %w", number, w.bucket, w.key, err)
	}
	w.parts = append(w.parts, s3types.CompletedPart{ETag: out.ETag, PartNumber: aws.Int32(number)})
	return nil
}

func (w *s3Writer) Commit() error {
	if w.err != nil {
		w.Abort()
		return w.err
	}
	if w.uploadID == "" {
		data := w.buf.Bytes()
		_, err := w.api.PutObject(w.ctx, &s3.PutObjectInput{
			Bucket: aws.String(w.bucket), Key: aws.String(w.key),
			Body: bytes.NewReader(data), ContentLength: aws.Int64(int64(len(data))),
		})
		if err != nil {
			return fmt.Errorf("put s3://%s/%s: %w", w.bucket, w.key, err)
		}
		return nil
	}
	if w.buf.Len() > 0 {
		if err := w.uploadPart(w.buf.Next(w.buf.Len())); err != nil {
			w.Abort()
			return err
		}
	}
	_, err := w.api.CompleteMultipartUpload(w.ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(w.bucket), Key: aws.String(w.key), UploadId: aws.String(w.uploadID),
		MultipartUpload: &s3types.CompletedMultipartUpload{Parts: w.parts},
	})
	if err != nil {
		w.Abort()
		return fmt.Errorf("complete multipart upload of s3://%s/%s: %w", w.bucket, w.key, err)
	}
	return nil
}

func (w *s3Writer) Abort() {
	if w.uploadID == "" {
		return
	}
	//nolint:errcheck // best effort; an abandoned upload is not visible as an object
	w.api.AbortMultipartUpload(context.Background(), &s3.AbortMultipartUploadInput{
		Bucket: aws.String(w.bucket), Key: aws.String(w.key), UploadId: aws.String(w.uploadID),
	})
	w.uploadID = ""
}
