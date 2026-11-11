package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func notificationSessionAuth(next http.Handler, secret []byte, check func(context.Context, string) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			http.Error(w, "bearer authorization required", http.StatusUnauthorized)
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		claims, err := auth.ValidateRevocableSessionJWT(token, secret, nil)
		if err != nil || claims.TenantID == "" {
			http.Error(w, "invalid session", http.StatusUnauthorized)
			return
		}
		if check == nil {
			http.Error(w, "session state unavailable", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		err = check(ctx, token)
		cancel()
		if err != nil {
			if status.Code(err) == codes.Unauthenticated {
				http.Error(w, "session signed out", http.StatusUnauthorized)
			} else {
				http.Error(w, "session state unavailable", http.StatusServiceUnavailable)
			}
			return
		}
		ctx = context.WithValue(r.Context(), ctxkeys.KeyTenantID, claims.TenantID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
