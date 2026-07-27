package storage

import (
	"strings"
	"testing"
)

func TestParseS3URL_StandardURL(t *testing.T) {
	c := &S3Client{config: S3Config{Bucket: "mybucket"}}

	key, err := c.ParseS3URL("s3://mybucket/clips/tenant/stream/hash.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if key != "clips/tenant/stream/hash.mp4" {
		t.Fatalf("expected full key, got %s", key)
	}
}

func TestParseS3URL_WithPrefix(t *testing.T) {
	c := &S3Client{config: S3Config{Bucket: "mybucket", Prefix: "prod"}}

	key, err := c.ParseS3URL("s3://mybucket/prod/clips/tenant/stream/hash.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if key != "clips/tenant/stream/hash.mp4" {
		t.Fatalf("expected key without prefix, got %s", key)
	}
}

func TestParseS3URL_NoPrefix(t *testing.T) {
	c := &S3Client{config: S3Config{Bucket: "mybucket"}}

	key, err := c.ParseS3URL("s3://mybucket/some/key")
	if err != nil {
		t.Fatal(err)
	}
	if key != "some/key" {
		t.Fatalf("expected some/key, got %s", key)
	}
}

func TestParseS3URL_WrongScheme(t *testing.T) {
	c := &S3Client{config: S3Config{Bucket: "mybucket"}}

	_, err := c.ParseS3URL("https://s3.amazonaws.com/mybucket/key")
	if err == nil {
		t.Fatal("expected error for non-s3:// scheme")
	}
	if !strings.Contains(err.Error(), "not an s3://") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseS3URL_NoKey(t *testing.T) {
	c := &S3Client{config: S3Config{Bucket: "mybucket"}}

	_, err := c.ParseS3URL("s3://mybucket")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	if !strings.Contains(err.Error(), "no key") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ParseLocalS3URL is the bucket-guarded parser: it strips the local bucket/prefix like ParseS3URL, but ERRORS
// on a foreign-bucket URL so a remote-provider pointer can never be rewritten/routed as a local key.
func TestParseLocalS3URL_LocalBucketStripsKey(t *testing.T) {
	c := &S3Client{config: S3Config{Bucket: "mybucket", Prefix: "prod"}}

	key, err := c.ParseLocalS3URL("s3://mybucket/prod/clips/tenant/hash.mp4")
	if err != nil {
		t.Fatalf("local bucket must parse: %v", err)
	}
	if key != "clips/tenant/hash.mp4" {
		t.Fatalf("expected prefix-stripped key, got %s", key)
	}
}

func TestParseLocalS3URL_ForeignBucketErrors(t *testing.T) {
	c := &S3Client{config: S3Config{Bucket: "mybucket"}}

	if _, err := c.ParseLocalS3URL("s3://otherbucket/clips/tenant/hash.mp4"); err == nil {
		t.Fatal("a foreign-bucket URL must NOT parse as local")
	} else if !strings.Contains(err.Error(), "not the local bucket") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseLocalS3URL_WrongScheme(t *testing.T) {
	c := &S3Client{config: S3Config{Bucket: "mybucket"}}
	if _, err := c.ParseLocalS3URL("https://s3.amazonaws.com/mybucket/key"); err == nil {
		t.Fatal("expected error for non-s3:// scheme")
	}
}
