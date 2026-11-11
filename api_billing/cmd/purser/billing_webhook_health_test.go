package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"frameworks/api_billing/internal/stripe"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func serveBillingWebhookHealth(t *testing.T, check func(context.Context) error) (*httptest.ResponseRecorder, *logtest.Hook) {
	t.Helper()
	logger, hook := logtest.NewNullLogger()
	router := gin.New()
	router.GET("/health/billing-webhooks", billingWebhookHealth(check, logger))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health/billing-webhooks", nil))
	return response, hook
}

func TestBillingWebhookHealthReportsReadOnlyConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		check  func(context.Context) error
		status int
	}{
		{"unconfigured", nil, http.StatusOK},
		{"configured", func(context.Context) error { return nil }, http.StatusOK},
		{"untyped_failure", func(context.Context) error { return errors.New("secret sk_live_private: missing invoice.finalized") }, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, hook := serveBillingWebhookHealth(t, tc.check)
			if response.Code != tc.status || !strings.Contains(response.Body.String(), `"read_only":true`) {
				t.Fatalf("configuration diagnostic: status=%d body=%s", response.Code, response.Body.String())
			}
			for _, entry := range hook.AllEntries() {
				if s, err := entry.String(); err == nil && strings.Contains(s, "sk_live_private") {
					t.Fatalf("provider error leaked credentials to the log: %s", s)
				}
			}
			if strings.Contains(response.Body.String(), "sk_live_private") {
				t.Fatal("provider error leaked credentials")
			}
		})
	}
}

func TestBillingWebhookHealthReturnsAndLogsTypedReason(t *testing.T) {
	reasonErr := &stripe.BillingWebhookError{
		Reason:        stripe.BillingWebhookMissingEvents,
		Message:       "Stripe webhook endpoint https://gateway.example/webhooks/billing/stripe is missing required events: charge.updated",
		ExpectedURL:   "https://gateway.example/webhooks/billing/stripe",
		MissingEvents: []string{"charge.updated"},
	}
	response, hook := serveBillingWebhookHealth(t, func(context.Context) error { return reasonErr })
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Reason        string   `json:"reason"`
		Message       string   `json:"message"`
		MissingEvents []string `json:"missing_events"`
		ExpectedURL   string   `json:"expected_url"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Reason != string(stripe.BillingWebhookMissingEvents) || len(body.MissingEvents) != 1 || body.MissingEvents[0] != "charge.updated" ||
		!strings.Contains(body.Message, "charge.updated") || body.ExpectedURL != reasonErr.ExpectedURL {
		t.Fatalf("health body does not carry the failure reason: %s", response.Body.String())
	}
	warnings := 0
	for _, entry := range hook.AllEntries() {
		if entry.Level == logrus.WarnLevel {
			warnings++
			if !strings.Contains(entry.Message+" "+toString(entry.Data["reason"])+" "+toString(entry.Data["missing_events"]), "charge.updated") {
				t.Fatalf("warn log omits the missing event: %+v", entry)
			}
		}
	}
	if warnings != 1 {
		t.Fatalf("want exactly one warn log for the failed check, got %d", warnings)
	}
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []string:
		return strings.Join(x, ",")
	default:
		return ""
	}
}
