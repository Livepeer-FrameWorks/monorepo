package storage

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestSanitizePresignedRequestErrorRedactsSignedURL(t *testing.T) {
	err := &url.Error{
		Op:  "Put",
		URL: "https://object.example/frameworks/dev/poster.jpg?X-Amz-Credential=secret&X-Amz-Signature=sig",
		Err: errors.New("transport failed"),
	}

	got := sanitizePresignedRequestError(err).Error()
	if strings.Contains(got, "X-Amz-") || strings.Contains(got, "secret") || strings.Contains(got, "sig") {
		t.Fatalf("sanitizePresignedRequestError leaked signed query: %q", got)
	}
	if got != "Put failed: transport failed" {
		t.Fatalf("sanitizePresignedRequestError = %q", got)
	}
}

func TestSanitizePresignedRequestErrorRedactsURLText(t *testing.T) {
	err := errors.New(`Put "https://object.example/frameworks/dev/poster.jpg?X-Amz-Credential=secret&X-Amz-Signature=sig": retry failed`)

	got := sanitizePresignedRequestError(err).Error()
	if strings.Contains(got, "X-Amz-") || strings.Contains(got, "secret") || strings.Contains(got, "sig") {
		t.Fatalf("sanitizePresignedRequestError leaked signed query: %q", got)
	}
	if !strings.Contains(got, "https://object.example/frameworks/dev/poster.jpg?[redacted]") {
		t.Fatalf("sanitizePresignedRequestError did not keep redacted URL context: %q", got)
	}
}
