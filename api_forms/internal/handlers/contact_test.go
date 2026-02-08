package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"frameworks/api_forms/internal/validation"
	"frameworks/pkg/turnstile"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
)

// --- Shared fakes ---

type emailSenderStub struct {
	calls []emailCall
	err   error
}

type emailCall struct {
	to      string
	subject string
	body    string
}

func (s *emailSenderStub) SendMail(ctx context.Context, to, subject, htmlBody string) error {
	s.calls = append(s.calls, emailCall{to: to, subject: subject, body: htmlBody})
	return s.err
}

type fakeTurnstile struct {
	resp *turnstile.VerifyResponse
	err  error
}

func (f *fakeTurnstile) Verify(ctx context.Context, token, remoteIP string) (*turnstile.VerifyResponse, error) {
	return f.resp, f.err
}

// --- Stub-based unit tests ---

type contactHandlerHarness struct {
	router *gin.Engine
	sender *emailSenderStub
}

func setupContactHandler(turnstileEnabled bool) *contactHandlerHarness {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	sender := &emailSenderStub{}
	logger, _ := test.NewNullLogger()
	handler := NewContactHandler(sender, nil, "contact@example.com", turnstileEnabled, logger, nil)
	router.POST("/api/contact", handler.Handle)
	return &contactHandlerHarness{router: router, sender: sender}
}

func TestContactHandlerRejectsMalformedJSON(t *testing.T) {
	harness := setupContactHandler(false)
	req := httptest.NewRequest(http.MethodPost, "/api/contact", bytes.NewBufferString("{bad json"))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	harness.router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.Code)
	}
	if len(harness.sender.calls) != 0 {
		t.Fatalf("expected no email send")
	}
}

func TestContactHandlerValidatesRequiredFields(t *testing.T) {
	harness := setupContactHandler(false)
	payload := map[string]interface{}{
		"name":        "A",
		"email":       "bad",
		"message":     "short",
		"human_check": "robot",
		"behavior": map[string]interface{}{
			"formShownAt": float64(time.Now().Add(-10 * time.Second).UnixMilli()),
			"submittedAt": float64(time.Now().UnixMilli()),
			"mouse":       true,
			"typed":       true,
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/contact", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	harness.router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.Code)
	}
	if len(harness.sender.calls) != 0 {
		t.Fatalf("expected no email send")
	}
}

func TestContactHandlerBlocksSpamKeywords(t *testing.T) {
	harness := setupContactHandler(false)
	payload := map[string]interface{}{
		"name":        "Jane Doe",
		"email":       "jane@example.com",
		"message":     "This is about crypto investment opportunities.",
		"human_check": "human",
		"behavior": map[string]interface{}{
			"formShownAt": float64(time.Now().Add(-10 * time.Second).UnixMilli()),
			"submittedAt": float64(time.Now().UnixMilli()),
			"mouse":       true,
			"typed":       true,
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/contact", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	harness.router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.Code)
	}
	if len(harness.sender.calls) != 0 {
		t.Fatalf("expected no email send")
	}
}

func TestContactHandlerAcceptsValidSubmission(t *testing.T) {
	harness := setupContactHandler(false)
	payload := map[string]interface{}{
		"name":        "Jane Doe",
		"email":       "jane@example.com",
		"message":     "Hello there, looking forward to learning more.",
		"human_check": "human",
		"behavior": map[string]interface{}{
			"formShownAt": float64(time.Now().Add(-10 * time.Second).UnixMilli()),
			"submittedAt": float64(time.Now().UnixMilli()),
			"mouse":       true,
			"typed":       true,
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/contact", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	harness.router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
	if len(harness.sender.calls) != 1 {
		t.Fatalf("expected one email send")
	}
}

// --- Observability tests ---

func buildContactRequest(t *testing.T, req validation.ContactRequest) *bytes.Buffer {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return bytes.NewBuffer(payload)
}

func TestContactHandlerRedactsLogsAndMetrics(t *testing.T) {
	logger, hook := test.NewNullLogger()
	metrics := &FormMetrics{
		ContactRequests: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "contact_requests_total", Help: "contact requests"},
			[]string{"status"},
		),
	}

	handler := NewContactHandler(&emailSenderStub{}, nil, "to@example.com", false, logger, metrics)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/contact", handler.Handle)

	req := validation.ContactRequest{
		Name:    "Jane Doe",
		Email:   "jane.doe@example.com",
		Message: "hi",
	}

	w := httptest.NewRecorder()
	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "/api/contact", buildContactRequest(t, req))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, httpReq)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
	if got := testutil.ToFloat64(metrics.ContactRequests.WithLabelValues("validation_failed")); got != 1.0 {
		t.Fatalf("expected validation_failed metric 1.0, got %f", got)
	}

	entries := hook.AllEntries()
	if len(entries) == 0 {
		t.Fatal("expected log entries")
	}

	var blockedEntry *logrus.Entry
	for _, entry := range entries {
		if entry.Message == "Blocked submission" {
			blockedEntry = entry
			break
		}
	}

	if blockedEntry == nil {
		t.Fatal("expected blocked submission log entry")
	}
	if blockedEntry.Data["email"] != "j***@example.com" {
		t.Fatalf("expected redacted email, got %v", blockedEntry.Data["email"])
	}
	if blockedEntry.Data["name"] != "J***" {
		t.Fatalf("expected redacted name, got %v", blockedEntry.Data["name"])
	}
}

