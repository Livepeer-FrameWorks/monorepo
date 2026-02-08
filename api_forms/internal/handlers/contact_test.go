package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"frameworks/pkg/logging"

	"github.com/gin-gonic/gin"
)

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

type contactHandlerHarness struct {
	router *gin.Engine
	sender *emailSenderStub
}

func setupContactHandler(turnstileEnabled bool) *contactHandlerHarness {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	sender := &emailSenderStub{}
	handler := NewContactHandler(sender, nil, "contact@example.com", turnstileEnabled, logging.NewLogger())
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
