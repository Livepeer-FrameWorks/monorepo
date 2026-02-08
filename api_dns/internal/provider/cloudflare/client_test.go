package cloudflare

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestClient_RetriesOnRateLimit(t *testing.T) {
	var calls int32
	var methodErr atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Method != http.MethodGet {
			methodErr.Store(fmt.Errorf("expected GET request, got %s", r.Method))
		}
		if atomic.LoadInt32(&calls) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(APIResponse{
				Success: false,
				Errors:  []APIError{{Code: 1014, Message: "rate limited"}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(APIResponse{
			Success: true,
			Result:  []Monitor{},
		})
	}))
	defer server.Close()

	client := NewClient("token", "zone", "acct")
	client.baseURL = server.URL

	monitors, err := client.ListMonitors()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(monitors) != 0 {
		t.Fatalf("expected no monitors, got %d", len(monitors))
	}
	if errVal := methodErr.Load(); errVal != nil {
		t.Fatal(errVal.(error))
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected 2 calls for retry, got %d", calls)
	}
}

func TestClient_DoesNotRetryNonIdempotentRequests(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(APIResponse{
			Success: false,
			Errors:  []APIError{{Code: 1100, Message: "internal error"}},
		})
	}))
	defer server.Close()

	client := NewClient("token", "zone", "acct")
	client.baseURL = server.URL

	_, err := client.CreateMonitor(Monitor{Type: "http"})
	if err == nil {
		t.Fatal("expected error from API")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected 1 call for non-idempotent request, got %d", calls)
	}
}

func TestClient_APIErrorsIncludeCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(APIResponse{
			Success: false,
			Errors:  []APIError{{Code: 1001, Message: "bad request"}},
		})
	}))
	defer server.Close()

	client := NewClient("token", "zone", "acct")
	client.baseURL = server.URL

	_, err := client.ListPools()
	if err == nil {
		t.Fatal("expected error from API")
	}
	if err.Error() != "CloudFlare API error: bad request (code: 1001)" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestClient_TimeoutReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(APIResponse{
			Success: true,
			Result:  []Pool{},
		})
	}))
	defer server.Close()

	client := NewClient("token", "zone", "acct")
	client.baseURL = server.URL
	client.httpClient.Timeout = 10 * time.Millisecond
	// Avoid overriding the executor in tests.
	// The client implementation is responsible for closing response bodies.

	_, err := client.ListPools()
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if err.Error() == "" {
		t.Fatal("expected error message to be populated")
	}
}
