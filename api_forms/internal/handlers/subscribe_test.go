package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"frameworks/pkg/clients"
	"frameworks/pkg/clients/listmonk"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/sirupsen/logrus/hooks/test"
)

// --- Stub-based unit tests ---

type listmonkStub struct {
	subscribeCalls []subscribeCall
	subscribeErr   error
	subscriberInfo *listmonk.SubscriberInfo
	subscriberErr  error
	subscriberOK   bool
}

type subscribeCall struct {
	email   string
	name    string
	listID  int
	confirm bool
}

func (s *listmonkStub) Subscribe(ctx context.Context, email, name string, listID int, preconfirm bool) error {
	s.subscribeCalls = append(s.subscribeCalls, subscribeCall{
		email:   email,
		name:    name,
		listID:  listID,
		confirm: preconfirm,
	})
	return s.subscribeErr
}

func (s *listmonkStub) GetSubscriber(ctx context.Context, email string) (*listmonk.SubscriberInfo, bool, error) {
	return s.subscriberInfo, s.subscriberOK, s.subscriberErr
}

type subscribeHarness struct {
	router *gin.Engine
	stub   *listmonkStub
}

func setupSubscribeHandler() *subscribeHarness {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	stub := &listmonkStub{}
	logger, _ := test.NewNullLogger()
	handler := NewSubscribeHandler(stub, nil, 99, false, logger, nil)
	router.POST("/api/subscribe", handler.Handle)
	return &subscribeHarness{router: router, stub: stub}
}

func TestSubscribeRejectsMalformedJSON(t *testing.T) {
	harness := setupSubscribeHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/subscribe", bytes.NewBufferString("{bad json"))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	harness.router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.Code)
	}
	if len(harness.stub.subscribeCalls) != 0 {
		t.Fatalf("expected no subscribe call")
	}
}

