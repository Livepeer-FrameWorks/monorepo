package listmonk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestClient creates a client without an executor so tests use the direct client.Do path.
// This avoids retry policies wrapping errors as ExceededError.
func newTestClient(baseURL string) *Client {
	return &Client{
		baseURL:  baseURL,
		username: "test-user",
		password: "test-pass",
		client:   &http.Client{},
	}
}

func TestNewClientDefaults(t *testing.T) {
	c := NewClient("http://localhost", "user", "pass")
	if c.baseURL != "http://localhost" {
		t.Fatalf("expected baseURL http://localhost, got %s", c.baseURL)
	}
	if c.username != "user" {
		t.Fatalf("expected username user, got %s", c.username)
	}
	if c.password != "pass" {
		t.Fatalf("expected password pass, got %s", c.password)
	}
	if c.client == nil {
		t.Fatal("expected non-nil HTTP client")
	}
	if c.client.Timeout != 10*time.Second {
		t.Fatalf("expected timeout 10s, got %v", c.client.Timeout)
	}
	if c.httpExecutor == nil {
		t.Fatal("expected non-nil httpExecutor")
	}
	if c.shouldRetry == nil {
		t.Fatal("expected non-nil shouldRetry")
	}
}

func TestWithHTTPClientOption(t *testing.T) {
	custom := &http.Client{}
	c := NewClient("http://localhost", "u", "p", WithHTTPClient(custom))
	if c.client != custom {
		t.Fatal("expected custom HTTP client")
	}
}

func TestWithHTTPClientNilIgnored(t *testing.T) {
	c := NewClient("http://localhost", "u", "p", WithHTTPClient(nil))
	if c.client == nil {
		t.Fatal("nil client should not replace default")
	}
}

func TestWithHTTPExecutorNilIgnored(t *testing.T) {
	c := NewClient("http://localhost", "u", "p", WithHTTPExecutor(nil, nil))
	if c.httpExecutor == nil {
		t.Fatal("nil executor should not replace default")
	}
}

func TestSubscribeSuccess(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody SubscriberRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")

		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	err := c.Subscribe(context.Background(), "test@example.com", "Test User", 42, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotMethod != "POST" {
		t.Fatalf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/api/subscribers" {
		t.Fatalf("expected /api/subscribers, got %s", gotPath)
	}
	if gotAuth == "" {
		t.Fatal("expected basic auth header")
	}
	if gotBody.Email != "test@example.com" {
		t.Fatalf("expected email test@example.com, got %s", gotBody.Email)
	}
	if gotBody.Name != "Test User" {
		t.Fatalf("expected name Test User, got %s", gotBody.Name)
	}
	if gotBody.Status != "enabled" {
		t.Fatalf("expected status enabled, got %s", gotBody.Status)
	}
	if len(gotBody.Lists) != 1 || gotBody.Lists[0] != 42 {
		t.Fatalf("expected lists [42], got %v", gotBody.Lists)
	}
	if gotBody.Preconfirm {
		t.Fatal("expected preconfirm=false")
	}
}

func TestSubscribeAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	err := c.Subscribe(context.Background(), "test@example.com", "Test", 1, false)
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", apiErr.StatusCode)
	}
}

func TestSubscribeConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	err := c.Subscribe(context.Background(), "test@example.com", "Test", 1, false)
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.StatusCode != http.StatusConflict {
		t.Fatalf("expected status 409, got %d", apiErr.StatusCode)
	}
}

func TestBlocklistSuccess(t *testing.T) {
	var gotBody SubscriberRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	err := c.Blocklist(context.Background(), "spam@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody.Email != "spam@example.com" {
		t.Fatalf("expected email spam@example.com, got %s", gotBody.Email)
	}
	if gotBody.Status != "blocklisted" {
		t.Fatalf("expected status blocklisted, got %s", gotBody.Status)
	}
}

func TestBlocklistAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	err := c.Blocklist(context.Background(), "test@example.com")
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.StatusCode != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", apiErr.StatusCode)
	}
}

func TestGetSubscriberFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"data": {
				"results": [{
					"id": 99,
					"status": "enabled",
					"lists": [
						{"id": 5, "subscription_status": "confirmed"},
						{"id": 10, "subscription_status": "unconfirmed"}
					]
				}]
			}
		}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	info, exists, err := c.GetSubscriber(context.Background(), "found@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Fatal("expected exists=true")
	}
	if info.ID != 99 {
		t.Fatalf("expected ID 99, got %d", info.ID)
	}
	if info.Status != "enabled" {
		t.Fatalf("expected status enabled, got %s", info.Status)
	}
	if len(info.Lists) != 2 {
		t.Fatalf("expected 2 lists, got %d", len(info.Lists))
	}
	if info.Lists[0].ListID != 5 || info.Lists[0].Status != "confirmed" {
		t.Fatalf("expected list 5 confirmed, got %+v", info.Lists[0])
	}
	if info.Lists[1].ListID != 10 || info.Lists[1].Status != "unconfirmed" {
		t.Fatalf("expected list 10 unconfirmed, got %+v", info.Lists[1])
	}
}

func TestGetSubscriberNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data": {"results": []}}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	info, exists, err := c.GetSubscriber(context.Background(), "missing@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false")
	}
	if info != nil {
		t.Fatal("expected nil info")
	}
}

func TestGetSubscriberAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, _, err := c.GetSubscriber(context.Background(), "test@example.com")
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", apiErr.StatusCode)
	}
}

func TestGetSubscriberDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `not-json`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, _, err := c.GetSubscriber(context.Background(), "test@example.com")
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestGetSubscriberEmailEscaping(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data": {"results": []}}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, _, _ = c.GetSubscriber(context.Background(), "o'brien@example.com")

	expected := "subscribers.email='o''brien@example.com'"
	if gotQuery != expected {
		t.Fatalf("expected query %q, got %q", expected, gotQuery)
	}
}

