package control

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestParseEdgeTelemetryPrivateKeyAcceptsProvisionedECKey(t *testing.T) {
	key := mustGenerateTelemetryTestKey(t)
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey failed: %v", err)
	}

	t.Setenv("EDGE_TELEMETRY_JWT_PRIVATE_KEY_PEM_B64", encodeTelemetryTestPEM("EC PRIVATE KEY", der))

	got, err := parseEdgeTelemetryPrivateKey()
	if err != nil {
		t.Fatalf("parseEdgeTelemetryPrivateKey failed: %v", err)
	}
	if !sameTelemetryTestPublicKey(t, got, key) {
		t.Fatal("parsed key does not match input key")
	}
}

func TestParseEdgeTelemetryPrivateKeyAcceptsPKCS8Key(t *testing.T) {
	key := mustGenerateTelemetryTestKey(t)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey failed: %v", err)
	}

	t.Setenv("EDGE_TELEMETRY_JWT_PRIVATE_KEY_PEM_B64", encodeTelemetryTestPEM("PRIVATE KEY", der))

	got, err := parseEdgeTelemetryPrivateKey()
	if err != nil {
		t.Fatalf("parseEdgeTelemetryPrivateKey failed: %v", err)
	}
	if !sameTelemetryTestPublicKey(t, got, key) {
		t.Fatal("parsed key does not match input key")
	}
}

func TestParseEdgeTelemetryPrivateKeyAcceptsMislabeledECKey(t *testing.T) {
	key := mustGenerateTelemetryTestKey(t)
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey failed: %v", err)
	}

	t.Setenv("EDGE_TELEMETRY_JWT_PRIVATE_KEY_PEM_B64", encodeTelemetryTestPEM("PRIVATE KEY", der))

	got, err := parseEdgeTelemetryPrivateKey()
	if err != nil {
		t.Fatalf("parseEdgeTelemetryPrivateKey failed: %v", err)
	}
	if !sameTelemetryTestPublicKey(t, got, key) {
		t.Fatal("parsed key does not match input key")
	}
}

func TestParseEdgeTelemetryPrivateKeyRequiresEnv(t *testing.T) {
	t.Setenv("EDGE_TELEMETRY_JWT_PRIVATE_KEY_PEM_B64", "")
	if _, err := parseEdgeTelemetryPrivateKey(); err == nil {
		t.Fatal("expected missing telemetry private key env to fail")
	}
}

func TestParseEdgeTelemetryPrivateKeyRejectsInvalidPEM(t *testing.T) {
	t.Setenv("EDGE_TELEMETRY_JWT_PRIVATE_KEY_PEM_B64", base64.StdEncoding.EncodeToString([]byte("not pem")))
	if _, err := parseEdgeTelemetryPrivateKey(); err == nil {
		t.Fatal("expected invalid telemetry private key PEM to fail")
	}
}

func TestMintEdgeTelemetryTokenUsesVMAuthLabelClaimShape(t *testing.T) {
	key := mustGenerateTelemetryTestKey(t)
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey failed: %v", err)
	}
	t.Setenv("EDGE_TELEMETRY_JWT_PRIVATE_KEY_PEM_B64", encodeTelemetryTestPEM("EC PRIVATE KEY", der))

	tokenString, _, err := mintEdgeTelemetryToken("edge-1", "cluster-1", "tenant-1")
	if err != nil {
		t.Fatalf("mintEdgeTelemetryToken failed: %v", err)
	}
	claims := &edgeTelemetryClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(*jwt.Token) (any, error) {
		return &key.PublicKey, nil
	})
	if err != nil {
		t.Fatalf("ParseWithClaims failed: %v", err)
	}
	if !token.Valid {
		t.Fatal("token should be valid")
	}
	if got := claims.Role; got != "edge_metrics_write" {
		t.Fatalf("role = %q, want edge_metrics_write", got)
	}
	if got := claims.VMAccess.MetricsExtraLabels; len(got) != 1 || got[0] != "frameworks_node=edge-1" {
		t.Fatalf("metrics_extra_labels = %#v, want frameworks_node=edge-1 array", got)
	}
}

func mustGenerateTelemetryTestKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	return key
}

func encodeTelemetryTestPEM(blockType string, der []byte) string {
	return base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}))
}

func sameTelemetryTestPublicKey(t *testing.T, got, want *ecdsa.PrivateKey) bool {
	t.Helper()
	gotDER, err := x509.MarshalPKIXPublicKey(&got.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey(got) failed: %v", err)
	}
	wantDER, err := x509.MarshalPKIXPublicKey(&want.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey(want) failed: %v", err)
	}
	return bytes.Equal(gotDER, wantDER)
}
