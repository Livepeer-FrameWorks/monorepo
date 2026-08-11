package control

import (
	"testing"
)

func TestBackendFingerprint(t *testing.T) {
	base := BackendFingerprint("s3", "bucket-1", "https://s3.example", "us-east-1", "artifacts")

	// EXACT descriptor: the ONLY normalization is region omitted→us-east-1. An empty region equals us-east-1.
	if got := BackendFingerprint("s3", "bucket-1", "https://s3.example", "", "artifacts"); got != base {
		t.Fatalf("empty region must default to us-east-1: %q != %q", got, base)
	}

	// A repoint (any PHYSICAL identity field changes) MUST yield a different id — that is the whole point. This
	// includes WHITESPACE and CASE in bucket/endpoint/prefix: they name a distinct physical keyspace, and matching
	// Foghorn's byte-for-byte cell identity means the fingerprint must NOT collapse them (`prod` vs ` prod` are
	// different stores, and cleanup must fail closed on the mismatch).
	for _, tc := range []struct {
		name, kind, bucket, endpoint, reg, prefix string
	}{
		{"bucket", "s3", "bucket-2", "https://s3.example", "us-east-1", "artifacts"},
		{"endpoint", "s3", "bucket-1", "https://other.example", "us-east-1", "artifacts"},
		{"region", "s3", "bucket-1", "https://s3.example", "eu-west-1", "artifacts"},
		{"prefix", "s3", "bucket-1", "https://s3.example", "us-east-1", "thumbs"},
		{"kind", "local_raid", "bucket-1", "https://s3.example", "us-east-1", "artifacts"},
		{"prefix leading space", "s3", "bucket-1", "https://s3.example", "us-east-1", " artifacts"},
		{"prefix trailing space", "s3", "bucket-1", "https://s3.example", "us-east-1", "artifacts "},
		{"bucket trailing space", "s3", "bucket-1 ", "https://s3.example", "us-east-1", "artifacts"},
		{"endpoint case", "s3", "bucket-1", "https://S3.EXAMPLE", "us-east-1", "artifacts"},
		{"endpoint whitespace", "s3", "bucket-1", " https://s3.example ", "us-east-1", "artifacts"},
	} {
		if got := BackendFingerprint(tc.kind, tc.bucket, tc.endpoint, tc.reg, tc.prefix); got == base {
			t.Fatalf("a %s change must mint a new backend_id, got the same", tc.name)
		}
	}

	// The logical cluster is NOT part of the physical identity: two clusters on the same store share one id.
	if got := BackendFingerprint("s3", "bucket-1", "https://s3.example", "us-east-1", "artifacts"); got != base {
		t.Fatal("cluster is metadata, not identity")
	}
}
