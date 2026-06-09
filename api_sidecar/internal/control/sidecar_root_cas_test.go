package control

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTestCAPEM emits a self-signed CA certificate to a temp file and returns
// its path.
func writeTestCAPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Unix(0, 0),
		NotAfter:              time.Unix(1<<31, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	path := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(path, pemBytes, 0o644); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	return path
}

// loadSidecarRootCAs builds the gRPC trust anchor. An empty path uses the system
// pool; a valid PEM is appended; a missing file or invalid PEM is a hard error
// (failing closed rather than trusting nothing or everything).
func TestLoadSidecarRootCAs(t *testing.T) {
	t.Run("empty path returns a usable system pool", func(t *testing.T) {
		pool, err := loadSidecarRootCAs("")
		if err != nil {
			t.Fatalf("empty path: %v", err)
		}
		if pool == nil {
			t.Fatal("pool must be non-nil")
		}
	})

	t.Run("whitespace path is treated as empty", func(t *testing.T) {
		pool, err := loadSidecarRootCAs("   ")
		if err != nil || pool == nil {
			t.Fatalf("whitespace path: pool=%v err=%v", pool, err)
		}
	})

	t.Run("valid CA file is appended", func(t *testing.T) {
		pool, err := loadSidecarRootCAs(writeTestCAPEM(t))
		if err != nil {
			t.Fatalf("valid CA: %v", err)
		}
		if pool == nil {
			t.Fatal("pool must be non-nil")
		}
	})

	t.Run("missing file errors", func(t *testing.T) {
		if _, err := loadSidecarRootCAs(filepath.Join(t.TempDir(), "absent.crt")); err == nil {
			t.Fatal("missing CA file must error")
		}
	})

	t.Run("invalid PEM errors", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), "bad.crt")
		if err := os.WriteFile(bad, []byte("not a pem"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadSidecarRootCAs(bad); err == nil {
			t.Fatal("invalid PEM must error")
		}
	})
}
