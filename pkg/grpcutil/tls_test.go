package grpcutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
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
	if cfg.GetCertificate == nil {
		t.Fatal("expected dynamic certificate loader")
	}
	if len(cfg.Certificates) != 0 {
		t.Fatalf("expected no static certificates, got %d", len(cfg.Certificates))
	}
	cert, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate returned error: %v", err)
	}
	if got := certificateCommonName(t, cert); got != "server.local" {
		t.Fatalf("certificate common name = %q, want server.local", got)
	}
}

func TestBuildServerTLSConfigReloadsCertificateWhenFilesChange(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeSelfSignedPair(t, dir, "server-v1.local")

	cfg, err := buildServerTLSConfig(ServerTLSConfig{
		CertFile: certFile,
		KeyFile:  keyFile,
	})
	if err != nil {
		t.Fatalf("buildServerTLSConfig returned error: %v", err)
	}

	cert, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate v1 returned error: %v", err)
	}
	if got := certificateCommonName(t, cert); got != "server-v1.local" {
		t.Fatalf("certificate common name = %q, want server-v1.local", got)
	}

	time.Sleep(10 * time.Millisecond)
	writeSelfSignedPairFiles(t, certFile, keyFile, "server-v2.local", 2)

	cert, err = cfg.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate v2 returned error: %v", err)
	}
	if got := certificateCommonName(t, cert); got != "server-v2.local" {
		t.Fatalf("certificate common name = %q, want server-v2.local", got)
	}
}

func TestBuildServerTLSConfigKeepsCachedCertificateDuringPartialRotation(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeSelfSignedPair(t, dir, "server-v1.local")

	cfg, err := buildServerTLSConfig(ServerTLSConfig{
		CertFile: certFile,
		KeyFile:  keyFile,
	})
	if err != nil {
		t.Fatalf("buildServerTLSConfig returned error: %v", err)
	}

	cert, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate v1 returned error: %v", err)
	}
	if got := certificateCommonName(t, cert); got != "server-v1.local" {
		t.Fatalf("certificate common name = %q, want server-v1.local", got)
	}

	v2CertPEM, v2KeyPEM := selfSignedPairPEM(t, "server-v2.local", 2)
	time.Sleep(10 * time.Millisecond)
	if writeErr := os.WriteFile(certFile, v2CertPEM, 0o600); writeErr != nil {
		t.Fatalf("WriteFile(cert v2): %v", writeErr)
	}

	cert, err = cfg.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate during partial rotation returned error: %v", err)
	}
	if got := certificateCommonName(t, cert); got != "server-v1.local" {
		t.Fatalf("certificate common name during partial rotation = %q, want cached server-v1.local", got)
	}

	time.Sleep(10 * time.Millisecond)
	if writeErr := os.WriteFile(keyFile, v2KeyPEM, 0o600); writeErr != nil {
		t.Fatalf("WriteFile(key v2): %v", writeErr)
	}

	cert, err = cfg.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate after complete rotation returned error: %v", err)
	}
	if got := certificateCommonName(t, cert); got != "server-v2.local" {
		t.Fatalf("certificate common name after complete rotation = %q, want server-v2.local", got)
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

	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	writeSelfSignedPairFiles(t, certFile, keyFile, commonName, 1)
	return certFile, keyFile
}

func writeSelfSignedPairFiles(t *testing.T, certFile, keyFile, commonName string, serial int64) {
	t.Helper()
	certPEM, keyPEM := selfSignedPairPEM(t, commonName, serial)
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("WriteFile(cert): %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("WriteFile(key): %v", err)
	}
}

func selfSignedPairPEM(t *testing.T, commonName string, serial int64) ([]byte, []byte) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
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

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func certificateCommonName(t *testing.T, cert *tls.Certificate) string {
	t.Helper()
	if cert == nil || len(cert.Certificate) == 0 {
		t.Fatal("missing certificate chain")
	}
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return parsed.Subject.CommonName
}
