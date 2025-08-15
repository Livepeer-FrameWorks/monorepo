package clients

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDoWithRetry_SucceedsWithoutRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer server.Close()

	req, _ := http.NewRequest("GET", server.URL, nil)
	resp, err := DoWithRetry(context.Background(), server.Client(), req, DefaultRetryConfig())
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("expected 200 without error; got %v %d", err, resp.StatusCode)
	}
}

func TestDoWithRetry_RetriesOn500(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
	}))
	defer server.Close()

	cfg := DefaultRetryConfig()
	cfg.BaseDelay = 1 * time.Millisecond
	cfg.MaxDelay = 2 * time.Millisecond

	req, _ := http.NewRequest("GET", server.URL, nil)
	resp, err := DoWithRetry(context.Background(), server.Client(), req, cfg)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("expected eventual 200; got %v %d", err, resp.StatusCode)
	}
}

func TestDoWithRetry_RespectsContextCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(50 * time.Millisecond); w.WriteHeader(200) }))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	req, _ := http.NewRequest("GET", server.URL, nil)
	cfg := DefaultRetryConfig()
	_, err := DoWithRetry(ctx, server.Client(), req, cfg)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}
}
