package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"frameworks/pkg/logging"

	"github.com/gin-gonic/gin"
)

func TestAuthHandlers_InvalidJSONBindingsReturnBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &AuthHandlers{
		logger: logging.NewLogger(),
	}

	tests := []struct {
		name    string
		handler gin.HandlerFunc
	}{
		{name: "login", handler: h.Login()},
		{name: "wallet login", handler: h.WalletLogin()},
		{name: "register", handler: h.Register()},
		{name: "resend verification", handler: h.ResendVerification()},
		{name: "forgot password", handler: h.ForgotPassword()},
		{name: "reset password", handler: h.ResetPassword()},
		{name: "update me", handler: h.UpdateMe()},
		{name: "update newsletter", handler: h.UpdateNewsletter()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"broken_json":`))
			c.Request.Header.Set("Content-Type", "application/json")

			tc.handler(c)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if !strings.Contains(rec.Body.String(), "invalid request") {
				t.Fatalf("body: expected invalid request error, got %q", rec.Body.String())
			}
		})
	}
}
