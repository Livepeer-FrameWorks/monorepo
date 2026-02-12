package federation

import "testing"

func TestIsSameBucketNormalizesBucketCaseAndEndpoint(t *testing.T) {
	local := ClusterS3Config{S3Bucket: "Shared-Bucket", S3Endpoint: "https://s3.us-east-1.amazonaws.com/"}
	remote := ClusterS3Config{S3Bucket: "shared-bucket", S3Endpoint: "s3.us-east-1.amazonaws.com"}

	if !IsSameBucket(local, remote) {
		t.Fatal("expected same bucket affinity")
	}
}

func TestIsSameBucketRejectsDifferentEndpoints(t *testing.T) {
	local := ClusterS3Config{S3Bucket: "shared-bucket", S3Endpoint: "https://minio-a.example:9000"}
	remote := ClusterS3Config{S3Bucket: "shared-bucket", S3Endpoint: "https://minio-b.example:9000"}

	if IsSameBucket(local, remote) {
		t.Fatal("expected affinity rejection for different endpoints")
	}
}

func TestNormalizeEndpoint_StripsDefaultPorts(t *testing.T) {
	if got := normalizeEndpoint("https://s3.example.com:443/"); got != "s3.example.com" {
		t.Fatalf("expected default https port stripped, got %q", got)
	}
	if got := normalizeEndpoint("http://s3.example.com:80/"); got != "s3.example.com" {
		t.Fatalf("expected default http port stripped, got %q", got)
	}
}

func TestNormalizeEndpoint_InvalidURLFallsBackToLowercase(t *testing.T) {
	if got := normalizeEndpoint("https://[::1"); got != "https://[::1" {
		t.Fatalf("expected lowercase fallback for invalid URL, got %q", got)
	}
}
