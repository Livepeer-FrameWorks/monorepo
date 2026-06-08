package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TestViewerJWTKid pins the kid-extraction contract used to pick a signing key
// before verification: a structurally valid JWS returns its header kid, an
// opaque/non-JWS token returns ErrTokenNotJWS, and a JWS whose header has no
// (or an empty) kid returns ErrMissingKid.
func TestViewerJWTKid(t *testing.T) {
	priv, _, _ := mustGenKey(t)

	t.Run("valid JWS returns kid", func(t *testing.T) {
		tok := mintES256(t, priv, "kid-7", jwt.MapClaims{"exp": time.Now().Add(time.Minute).Unix()})
		got, err := ViewerJWTKid(tok)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "kid-7" {
			t.Fatalf("kid = %q, want kid-7", got)
		}
	})

	t.Run("opaque token is not a JWS", func(t *testing.T) {
		_, err := ViewerJWTKid("opaque-mist-session-token")
		if !errors.Is(err, ErrTokenNotJWS) {
			t.Fatalf("err = %v, want ErrTokenNotJWS", err)
		}
	})

	t.Run("empty kid header is missing kid", func(t *testing.T) {
		tok := mintES256(t, priv, "", jwt.MapClaims{"exp": time.Now().Add(time.Minute).Unix()})
		_, err := ViewerJWTKid(tok)
		if !errors.Is(err, ErrMissingKid) {
			t.Fatalf("err = %v, want ErrMissingKid", err)
		}
	})
}

// TestViewerJWTClaimsUnverified pins the security-critical contract that this
// diagnostic helper decodes the payload WITHOUT verifying the signature. It is
// used only to surface "why was this token rejected" alongside a deny, so it
// must (a) return claims from a structurally valid JWS even when the signature
// is bogus, and (b) still reject anything that isn't a JWS. The bogus-signature
// case is the important one: it proves no caller can mistake this for an auth
// path.
func TestViewerJWTClaimsUnverified(t *testing.T) {
	priv, _, _ := mustGenKey(t)

	t.Run("decodes claims from a JWS with a tampered signature", func(t *testing.T) {
		tok := mintES256(t, priv, "kid-1", jwt.MapClaims{
			"sub": "viewer-9",
			"exp": time.Now().Add(time.Minute).Unix(),
		})
		// Replace the signature segment with garbage: verification would fail,
		// but the unverified decode must still surface the payload claims.
		parts := strings.Split(tok, ".")
		if len(parts) != 3 {
			t.Fatalf("minted token is not 3 segments: %q", tok)
		}
		tampered := parts[0] + "." + parts[1] + ".AAAAdGFtcGVyZWQ"

		claims, err := ViewerJWTClaimsUnverified(tampered)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if claims["sub"] != "viewer-9" {
			t.Fatalf("sub = %v, want viewer-9 (claims must decode without verifying)", claims["sub"])
		}
	})

	t.Run("opaque token is not a JWS", func(t *testing.T) {
		_, err := ViewerJWTClaimsUnverified("not.a-jws")
		if !errors.Is(err, ErrTokenNotJWS) {
			t.Fatalf("err = %v, want ErrTokenNotJWS", err)
		}
	})
}