func TestUnsubscribeSuccess(t *testing.T) {
	var gotMethod string
	var gotBody struct {
		IDs           []int  `json:"ids"`
		Action        string `json:"action"`
		TargetListIDs []int  `json:"target_list_ids"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	err := c.Unsubscribe(context.Background(), 42, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != "PUT" {
		t.Fatalf("expected PUT, got %s", gotMethod)
	}
	if len(gotBody.IDs) != 1 || gotBody.IDs[0] != 42 {
		t.Fatalf("expected IDs [42], got %v", gotBody.IDs)
	}
	if gotBody.Action != "unsubscribe" {
		t.Fatalf("expected action unsubscribe, got %s", gotBody.Action)
	}
	if len(gotBody.TargetListIDs) != 1 || gotBody.TargetListIDs[0] != 5 {
		t.Fatalf("expected target_list_ids [5], got %v", gotBody.TargetListIDs)
	}
}

func TestUnsubscribeAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	err := c.Unsubscribe(context.Background(), 42, 5)
	if err == nil {
		t.Fatal("expected error")
	}
	// Unsubscribe uses fmt.Errorf, not *APIError (design inconsistency)
	if err.Error() != "listmonk returned status: 404" {
		t.Fatalf("expected 'listmonk returned status: 404', got %q", err.Error())
	}
}

func TestAPIErrorMessage(t *testing.T) {
	e := &APIError{StatusCode: 500}
	want := "listmonk returned status: 500"
	if e.Error() != want {
		t.Fatalf("expected %q, got %q", want, e.Error())
	}
}

func TestIsSubscribedToList(t *testing.T) {
	tests := []struct {
		name   string
		info   *SubscriberInfo
		listID int
		want   bool
	}{
		{
			name:   "nil info",
			info:   nil,
			listID: 1,
			want:   false,
		},
		{
			name:   "blocklisted",
			info:   &SubscriberInfo{Status: "blocklisted", Lists: []ListSubscription{{ListID: 1, Status: "confirmed"}}},
			listID: 1,
			want:   false,
		},
		{
			name:   "confirmed match",
			info:   &SubscriberInfo{Status: "enabled", Lists: []ListSubscription{{ListID: 5, Status: "confirmed"}}},
			listID: 5,
			want:   true,
		},
		{
			name:   "unconfirmed match",
			info:   &SubscriberInfo{Status: "enabled", Lists: []ListSubscription{{ListID: 5, Status: "unconfirmed"}}},
			listID: 5,
			want:   false,
		},
		{
			name:   "wrong list ID",
			info:   &SubscriberInfo{Status: "enabled", Lists: []ListSubscription{{ListID: 5, Status: "confirmed"}}},
			listID: 99,
			want:   false,
		},
		{
			name:   "empty lists",
			info:   &SubscriberInfo{Status: "enabled"},
			listID: 1,
			want:   false,
		},
		{
			name: "multiple lists, one confirmed",
			info: &SubscriberInfo{
				Status: "enabled",
				Lists: []ListSubscription{
					{ListID: 1, Status: "unconfirmed"},
					{ListID: 2, Status: "confirmed"},
					{ListID: 3, Status: "unsubscribed"},
				},
			},
			listID: 2,
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.info.IsSubscribedToList(tt.listID)
			if got != tt.want {
				t.Fatalf("IsSubscribedToList(%d) = %v, want %v", tt.listID, got, tt.want)
			}
		})
	}
}

func TestDoRequestWithoutExecutor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	// httpExecutor is nil, so doRequest should call client.Do directly
	resp, err := c.doRequest(context.Background(), func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestSubscribePreconfirmTrue(t *testing.T) {
	var gotBody SubscriberRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_ = c.Subscribe(context.Background(), "test@example.com", "Test", 1, true)
	if !gotBody.Preconfirm {
		t.Fatal("expected preconfirm=true")
	}
}

func TestSubscribeContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.Subscribe(ctx, "test@example.com", "Test", 1, false)
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
}

func TestSubscribeStatus399NotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(399)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	err := c.Subscribe(context.Background(), "test@example.com", "Test", 1, false)
	if err != nil {
		t.Fatalf("status 399 should not be an error, got: %v", err)
	}
}

func TestSubscribeStatus400IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	err := c.Subscribe(context.Background(), "test@example.com", "Test", 1, false)
	if err == nil {
		t.Fatal("status 400 should be an error")
	}
}

func TestBlocklistStatus399NotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(399)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	err := c.Blocklist(context.Background(), "test@example.com")
	if err != nil {
		t.Fatalf("status 399 should not be an error, got: %v", err)
	}
}

func TestBlocklistStatus400IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	err := c.Blocklist(context.Background(), "test@example.com")
	if err == nil {
		t.Fatal("status 400 should be an error")
	}
}

func TestGetSubscriberStatus399NotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(399)
		_, _ = fmt.Fprint(w, `{"data":{"results":[]}}`)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, _, err := c.GetSubscriber(context.Background(), "test@example.com")
	if err != nil {
		t.Fatalf("status 399 should not be an error, got: %v", err)
	}
}

func TestGetSubscriberStatus400IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, _, err := c.GetSubscriber(context.Background(), "test@example.com")
	if err == nil {
		t.Fatal("status 400 should be an error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError for status 400, got %T: %v", err, err)
	}
}

func TestUnsubscribeStatus399NotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(399)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	err := c.Unsubscribe(context.Background(), 42, 5)
	if err != nil {
		t.Fatalf("status 399 should not be an error, got: %v", err)
	}
}

func TestUnsubscribeStatus400IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	err := c.Unsubscribe(context.Background(), 42, 5)
	if err == nil {
		t.Fatal("status 400 should be an error")
	}
}
