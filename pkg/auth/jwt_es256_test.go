package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func mustGenKey(t *testing.T) (*ecdsa.PrivateKey, string, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal pub: %v", err)
	}
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	return priv, pubPEM, "kid_test"
}

func mintES256(t *testing.T, priv *ecdsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

func TestVerifyViewerJWT_HappyPath(t *testing.T) {
	priv, pub, kid := mustGenKey(t)
	tok := mintES256(t, priv, kid, jwt.MapClaims{
		"sub": "viewer1",
		"exp": time.Now().Add(5 * time.Minute).Unix(),
		"iat": time.Now().Unix(),
	})

	claims, err := VerifyViewerJWT(tok, []SigningKey{{Kid: kid, PublicKeyPEM: pub}}, VerifyOptions{})
	if err != nil {
		t.Fatalf("expected verify ok, got %v", err)
	}
	if claims["sub"] != "viewer1" {
		t.Fatalf("missing sub claim: %v", claims)
	}
}

func TestVerifyViewerJWT_NotJWS(t *testing.T) {
	cases := []string{
		"",
		"not-a-jwt",
		"random-mist-tkn-abc123",
		"a.b", // only two segments
		"...", // empty segments
	}
	for _, c := range cases {
		_, err := VerifyViewerJWT(c, nil, VerifyOptions{})
		if !errors.Is(err, ErrTokenNotJWS) {
			t.Errorf("input %q: want ErrTokenNotJWS, got %v", c, err)
		}
	}
}

func TestVerifyViewerJWT_MissingKid(t *testing.T) {
	priv, pub, _ := mustGenKey(t)
	// mint without kid header
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	})
	signed, _ := tok.SignedString(priv)
	_, err := VerifyViewerJWT(signed, []SigningKey{{Kid: "kid1", PublicKeyPEM: pub}}, VerifyOptions{})
	if !errors.Is(err, ErrMissingKid) {
		t.Errorf("want ErrMissingKid, got %v", err)
	}
}

func TestVerifyViewerJWT_UnknownKid(t *testing.T) {
	priv, pub, _ := mustGenKey(t)
	tok := mintES256(t, priv, "unknown-kid", jwt.MapClaims{
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	})
	_, err := VerifyViewerJWT(tok, []SigningKey{{Kid: "different-kid", PublicKeyPEM: pub}}, VerifyOptions{})
	if !errors.Is(err, ErrUnknownKid) {
		t.Errorf("want ErrUnknownKid, got %v", err)
	}
}

func TestVerifyViewerJWT_KidNotInAllowedSet(t *testing.T) {
	priv, pub, kid := mustGenKey(t)
	tok := mintES256(t, priv, kid, jwt.MapClaims{
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	})
	_, err := VerifyViewerJWT(tok, []SigningKey{{Kid: kid, PublicKeyPEM: pub}}, VerifyOptions{
		AllowedKids: []string{"some-other-kid"},
	})
	if !errors.Is(err, ErrUnknownKid) {
		t.Errorf("want ErrUnknownKid, got %v", err)
	}
}

func TestVerifyViewerJWT_WrongAlgorithm(t *testing.T) {
	// HS256 token must be rejected even if a key happens to verify it.
	hsTok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	})
	hsTok.Header["kid"] = "kid1"
	signed, err := hsTok.SignedString([]byte("shared-secret"))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	_, _, _ = mustGenKey(t)
	_, vpub, _ := mustGenKey(t)
	_, err = VerifyViewerJWT(signed, []SigningKey{{Kid: "kid1", PublicKeyPEM: vpub}}, VerifyOptions{})
	if err == nil {
		t.Fatalf("expected wrong-alg / signature failure, got nil")
	}
}

func TestVerifyViewerJWT_AlgNone(t *testing.T) {
	// alg: none must never verify.
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	})
	tok.Header["kid"] = "kid1"
	signed, _ := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	_, _, _ = mustGenKey(t)
	_, pub, _ := mustGenKey(t)
	_, err := VerifyViewerJWT(signed, []SigningKey{{Kid: "kid1", PublicKeyPEM: pub}}, VerifyOptions{})
	if err == nil {
		t.Fatalf("expected alg=none rejection, got nil")
	}
}

func TestVerifyViewerJWT_BadSignature(t *testing.T) {
	priv1, _, kid := mustGenKey(t)
	_, otherPub, _ := mustGenKey(t)
	tok := mintES256(t, priv1, kid, jwt.MapClaims{
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	})
	_, err := VerifyViewerJWT(tok, []SigningKey{{Kid: kid, PublicKeyPEM: otherPub}}, VerifyOptions{})
	if !errors.Is(err, ErrSignatureFailed) {
		t.Errorf("want ErrSignatureFailed, got %v", err)
	}
}

