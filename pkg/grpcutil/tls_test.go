package grpcutil

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

func TestBuildServerTLSConfig(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeSelfSignedPair(t, dir, "server.local")

	cfg, err := buildServerTLSConfig(ServerTLSConfig{
		CertFile: certFile,
		KeyFile:  keyFile,
	})
	if err != nil {
		t.Fatalf("buildServerTLSConfig returned error: %v", err)
	}
	if cfg == nil {
		t.Fatal("buildServerTLSConfig returned nil config")
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("expected 1 certificate, got %d", len(cfg.Certificates))
	}
}

func TestBuildServerTLSConfigRequiresPair(t *testing.T) {
	_, err := buildServerTLSConfig(ServerTLSConfig{CertFile: "cert.pem"})
	if err == nil {
		t.Fatal("expected error for missing key file")
	}
}

func TestBuildServerTLSConfigAllowsExplicitInsecure(t *testing.T) {
	cfg, err := buildServerTLSConfig(ServerTLSConfig{AllowInsecure: true})
	if err != nil {
		t.Fatalf("buildServerTLSConfig returned error: %v", err)
	}
	if cfg != nil {
		t.Fatal("expected nil tls config for insecure mode")
	}
}

func TestBuildServerTLSConfigRejectsInsecureInProduction(t *testing.T) {
	t.Setenv("BUILD_ENV", "production")

	_, err := buildServerTLSConfig(ServerTLSConfig{AllowInsecure: true})
	if err == nil {
		t.Fatal("expected production insecure server config to fail")
	}
}

func TestBuildClientTLSConfigInsecure(t *testing.T) {
	cfg, insecureAllowed, err := buildClientTLSConfig(ClientTLSConfig{AllowInsecure: true})
	if err != nil {
		t.Fatalf("buildClientTLSConfig returned error: %v", err)
	}
	if !insecureAllowed {
		t.Fatal("expected insecureAllowed=true")
	}
	if cfg != nil {
		t.Fatal("expected nil tls config in insecure mode")
	}
}

func TestBuildClientTLSConfigRejectsInsecureInProduction(t *testing.T) {
	t.Setenv("BUILD_ENV", "production")

	_, insecureAllowed, err := buildClientTLSConfig(ClientTLSConfig{AllowInsecure: true})
	if err == nil {
		t.Fatal("expected production insecure client config to fail")
	}
	if insecureAllowed {
		t.Fatal("expected insecureAllowed=false on production error")
	}
}

func TestBuildClientTLSConfigWithCAFile(t *testing.T) {
	dir := t.TempDir()
	certFile, _ := writeSelfSignedPair(t, dir, "server.local")

	cfg, insecureAllowed, err := buildClientTLSConfig(ClientTLSConfig{
		CACertFile: certFile,
		ServerName: "server.local",
	})
	if err != nil {
		t.Fatalf("buildClientTLSConfig returned error: %v", err)
	}
	if insecureAllowed {
		t.Fatal("expected TLS mode, got insecure")
	}
	if cfg == nil {
		t.Fatal("expected tls config")
	}
	if cfg.ServerName != "server.local" {
		t.Fatalf("expected ServerName %q, got %q", "server.local", cfg.ServerName)
	}
}

func TestBuildClientTLSConfigWithInlineCAPEM(t *testing.T) {
	dir := t.TempDir()
	certFile, _ := writeSelfSignedPair(t, dir, "server.local")
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	cfg, insecureAllowed, err := buildClientTLSConfig(ClientTLSConfig{
		CACertPEM:  string(certPEM),
		ServerName: "server.local",
	})
	if err != nil {
		t.Fatalf("buildClientTLSConfig returned error: %v", err)
	}
	if insecureAllowed {
		t.Fatal("expected TLS mode, got insecure")
	}
	if cfg == nil || cfg.RootCAs == nil {
		t.Fatal("expected tls config with root CAs")
	}
	if cfg.ServerName != "server.local" {
		t.Fatalf("expected ServerName %q, got %q", "server.local", cfg.ServerName)
	}
}

func TestBuildClientTLSConfigRejectsMissingCAFile(t *testing.T) {
	_, _, err := buildClientTLSConfig(ClientTLSConfig{CACertFile: filepath.Join(t.TempDir(), "missing.pem")})
	if err == nil {
		t.Fatal("expected error for missing CA file")
	}
}

func TestBuildClientTLSConfigSystemPoolFallback(t *testing.T) {
	cfg, insecureAllowed, err := buildClientTLSConfig(ClientTLSConfig{ServerName: "example.com"})
	if err != nil {
		t.Fatalf("buildClientTLSConfig returned error: %v", err)
	}
	if insecureAllowed {
		t.Fatal("expected TLS mode")
	}
	if cfg == nil || cfg.RootCAs == nil {
		t.Fatal("expected system root CAs")
	}
	if cfg.ServerName != "example.com" {
		t.Fatalf("expected ServerName %q, got %q", "example.com", cfg.ServerName)
	}
}

func writeSelfSignedPair(t *testing.T, dir, commonName string) (string, string) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: commonName,
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		DNSNames:              []string{commonName},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}

	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("WriteFile(cert): %v", err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("WriteFile(key): %v", err)
	}

	return certFile, keyFile
}
