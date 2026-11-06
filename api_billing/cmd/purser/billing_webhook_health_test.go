package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestBillingWebhookHealthReportsReadOnlyConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		check  func(context.Context) error
		status int
	}{
		{"unconfigured", nil, http.StatusOK},
		{"configured", func(context.Context) error { return nil }, http.StatusOK},
		{"missing_event", func(context.Context) error { return errors.New("secret sk_live_private: missing invoice.finalized") }, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := gin.New()
			router.GET("/health/billing-webhooks", billingWebhookHealth(tc.check))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health/billing-webhooks", nil))
			if response.Code != tc.status || !strings.Contains(response.Body.String(), `"read_only":true`) {
				t.Fatalf("configuration diagnostic: status=%d body=%s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "sk_live_private") {
				t.Fatal("provider error leaked credentials")
			}
		})
	}
}