func TestSubscribeRejectsBotHeuristics(t *testing.T) {
	harness := setupSubscribeHandler()
	payload := map[string]interface{}{
		"email":        "user@example.com",
		"human_check":  "robot",
		"phone_number": "123",
		"behavior": map[string]interface{}{
			"formShownAt": float64(time.Now().Add(-10 * time.Second).UnixMilli()),
			"submittedAt": float64(time.Now().UnixMilli()),
			"mouse":       true,
			"typed":       true,
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/subscribe", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	harness.router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.Code)
	}
	if len(harness.stub.subscribeCalls) != 0 {
		t.Fatalf("expected no subscribe call")
	}
}

func TestSubscribeRejectsInvalidEmail(t *testing.T) {
	harness := setupSubscribeHandler()
	payload := map[string]interface{}{
		"email":       "bad-email",
		"human_check": "human",
		"behavior": map[string]interface{}{
			"formShownAt": float64(time.Now().Add(-10 * time.Second).UnixMilli()),
			"submittedAt": float64(time.Now().UnixMilli()),
			"mouse":       true,
			"typed":       true,
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/subscribe", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	harness.router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.Code)
	}
	if len(harness.stub.subscribeCalls) != 0 {
		t.Fatalf("expected no subscribe call")
	}
}

func TestSubscribeReturnsSuccessWhenAlreadySubscribed(t *testing.T) {
	harness := setupSubscribeHandler()
	harness.stub.subscriberOK = true
	harness.stub.subscriberInfo = &listmonk.SubscriberInfo{
		Status: "enabled",
		Lists: []listmonk.ListSubscription{
			{ListID: 99, Status: "confirmed"},
		},
	}
	payload := map[string]interface{}{
		"email":       "user@example.com",
		"human_check": "human",
		"behavior": map[string]interface{}{
			"formShownAt": float64(time.Now().Add(-10 * time.Second).UnixMilli()),
			"submittedAt": float64(time.Now().UnixMilli()),
			"mouse":       true,
			"typed":       true,
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/subscribe", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	harness.router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
	if len(harness.stub.subscribeCalls) != 0 {
		t.Fatalf("expected no subscribe call")
	}
}

func TestSubscribeRetriesWhenUnconfirmed(t *testing.T) {
	harness := setupSubscribeHandler()
	harness.stub.subscriberOK = true
	harness.stub.subscriberInfo = &listmonk.SubscriberInfo{
		Status: "enabled",
		Lists: []listmonk.ListSubscription{
			{ListID: 99, Status: "unconfirmed"},
		},
	}
	payload := map[string]interface{}{
		"email":       "user@example.com",
		"human_check": "human",
		"behavior": map[string]interface{}{
			"formShownAt": float64(time.Now().Add(-10 * time.Second).UnixMilli()),
			"submittedAt": float64(time.Now().UnixMilli()),
			"mouse":       true,
			"typed":       true,
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/subscribe", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	harness.router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
	if len(harness.stub.subscribeCalls) != 1 {
		t.Fatalf("expected subscribe to be called once")
	}
}

func TestSubscribeRejectsBlocklistedSubscriber(t *testing.T) {
	harness := setupSubscribeHandler()
	harness.stub.subscriberOK = true
	harness.stub.subscriberInfo = &listmonk.SubscriberInfo{
		Status: "blocklisted",
	}
	payload := map[string]interface{}{
		"email":       "user@example.com",
		"human_check": "human",
		"behavior": map[string]interface{}{
			"formShownAt": float64(time.Now().Add(-10 * time.Second).UnixMilli()),
			"submittedAt": float64(time.Now().UnixMilli()),
			"mouse":       true,
			"typed":       true,
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/subscribe", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	harness.router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.Code)
	}
	if len(harness.stub.subscribeCalls) != 0 {
		t.Fatalf("expected no subscribe call")
	}
}

func TestSubscribeHandlesListmonkConflict(t *testing.T) {
	harness := setupSubscribeHandler()
	harness.stub.subscribeErr = &listmonk.APIError{StatusCode: http.StatusConflict}
	payload := map[string]interface{}{
		"email":       "User@Example.com",
		"human_check": "human",
		"behavior": map[string]interface{}{
			"formShownAt": float64(time.Now().Add(-10 * time.Second).UnixMilli()),
			"submittedAt": float64(time.Now().UnixMilli()),
			"mouse":       true,
			"typed":       true,
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/subscribe", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	harness.router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
	if len(harness.stub.subscribeCalls) != 1 {
		t.Fatalf("expected one subscribe call")
	}
	if harness.stub.subscribeCalls[0].email != "user@example.com" {
		t.Fatalf("expected normalized email, got %s", harness.stub.subscribeCalls[0].email)
	}
}

// --- Observability tests ---

func TestSubscribeHandlerTurnstileErrorMapsToBadGateway(t *testing.T) {
	logger, _ := test.NewNullLogger()
	metrics := &FormMetrics{
		SubscribeRequests: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "subscribe_turnstile_total", Help: "subscribe requests"},
			[]string{"status"},
		),
	}

	handler := NewSubscribeHandler(
		&listmonkStub{},
		&fakeTurnstile{err: errors.New("turnstile down")},
		1,
		true,
		logger,
		metrics,
	)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/subscribe", handler.Handle)

	body := newSubscribeBody(t, "user@example.com")
	w := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/api/subscribe", bytes.NewBuffer(body))
	httpReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, httpReq)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected status %d, got %d", http.StatusBadGateway, w.Code)
	}
	if got := testutil.ToFloat64(metrics.SubscribeRequests.WithLabelValues("turnstile_error")); got != 1.0 {
		t.Fatalf("expected turnstile_error metric 1.0, got %f", got)
	}
}

func TestSubscribeHandlerListmonkErrorMapsToBadGateway(t *testing.T) {
	logger, _ := test.NewNullLogger()
	metrics := &FormMetrics{
		SubscribeRequests: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "subscribe_listmonk_total", Help: "subscribe requests"},
			[]string{"status"},
		),
	}

	handler := NewSubscribeHandler(
		&listmonkStub{subscribeErr: errors.New("listmonk down")},
		nil,
		1,
		false,
		logger,
		metrics,
	)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/subscribe", handler.Handle)

	body := newSubscribeBody(t, "user@example.com")
	w := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/api/subscribe", bytes.NewBuffer(body))
	httpReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, httpReq)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected status %d, got %d", http.StatusBadGateway, w.Code)
	}
	if got := testutil.ToFloat64(metrics.SubscribeRequests.WithLabelValues("listmonk_error")); got != 1.0 {
		t.Fatalf("expected listmonk_error metric 1.0, got %f", got)
	}
}

// --- Integration tests (real listmonk client with httptest servers) ---

func TestSubscribeRetriesWithBackoff(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var mu sync.Mutex
	var postTimes []time.Time

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"results":[]}}`))
		case http.MethodPost:
			mu.Lock()
			postTimes = append(postTimes, time.Now())
			attempt := len(postTimes)
			mu.Unlock()

			if attempt == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	executorCfg := clients.HTTPExecutorConfig{
		MaxRetries:  1,
		BaseDelay:   25 * time.Millisecond,
		MaxDelay:    25 * time.Millisecond,
		ShouldRetry: clients.DefaultShouldRetry,
	}
	httpClient := &http.Client{Timeout: 500 * time.Millisecond}
	lmClient := listmonk.NewClient(
		server.URL,
		"user",
		"pass",
		listmonk.WithHTTPClient(httpClient),
		listmonk.WithHTTPExecutorConfig(executorCfg),
	)
	logger, _ := test.NewNullLogger()

	handler := NewSubscribeHandler(lmClient, nil, 42, false, logger, nil)
	router := gin.New()
	router.POST("/api/subscribe", handler.Handle)

	body := newSubscribeBody(t, "retry@example.com")
	req := httptest.NewRequest(http.MethodPost, "/api/subscribe", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	mu.Lock()
	if len(postTimes) != 2 {
		mu.Unlock()
		t.Fatalf("expected 2 POST attempts, got %d", len(postTimes))
	}
	delay := postTimes[1].Sub(postTimes[0])
	mu.Unlock()

	// Allow 5ms jitter tolerance for timer imprecision on slow/busy runners
	if delay < executorCfg.BaseDelay-5*time.Millisecond {
		t.Fatalf("expected retry delay >= ~%s, got %s", executorCfg.BaseDelay, delay)
	}
}

func TestSubscribeSuppressesDuplicateSubmission(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var mu sync.Mutex
	getCount := 0
	postCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			getCount++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"results":[{"id":12,"status":"enabled","lists":[{"id":42,"subscription_status":"confirmed"}]}]}}`))
		case http.MethodPost:
			postCount++
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	httpClient := &http.Client{Timeout: 500 * time.Millisecond}
	lmClient := listmonk.NewClient(
		server.URL,
		"user",
		"pass",
		listmonk.WithHTTPClient(httpClient),
	)
	logger, _ := test.NewNullLogger()

	handler := NewSubscribeHandler(lmClient, nil, 42, false, logger, nil)
	router := gin.New()
	router.POST("/api/subscribe", handler.Handle)

	body := newSubscribeBody(t, "dupe@example.com")
	req := httptest.NewRequest(http.MethodPost, "/api/subscribe", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if payload["success"] != true {
		t.Fatalf("expected success true, got %v", payload["success"])
	}

	mu.Lock()
	defer mu.Unlock()
	if getCount != 1 {
		t.Fatalf("expected 1 GET request, got %d", getCount)
	}
	if postCount != 0 {
		t.Fatalf("expected 0 POST requests for duplicate, got %d", postCount)
	}
}