func TestVerifyViewerJWT_Expired(t *testing.T) {
	priv, pub, kid := mustGenKey(t)
	tok := mintES256(t, priv, kid, jwt.MapClaims{
		"exp": time.Now().Add(-1 * time.Hour).Unix(),
		"iat": time.Now().Add(-2 * time.Hour).Unix(),
	})
	_, err := VerifyViewerJWT(tok, []SigningKey{{Kid: kid, PublicKeyPEM: pub}}, VerifyOptions{})
	if !errors.Is(err, ErrTokenExpired) {
		t.Errorf("want ErrTokenExpired, got %v", err)
	}
}

func TestVerifyViewerJWT_MissingExpiration(t *testing.T) {
	priv, pub, kid := mustGenKey(t)
	tok := mintES256(t, priv, kid, jwt.MapClaims{
		"sub": "viewer1",
		"iat": time.Now().Unix(),
	})
	_, err := VerifyViewerJWT(tok, []SigningKey{{Kid: kid, PublicKeyPEM: pub}}, VerifyOptions{})
	if !errors.Is(err, ErrMissingExpiration) {
		t.Errorf("want missing exp to fail verification, got %v", err)
	}
}

func TestVerifyViewerJWT_NotYetValid(t *testing.T) {
	priv, pub, kid := mustGenKey(t)
	tok := mintES256(t, priv, kid, jwt.MapClaims{
		"exp": time.Now().Add(2 * time.Hour).Unix(),
		"nbf": time.Now().Add(1 * time.Hour).Unix(),
	})
	_, err := VerifyViewerJWT(tok, []SigningKey{{Kid: kid, PublicKeyPEM: pub}}, VerifyOptions{})
	if !errors.Is(err, ErrTokenNotYetValid) {
		t.Errorf("want ErrTokenNotYetValid, got %v", err)
	}
}

func TestVerifyViewerJWT_AudienceMatch(t *testing.T) {
	priv, pub, kid := mustGenKey(t)
	tok := mintES256(t, priv, kid, jwt.MapClaims{
		"exp": time.Now().Add(5 * time.Minute).Unix(),
		"aud": []string{"viewer", "embed"},
	})
	if _, err := VerifyViewerJWT(tok, []SigningKey{{Kid: kid, PublicKeyPEM: pub}}, VerifyOptions{
		RequiredAudience: []string{"viewer"},
	}); err != nil {
		t.Errorf("want ok, got %v", err)
	}
}

func TestVerifyViewerJWT_AudienceMismatch(t *testing.T) {
	priv, pub, kid := mustGenKey(t)
	tok := mintES256(t, priv, kid, jwt.MapClaims{
		"exp": time.Now().Add(5 * time.Minute).Unix(),
		"aud": "different-audience",
	})
	_, err := VerifyViewerJWT(tok, []SigningKey{{Kid: kid, PublicKeyPEM: pub}}, VerifyOptions{
		RequiredAudience: []string{"viewer"},
	})
	if !errors.Is(err, ErrAudienceMismatch) {
		t.Errorf("want ErrAudienceMismatch, got %v", err)
	}
}

func TestVerifyViewerJWT_RequiredClaimMatch(t *testing.T) {
	priv, pub, kid := mustGenKey(t)
	tok := mintES256(t, priv, kid, jwt.MapClaims{
		"exp":  time.Now().Add(5 * time.Minute).Unix(),
		"tier": "pro",
	})
	if _, err := VerifyViewerJWT(tok, []SigningKey{{Kid: kid, PublicKeyPEM: pub}}, VerifyOptions{
		RequiredClaims: map[string]string{"tier": `"pro"`},
	}); err != nil {
		t.Errorf("want ok, got %v", err)
	}
}

func TestVerifyViewerJWT_RequiredClaimMissing(t *testing.T) {
	priv, pub, kid := mustGenKey(t)
	tok := mintES256(t, priv, kid, jwt.MapClaims{
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	})
	_, err := VerifyViewerJWT(tok, []SigningKey{{Kid: kid, PublicKeyPEM: pub}}, VerifyOptions{
		RequiredClaims: map[string]string{"tier": `"pro"`},
	})
	if !errors.Is(err, ErrRequiredClaimMiss) {
		t.Errorf("want ErrRequiredClaimMiss, got %v", err)
	}
}

func TestGenerateES256Keypair_RoundTrip(t *testing.T) {
	privPEM, pubPEM, kid, err := GenerateES256Keypair()
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	if kid == "" {
		t.Fatal("empty kid")
	}

	block, _ := pem.Decode([]byte(privPEM))
	if block == nil {
		t.Fatal("private PEM did not decode")
	}
	priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse priv: %v", err)
	}
	ecPriv, ok := priv.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatal("private key is not ECDSA")
	}

	tok := mintES256(t, ecPriv, kid, jwt.MapClaims{"exp": time.Now().Add(time.Minute).Unix()})
	if _, err := VerifyViewerJWT(tok, []SigningKey{{Kid: kid, PublicKeyPEM: pubPEM}}, VerifyOptions{}); err != nil {
		t.Errorf("round-trip verify failed: %v", err)
	}
}
