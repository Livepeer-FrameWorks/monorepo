package storage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
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

func TestUploadToPresignedURLClampsBodyToContentLength(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength != 3 {
			t.Errorf("ContentLength = %d, want 3", r.ContentLength)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll failed: %v", err)
		}
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewPresignedClient(logging.NewLogger())
	err := client.UploadToPresignedURL(context.Background(), server.URL, strings.NewReader("abcdef"), 3, nil)
	if err != nil {
		t.Fatalf("UploadToPresignedURL returned error: %v", err)
	}
	if gotBody != "abc" {
		t.Fatalf("uploaded body = %q, want %q", gotBody, "abc")
	}
}