func TestSubscribeTimeoutReturnsGatewayTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var mu sync.Mutex
	postCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"results":[]}}`))
		case http.MethodPost:
			mu.Lock()
			postCount++
			mu.Unlock()
			time.Sleep(50 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	executorCfg := clients.HTTPExecutorConfig{
		MaxRetries:  1,
		BaseDelay:   5 * time.Millisecond,
		MaxDelay:    5 * time.Millisecond,
		ShouldRetry: clients.DefaultShouldRetry,
	}
	httpClient := &http.Client{Timeout: 10 * time.Millisecond}
	lmClient := listmonk.NewClient(
		server.URL,
		"user",
		"pass",
		listmonk.WithHTTPClient(httpClient),
		listmonk.WithHTTPExecutorConfig(executorCfg),
	)
	logger, _ := test.NewNullLogger()

	handler := NewSubscribeHandler(lmClient, nil, 42, false, logger, nil)
	router := gin.New()
	router.POST("/api/subscribe", handler.Handle)

	body := newSubscribeBody(t, "timeout@example.com")
	req := httptest.NewRequest(http.MethodPost, "/api/subscribe", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected status 504, got %d", recorder.Code)
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if payload["error"] != "Subscription service timeout" {
		t.Fatalf("expected timeout error message, got %v", payload["error"])
	}

	mu.Lock()
	defer mu.Unlock()
	if postCount != 2 {
		t.Fatalf("expected 2 POST attempts, got %d", postCount)
	}
}

func newSubscribeBody(t *testing.T, email string) []byte {
	t.Helper()
	payload := map[string]any{
		"email":       email,
		"name":        "Test User",
		"human_check": "human",
		"behavior": map[string]any{
			"formShownAt": 0,
			"submittedAt": 4000,
			"mouse":       true,
			"typed":       true,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}
	return body
}