func TestContactHandlerTurnstileErrorMapsToBadGateway(t *testing.T) {
	logger, _ := test.NewNullLogger()
	metrics := &FormMetrics{
		ContactRequests: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "contact_turnstile_total", Help: "contact requests"},
			[]string{"status"},
		),
	}

	handler := NewContactHandler(
		&emailSenderStub{},
		&fakeTurnstile{err: errors.New("turnstile down")},
		"to@example.com",
		true,
		logger,
		metrics,
	)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/contact", handler.Handle)

	req := validation.ContactRequest{
		Name:           "Jane Doe",
		Email:          "jane.doe@example.com",
		Message:        "Hello there world",
		TurnstileToken: "token",
	}

	w := httptest.NewRecorder()
	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "/api/contact", buildContactRequest(t, req))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, httpReq)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected status %d, got %d", http.StatusBadGateway, w.Code)
	}
	if got := testutil.ToFloat64(metrics.ContactRequests.WithLabelValues("turnstile_error")); got != 1.0 {
		t.Fatalf("expected turnstile_error metric 1.0, got %f", got)
	}
}

func TestContactHandlerEmailErrorMapsToBadGateway(t *testing.T) {
	logger, _ := test.NewNullLogger()
	metrics := &FormMetrics{
		ContactRequests: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "contact_email_total", Help: "contact requests"},
			[]string{"status"},
		),
	}

	handler := NewContactHandler(
		&emailSenderStub{err: errors.New("smtp down")},
		nil,
		"to@example.com",
		false,
		logger,
		metrics,
	)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/contact", handler.Handle)

	req := validation.ContactRequest{
		Name:        "Jane Doe",
		Email:       "jane.doe@example.com",
		Message:     "Hello there world",
		HumanCheck:  "human",
		PhoneNumber: "",
		Behavior: map[string]interface{}{
			"formShownAt": 0,
			"submittedAt": 5000,
			"mouse":       true,
			"typed":       false,
		},
	}

	w := httptest.NewRecorder()
	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "/api/contact", buildContactRequest(t, req))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, httpReq)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected status %d, got %d", http.StatusBadGateway, w.Code)
	}
	if got := testutil.ToFloat64(metrics.ContactRequests.WithLabelValues("email_error")); got != 1.0 {
		t.Fatalf("expected email_error metric 1.0, got %f", got)
	}
}
