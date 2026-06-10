package githubapp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	jwt "github.com/golang-jwt/jwt/v5"
)

func pkcs1PEM(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

func pkcs8PEM(t *testing.T, key any) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// ParsePrivateKey must accept both PKCS1 and PKCS8 RSA encodings (the GitHub App
// key can be downloaded in either form) and reject non-PEM / non-RSA inputs.
func TestParsePrivateKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	t.Run("pkcs1", func(t *testing.T) {
		got, err := ParsePrivateKey(pkcs1PEM(t, key))
		if err != nil {
			t.Fatalf("pkcs1: %v", err)
		}
		if !got.Equal(key) {
			t.Fatalf("pkcs1 round-trip mismatch")
		}
	})

	t.Run("pkcs8", func(t *testing.T) {
		got, err := ParsePrivateKey(pkcs8PEM(t, key))
		if err != nil {
			t.Fatalf("pkcs8: %v", err)
		}
		if !got.Equal(key) {
			t.Fatalf("pkcs8 round-trip mismatch")
		}
	})

	t.Run("not pem", func(t *testing.T) {
		if _, err := ParsePrivateKey([]byte("definitely not a pem block")); err == nil {
			t.Fatal("expected error for non-PEM input")
		}
	})

	t.Run("pem but garbage bytes", func(t *testing.T) {
		bad := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("garbage")})
		if _, err := ParsePrivateKey(bad); err == nil {
			t.Fatal("expected error for undecodable key bytes")
		}
	})

	t.Run("non-rsa key", func(t *testing.T) {
		ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("generate ec key: %v", err)
		}
		if _, err := ParsePrivateKey(pkcs8PEM(t, ec)); err == nil {
			t.Fatal("expected error for non-RSA key")
		}
	})
}

// MintJWT must produce an RS256 token that verifies against the public key and
// carries the app ID as issuer.
func TestMintJWT(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	const appID int64 = 12345
	tokenStr, err := MintJWT(appID, key)
	if err != nil {
		t.Fatalf("MintJWT: %v", err)
	}

	parsed, err := jwt.Parse(tokenStr, func(tok *jwt.Token) (any, error) {
		if _, ok := tok.Method.(*jwt.SigningMethodRSA); !ok {
			t.Fatalf("unexpected signing method: %v", tok.Header["alg"])
		}
		return &key.PublicKey, nil
	})
	if err != nil || !parsed.Valid {
		t.Fatalf("token did not verify: err=%v valid=%v", err, parsed.Valid)
	}

	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("unexpected claims type %T", parsed.Claims)
	}
	// iss survives the JSON round-trip as a float64.
	if iss, _ := claims["iss"].(float64); int64(iss) != appID {
		t.Fatalf("iss = %v, want %d", claims["iss"], appID)
	}
}
