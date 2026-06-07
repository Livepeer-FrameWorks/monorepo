package skipper

import (
	"context"
	"testing"
)

// TestResolveAuthType pins the precedence: an explicit auth type wins, a bare
// JWT implies "jwt", and an empty context resolves to "unknown" (never empty).
func TestResolveAuthType(t *testing.T) {
	t.Run("explicit_auth_type_wins", func(t *testing.T) {
		ctx := WithAuthType(WithJWTToken(context.Background(), "tok"), "apikey")
		if got := resolveAuthType(ctx); got != "apikey" {
			t.Fatalf("got %q, want apikey", got)
		}
	})

	t.Run("jwt_token_implies_jwt", func(t *testing.T) {
		ctx := WithJWTToken(context.Background(), "tok")
		if got := resolveAuthType(ctx); got != "jwt" {
			t.Fatalf("got %q, want jwt", got)
		}
	})

	t.Run("empty_context_is_unknown", func(t *testing.T) {
		if got := resolveAuthType(context.Background()); got != "unknown" {
			t.Fatalf("got %q, want unknown", got)
		}
	})
}
