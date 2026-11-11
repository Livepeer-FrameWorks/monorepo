package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNotificationMCPRejectsRevokedExistingSession(t *testing.T) {
	secret := []byte("skipper-session-test")
	token, err := auth.GenerateJWT("user", "tenant", "user@example.com", "member", secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"revoked", status.Error(codes.Unauthenticated, "session signed out"), http.StatusUnauthorized},
		{"unavailable", status.Error(codes.Unavailable, "unavailable"), http.StatusServiceUnavailable},
		{"active", nil, http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			handler := notificationSessionAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			}), secret, func(ctx context.Context, got string) error {
				if got != token {
					t.Fatal("state check received another token")
				}
				return tc.err
			})
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/mcp/notify", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Mcp-Session-Id", "already-connected")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tc.want || called != (tc.want == http.StatusNoContent) {
				t.Fatalf("status=%d want=%d handler called=%v", response.Code, tc.want, called)
			}
		})
	}
}
