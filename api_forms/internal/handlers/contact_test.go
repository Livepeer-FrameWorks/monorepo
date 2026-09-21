package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"frameworks/api_forms/internal/validation"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/serviceevents"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/turnstile"

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

type activityEmitterStub struct {
	events []string
	err    error
}

func (s *activityEmitterStub) EmitActivity(_ context.Context, eventType string) error {
	s.events = append(s.events, eventType)
	return s.err
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
	handler := NewContactHandler(sender, nil, "contact@example.com", "Contact Form", "Thank you!", config.EmailBranding{}, turnstileEnabled, logger, nil, nil)
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

func TestContactHandlerRejectsOversizedBody(t *testing.T) {
	harness := setupContactHandler(false)
	body := `{"name":"Jane Doe","email":"jane@example.com","message":"` + strings.Repeat("x", contactMaxBodyBytes) + `"}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/contact", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	harness.router.ServeHTTP(resp, req)

	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", resp.Code, resp.Body.String())
	}
	if len(harness.sender.calls) != 0 {
		t.Fatal("oversized request sent an email")
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

func TestContactHandlerEmitsOnlyAfterEmailDelivery(t *testing.T) {
	logger, _ := test.NewNullLogger()
	emitter := &activityEmitterStub{err: errors.New("decklog unavailable")}
	handler := NewContactHandler(&emailSenderStub{}, nil, "contact@example.com", "Contact Form", "Thank you!", config.EmailBranding{}, false, logger, nil, emitter)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/contact", handler.Handle)
	payload := map[string]any{
		"name": "Jane Doe", "email": "jane@example.com",
		"message": "Hello there, looking forward to learning more.", "human_check": "human",
		"behavior": map[string]any{
			"formShownAt": float64(time.Now().Add(-10 * time.Second).UnixMilli()),
			"submittedAt": float64(time.Now().UnixMilli()), "mouse": true, "typed": true,
		},
	}
	body, _ := json.Marshal(payload)
	request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/contact", bytes.NewBuffer(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if len(emitter.events) != 1 || emitter.events[0] != serviceevents.MarketingContactDelivered {
		t.Fatalf("activity events = %v", emitter.events)
	}

	failedHandler := NewContactHandler(&emailSenderStub{err: errors.New("smtp unavailable")}, nil, "contact@example.com", "Contact Form", "Thank you!", config.EmailBranding{}, false, logger, nil, emitter)
	failedRouter := gin.New()
	failedRouter.POST("/api/contact", failedHandler.Handle)
	request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/contact", bytes.NewBuffer(body))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	failedRouter.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway || len(emitter.events) != 1 {
		t.Fatalf("failed email status = %d, activity events = %v", response.Code, emitter.events)
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

	handler := NewContactHandler(&emailSenderStub{}, nil, "to@example.com", "Contact Form", "Thank you!", config.EmailBranding{}, false, logger, metrics, nil)
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
		"Contact Form",
		"Thank you!",
		config.EmailBranding{},
		true,
		logger,
		metrics,
		nil,
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

func TestGetRemoteIPCFHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c := gin.CreateTestContextOnly(w, gin.New())
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Request.Header.Set("CF-Connecting-IP", "1.2.3.4")

	got := getRemoteIP(c)
	if got != "1.2.3.4" {
		t.Fatalf("expected 1.2.3.4, got %s", got)
	}
}

func TestGetRemoteIPXForwardedFor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c := gin.CreateTestContextOnly(w, gin.New())
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Request.Header.Set("X-Forwarded-For", "5.6.7.8, 9.10.11.12")

	got := getRemoteIP(c)
	if got != "5.6.7.8" {
		t.Fatalf("expected 5.6.7.8, got %s", got)
	}
}

func TestGetRemoteIPFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c := gin.CreateTestContextOnly(w, gin.New())
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	got := getRemoteIP(c)
	expected := c.ClientIP()
	if got != expected {
		t.Fatalf("expected %s, got %s", expected, got)
	}
}

func TestBuildEmailHTMLWithCompany(t *testing.T) {
	html := buildEmailHTML("Jane", "jane@example.com", "ACME", "Hello", "1.2.3.4")
	if !strings.Contains(html, "ACME") {
		t.Fatalf("expected output to contain ACME, got %s", html)
	}
}

func TestBuildEmailHTMLWithoutCompany(t *testing.T) {
	html := buildEmailHTML("Jane", "jane@example.com", "", "Hello", "1.2.3.4")
	if !strings.Contains(html, "Not provided") {
		t.Fatalf("expected output to contain 'Not provided', got %s", html)
	}
}

func TestBuildEmailHTMLNewlines(t *testing.T) {
	html := buildEmailHTML("Jane", "jane@example.com", "ACME", "line1\nline2", "1.2.3.4")
	if !strings.Contains(html, "<br>") {
		t.Fatalf("expected output to contain <br>, got %s", html)
	}
}

func TestBuildEmailHTMLEscapesSubmittedContent(t *testing.T) {
	html := buildEmailHTML(`<script>alert("name")</script>`, "jane@example.com", "ACME", `<img src=x onerror=alert(1)>`, "1.2.3.4")
	if strings.Contains(html, `<script>`) || strings.Contains(html, `<img src=x`) {
		t.Fatalf("submitted content was not escaped: %s", html)
	}
	for _, want := range []string{"&lt;script&gt;", "&lt;img"} {
		if !strings.Contains(html, want) {
			t.Fatalf("escaped email missing %q", want)
		}
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
		"Contact Form",
		"Thank you!",
		config.EmailBranding{},
		false,
		logger,
		metrics,
		nil,
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
