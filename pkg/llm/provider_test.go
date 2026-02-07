package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestDoWithRetryRetryCount(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&count, 1)
		if n <= 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client := &http.Client{}
	resp, err := doWithRetry(context.Background(), client, func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, srv.URL, nil)
	})
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	defer resp.Body.Close()

	got := atomic.LoadInt32(&count)
	if got != 4 {
		t.Fatalf("expected exactly 4 attempts (3 retries + 1 success), got %d", got)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestDoWithRetryAllFailures(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	client := &http.Client{}
	_, err := doWithRetry(context.Background(), client, func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, srv.URL, nil)
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}

	got := atomic.LoadInt32(&count)
	// maxRetries=3, so attempts 0..3 = 4 total requests
	if got != int32(maxRetries)+1 {
		t.Fatalf("expected %d attempts, got %d", maxRetries+1, got)
	}
}
