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
