package logic

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"frameworks/api_dns/internal/store"
)

type fakeInternalCAStore struct {
	internalCAs map[string]*store.InternalCA
}

func (f *fakeInternalCAStore) GetInternalCA(_ context.Context, role string) (*store.InternalCA, error) {
	if ca, ok := f.internalCAs[role]; ok {
		return ca, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeInternalCAStore) SaveInternalCA(_ context.Context, ca *store.InternalCA) error {
	if f.internalCAs == nil {
		f.internalCAs = make(map[string]*store.InternalCA)
	}
	f.internalCAs[ca.Role] = ca
	return nil
}

func (f *fakeInternalCAStore) GetInternalCertificate(_ context.Context, _, _ string) (*store.InternalCertificate, error) {
	return nil, store.ErrNotFound
}

func (f *fakeInternalCAStore) SaveInternalCertificate(_ context.Context, _ *store.InternalCertificate) error {
	return nil
}

func TestEnsureCARequiresManagedFilesInProduction(t *testing.T) {
	t.Setenv("BUILD_ENV", "production")

	manager := NewInternalCAManager(&fakeInternalCAStore{}, nil, nil, "frameworks.network")
	err := manager.EnsureCA(context.Background())
	if err == nil {
		t.Fatal("expected managed CA requirement error")
	}
}

func TestEnsureCAImportsManagedMaterialFromBase64Env(t *testing.T) {
	rootCertPEM, rootKeyPEM, err := generateSelfSignedCATestCert("FrameWorks Root Test CA")
	if err != nil {
		t.Fatalf("generate root test cert: %v", err)
	}
	intermediateCertPEM, intermediateKeyPEM, err := generateSignedCATestCert("FrameWorks Intermediate Test CA", rootCertPEM, rootKeyPEM)
	if err != nil {
		t.Fatalf("generate intermediate test cert: %v", err)
	}

	t.Setenv("BUILD_ENV", "production")
	t.Setenv("NAVIGATOR_INTERNAL_CA_ROOT_CERT_PEM_B64", base64.StdEncoding.EncodeToString([]byte(rootCertPEM)))
	t.Setenv("NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_PEM_B64", base64.StdEncoding.EncodeToString([]byte(intermediateCertPEM)))
	t.Setenv("NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_PEM_B64", base64.StdEncoding.EncodeToString([]byte(intermediateKeyPEM)))

	fakeStore := &fakeInternalCAStore{}
	manager := NewInternalCAManager(fakeStore, nil, nil, "frameworks.network")
	if err := manager.EnsureCA(context.Background()); err != nil {
		t.Fatalf("EnsureCA returned error: %v", err)
	}

	rootCA, ok := fakeStore.internalCAs[internalRootRole]
	if !ok {
		t.Fatal("expected root CA to be stored")
	}
	intermediateCA, ok := fakeStore.internalCAs[internalIntermediateRole]
	if !ok {
		t.Fatal("expected intermediate CA to be stored")
	}
	if !strings.Contains(rootCA.CertPEM, "BEGIN CERTIFICATE") {
		t.Fatalf("expected PEM root cert, got %q", rootCA.CertPEM)
	}
	if !strings.Contains(intermediateCA.KeyPEM, "BEGIN EC PRIVATE KEY") {
		t.Fatalf("expected PEM private key, got %q", intermediateCA.KeyPEM)
	}
}

func TestEnsureCARejectsMismatchedIntermediateKey(t *testing.T) {
	rootCertPEM, rootKeyPEM, err := generateSelfSignedCATestCert("FrameWorks Root Test CA")
	if err != nil {
		t.Fatalf("generate root test cert: %v", err)
	}
	intermediateCertPEM, _, err := generateSignedCATestCert("FrameWorks Intermediate Test CA", rootCertPEM, rootKeyPEM)
	if err != nil {
		t.Fatalf("generate intermediate test cert: %v", err)
	}
	_, wrongIntermediateKeyPEM, err := generateSelfSignedCATestCert("Wrong Intermediate Key")
	if err != nil {
		t.Fatalf("generate wrong intermediate key: %v", err)
	}

	dir := t.TempDir()
	t.Setenv("NAVIGATOR_INTERNAL_CA_ROOT_CERT_FILE", writeTempPEMFile(t, dir, "root.crt", rootCertPEM))
	t.Setenv("NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_FILE", writeTempPEMFile(t, dir, "intermediate.crt", intermediateCertPEM))
	t.Setenv("NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_FILE", writeTempPEMFile(t, dir, "intermediate.key", wrongIntermediateKeyPEM))

	manager := NewInternalCAManager(&fakeInternalCAStore{}, nil, nil, "frameworks.network")
	err = manager.EnsureCA(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected mismatched key error, got %v", err)
	}
}

func TestEnsureCARejectsIntermediateSignedByWrongRoot(t *testing.T) {
	rootCertPEM, _, err := generateSelfSignedCATestCert("FrameWorks Root Test CA")
	if err != nil {
		t.Fatalf("generate root test cert: %v", err)
	}
	otherRootCertPEM, otherRootKeyPEM, err := generateSelfSignedCATestCert("Other Root Test CA")
	if err != nil {
		t.Fatalf("generate other root test cert: %v", err)
	}
	intermediateCertPEM, intermediateKeyPEM, err := generateSignedCATestCert("FrameWorks Intermediate Test CA", otherRootCertPEM, otherRootKeyPEM)
	if err != nil {
		t.Fatalf("generate intermediate test cert: %v", err)
	}

	dir := t.TempDir()
	t.Setenv("NAVIGATOR_INTERNAL_CA_ROOT_CERT_FILE", writeTempPEMFile(t, dir, "root.crt", rootCertPEM))
	t.Setenv("NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_FILE", writeTempPEMFile(t, dir, "intermediate.crt", intermediateCertPEM))
	t.Setenv("NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_FILE", writeTempPEMFile(t, dir, "intermediate.key", intermediateKeyPEM))

	manager := NewInternalCAManager(&fakeInternalCAStore{}, nil, nil, "frameworks.network")
	err = manager.EnsureCA(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not signed by root") {
		t.Fatalf("expected broken chain error, got %v", err)
	}
}

func TestEnsureLocalServerCertificateWritesBootstrapLeaf(t *testing.T) {
	rootCertPEM, rootKeyPEM, err := generateSelfSignedCATestCert("FrameWorks Root Test CA")
	if err != nil {
		t.Fatalf("generate root test cert: %v", err)
	}
	intermediateCertPEM, intermediateKeyPEM, err := generateSignedCATestCert("FrameWorks Intermediate Test CA", rootCertPEM, rootKeyPEM)
	if err != nil {
		t.Fatalf("generate intermediate test cert: %v", err)
	}

	store := &fakeInternalCAStore{
		internalCAs: map[string]*store.InternalCA{
			internalRootRole: {
				Role:    internalRootRole,
				CertPEM: rootCertPEM,
			},
			internalIntermediateRole: {
				Role:    internalIntermediateRole,
				CertPEM: intermediateCertPEM,
				KeyPEM:  intermediateKeyPEM,
			},
		},
	}
	manager := NewInternalCAManager(store, nil, nil, "frameworks.network")

	dir := t.TempDir()
	certPath := dir + "/tls.crt"
	keyPath := dir + "/tls.key"
	if err := manager.EnsureLocalServerCertificate(context.Background(), "navigator", certPath, keyPath); err != nil {
		t.Fatalf("EnsureLocalServerCertificate returned error: %v", err)
	}

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}
	cert, err := parseCertificatePEM(string(certPEM))
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	if !containsString(cert.DNSNames, "navigator.internal") {
		t.Fatalf("expected navigator.internal SANs, got %v", cert.DNSNames)
	}
	if _, err := os.ReadFile(keyPath); err != nil {
		t.Fatalf("read key: %v", err)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func generateSelfSignedCATestCert(commonName string) (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})), nil
}

func generateSignedCATestCert(commonName, parentCertPEM, parentKeyPEM string) (string, string, error) {
	parentCert, err := parseCertificatePEM(parentCertPEM)
	if err != nil {
		return "", "", err
	}
	parentKey, err := parseECPrivateKeyPEM(parentKeyPEM)
	if err != nil {
		return "", "", err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parentCert, &key.PublicKey, parentKey)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})), nil
}

func writeTempPEMFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := dir + "/" + name
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp pem file %s: %v", name, err)
	}
	return path
}
