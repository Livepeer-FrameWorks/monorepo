package config

import (
	"os"
	"path/filepath"
	"testing"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

func TestApplyTLSBundleWritesReplaceableFiles(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "certs", "cert.pem")
	keyPath := filepath.Join(dir, "certs", "key.pem")
	t.Setenv("HELMSMAN_TLS_CERT_PATH", certPath)
	t.Setenv("HELMSMAN_TLS_KEY_PATH", keyPath)

	m := &Manager{logger: logging.NewLogger()}
	if !m.applyTLSBundle(&pb.TLSCertBundle{CertPem: "cert-a", KeyPem: "key-a", Domain: "*.edge.example"}) {
		t.Fatal("first applyTLSBundle returned false")
	}
	if got := readFileString(t, certPath); got != "cert-a" {
		t.Fatalf("cert = %q, want cert-a", got)
	}
	if got := readFileString(t, keyPath); got != "key-a" {
		t.Fatalf("key = %q, want key-a", got)
	}
	if mode := fileMode(t, certPath); mode != 0o644 {
		t.Fatalf("cert mode = %o, want 0644", mode)
	}
	if mode := fileMode(t, keyPath); mode != 0o640 {
		t.Fatalf("key mode = %o, want 0640", mode)
	}

	if !m.applyTLSBundle(&pb.TLSCertBundle{CertPem: "cert-b", KeyPem: "key-b", Domain: "*.edge.example"}) {
		t.Fatal("rotated applyTLSBundle returned false")
	}
	if got := readFileString(t, certPath); got != "cert-b" {
		t.Fatalf("rotated cert = %q, want cert-b", got)
	}
	if got := readFileString(t, keyPath); got != "key-b" {
		t.Fatalf("rotated key = %q, want key-b", got)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}
