package telemetrytoken

import (
	"testing"
	"time"
)

func TestSignVerifyRoundtrip(t *testing.T) {
	secret := []byte("platform-telemetry-secret")
	now := time.Unix(1_700_000_000, 0)
	in := Claims{ContentID: "demo", NodeID: "edge-1", ServingClusterID: "cluster-eu", OriginClusterID: "cluster-us"}

	tok, err := Sign(secret, in, 5*time.Minute, now)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	out, err := Verify(secret, tok, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if out.ContentID != "demo" || out.NodeID != "edge-1" || out.ServingClusterID != "cluster-eu" || out.OriginClusterID != "cluster-us" {
		t.Fatalf("claims roundtrip mismatch: %+v", out)
	}
	if out.ExpUnix != now.Add(5*time.Minute).Unix() {
		t.Fatalf("exp not stamped: %d", out.ExpUnix)
	}
}

func TestVerifyRejectsTamper(t *testing.T) {
	secret := []byte("s")
	now := time.Unix(1_700_000_000, 0)
	tok, _ := Sign(secret, Claims{ContentID: "demo", NodeID: "n1"}, time.Minute, now)

	// Flip a payload byte.
	b := []byte(tok)
	idx := len("v1.") + 2
	b[idx] = b[idx] ^ 0x01
	if _, err := Verify(secret, string(b), now); err == nil {
		t.Fatal("expected tampered token to fail verification")
	}
	// Wrong secret.
	if _, err := Verify([]byte("other"), tok, now); err != ErrBadSignature {
		t.Fatalf("expected ErrBadSignature, got %v", err)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	secret := []byte("s")
	now := time.Unix(1_700_000_000, 0)
	tok, _ := Sign(secret, Claims{ContentID: "demo"}, time.Minute, now)
	if _, err := Verify(secret, tok, now.Add(2*time.Minute)); err != ErrExpired {
		t.Fatalf("expected ErrExpired, got %v", err)
	}
}

func TestEmptySecret(t *testing.T) {
	if _, err := Sign(nil, Claims{}, time.Minute, time.Unix(0, 0)); err != ErrEmptySecret {
		t.Fatalf("expected ErrEmptySecret on sign, got %v", err)
	}
	if _, err := Verify(nil, "v1.a.b", time.Unix(0, 0)); err != ErrEmptySecret {
		t.Fatalf("expected ErrEmptySecret on verify, got %v", err)
	}
}
