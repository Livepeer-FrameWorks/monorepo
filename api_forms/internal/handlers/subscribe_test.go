package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"frameworks/pkg/clients/listmonk"
	"frameworks/pkg/logging"

	"github.com/gin-gonic/gin"
)

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
	handler := NewSubscribeHandler(stub, nil, 99, false, logging.NewLogger())
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
