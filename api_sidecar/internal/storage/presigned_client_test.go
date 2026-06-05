package storage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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

func TestUploadBytesToPresignedURLRetriesReplayableBody(t *testing.T) {
	var attempts int
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll failed: %v", err)
		}
		bodies = append(bodies, string(body))
		if attempts == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewPresignedClient(logging.NewLogger())
	err := client.UploadBytesToPresignedURL(context.Background(), server.URL, []byte("thumbnail"), nil)
	if err != nil {
		t.Fatalf("UploadBytesToPresignedURL returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	for i, body := range bodies {
		if body != "thumbnail" {
			t.Fatalf("body[%d] = %q, want thumbnail", i, body)
		}
	}
}

func TestUploadFileToPresignedURLRetriesReplayableBody(t *testing.T) {
	var attempts int
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll failed: %v", err)
		}
		bodies = append(bodies, string(body))
		if attempts == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	localPath := filepath.Join(t.TempDir(), "sprite.jpg")
	if err := os.WriteFile(localPath, []byte("jpeg"), 0o644); err != nil {
		t.Fatalf("write local file: %v", err)
	}

	client := NewPresignedClient(logging.NewLogger())
	err := client.UploadFileToPresignedURL(context.Background(), server.URL, localPath, nil)
	if err != nil {
		t.Fatalf("UploadFileToPresignedURL returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	for i, body := range bodies {
		if body != "jpeg" {
			t.Fatalf("body[%d] = %q, want jpeg", i, body)
		}
	}
}
