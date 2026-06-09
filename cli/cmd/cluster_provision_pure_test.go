package cmd

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"frameworks/cli/internal/readiness"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
)

func TestPhaseRunsPostProvisionInit(t *testing.T) {
	cases := map[orchestrator.Phase]bool{
		orchestrator.PhaseApplications:   true,
		orchestrator.PhaseAll:            true,
		orchestrator.PhaseInfrastructure: false,
		orchestrator.PhaseInterfaces:     false,
		orchestrator.PhaseMesh:           false,
	}
	for phase, want := range cases {
		if got := phaseRunsPostProvisionInit(phase); got != want {
			t.Errorf("phaseRunsPostProvisionInit(%q) = %v, want %v", phase, got, want)
		}
	}
}

// phaseSyncsEdgeReleaseTarget must track the same phase-set as
// phaseRunsPostProvisionInit; asserted independently so a future divergence
// is caught rather than silently accepted.
func TestPhaseSyncsEdgeReleaseTarget(t *testing.T) {
	cases := map[orchestrator.Phase]bool{
		orchestrator.PhaseApplications:   true,
		orchestrator.PhaseAll:            true,
		orchestrator.PhaseInfrastructure: false,
		orchestrator.PhaseInterfaces:     false,
	}
	for phase, want := range cases {
		if got := phaseSyncsEdgeReleaseTarget(phase); got != want {
			t.Errorf("phaseSyncsEdgeReleaseTarget(%q) = %v, want %v", phase, got, want)
		}
	}
}

func TestAdminDetail(t *testing.T) {
	cases := []struct {
		exists bool
		email  string
		want   string
	}{
		{true, "admin@example.com", "admin@example.com"},
		{true, "", "present"},
		{false, "admin@example.com", "missing"},
		{false, "", "missing"},
	}
	for _, tc := range cases {
		if got := adminDetail(tc.exists, tc.email); got != tc.want {
			t.Errorf("adminDetail(%v, %q) = %q, want %q", tc.exists, tc.email, got, tc.want)
		}
	}
}

func TestControlPlaneDetail(t *testing.T) {
	if got := controlPlaneDetail(readiness.Report{Checked: false}); got == "healthy" {
		t.Errorf("unchecked report should not report healthy, got %q", got)
	}
	if got := controlPlaneDetail(readiness.Report{Checked: true}); got != "healthy" {
		t.Errorf("0 warnings: got %q, want healthy", got)
	}
	one := controlPlaneDetail(readiness.Report{Checked: true, Warnings: make([]readiness.Warning, 1)})
	many := controlPlaneDetail(readiness.Report{Checked: true, Warnings: make([]readiness.Warning, 3)})
	// The distinguishing contract is singular vs plural wording, not the exact sentence.
	if one == many {
		t.Errorf("1-warning and 3-warning details should differ: both %q", one)
	}
}

func TestResolveEnvFilePath(t *testing.T) {
	abs := filepath.Join(string(filepath.Separator), "etc", "frameworks", "env")
	cases := []struct {
		path        string
		manifestDir string
		want        string
	}{
		{abs, "/manifests", abs},         // absolute → unchanged
		{"shared.env", "", "shared.env"}, // empty manifestDir → unchanged
		{"shared.env", "/manifests", filepath.Join("/manifests", "shared.env")}, // relative → joined
	}
	for _, tc := range cases {
		if got := resolveEnvFilePath(tc.path, tc.manifestDir); got != tc.want {
			t.Errorf("resolveEnvFilePath(%q, %q) = %q, want %q", tc.path, tc.manifestDir, got, tc.want)
		}
	}
}

func TestBatchContainsPrivateerAndService(t *testing.T) {
	batch := []*orchestrator.Task{
		{ServiceID: "foghorn", Type: "foghorn"},
		{ServiceID: "privateer", Type: "privateer"},
	}
	if !batchContainsPrivateer(batch) {
		t.Error("batchContainsPrivateer should be true when privateer task present")
	}
	if batchContainsPrivateer(nil) {
		t.Error("batchContainsPrivateer(nil) should be false")
	}

	// Matches on Type even when ServiceID differs (and vice-versa).
	typeOnly := []*orchestrator.Task{{Type: "privateer"}}
	if !batchContainsPrivateer(typeOnly) {
		t.Error("batchContainsPrivateer should match on Type")
	}
}

func TestRemainingBatchesContainService(t *testing.T) {
	batches := [][]*orchestrator.Task{
		{{ServiceID: "commodore"}},
		{{ServiceID: "foghorn"}, {Type: "privateer"}},
	}
	if !remainingBatchesContainService(batches, "privateer") {
		t.Error("expected privateer found across batches")
	}
	if !remainingBatchesContainService(batches, "commodore") {
		t.Error("expected commodore found in first batch")
	}
	if remainingBatchesContainService(batches, "navigator") {
		t.Error("navigator absent — want false")
	}
	if remainingBatchesContainService(nil, "privateer") {
		t.Error("nil batches — want false")
	}
}

func TestInternalPKIBootstrapRequired(t *testing.T) {
	cases := []struct {
		name     string
		manifest *inventory.Manifest
		want     bool
	}{
		{"nil", nil, false},
		{"dev profile short-circuits even with navigator", &inventory.Manifest{
			Profile:  "dev",
			Services: map[string]inventory.ServiceConfig{"navigator": {Enabled: true}},
		}, false},
		{"navigator enabled", &inventory.Manifest{
			Services: map[string]inventory.ServiceConfig{"navigator": {Enabled: true}},
		}, true},
		{"internal grpc leaf service enabled", &inventory.Manifest{
			Services: map[string]inventory.ServiceConfig{"commodore": {Enabled: true}},
		}, true},
		{"only non-leaf service enabled", &inventory.Manifest{
			Services: map[string]inventory.ServiceConfig{"bridge": {Enabled: true}},
		}, false},
		{"leaf service disabled", &inventory.Manifest{
			Services: map[string]inventory.ServiceConfig{"commodore": {Enabled: false}},
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := internalPKIBootstrapRequired(tc.manifest); got != tc.want {
				t.Errorf("internalPKIBootstrapRequired = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseEdgeTelemetryPublicKeyB64(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	valid, err := edgeTelemetryPublicKeyB64(key)
	if err != nil {
		t.Fatalf("encode public key: %v", err)
	}

	if _, err := parseEdgeTelemetryPublicKeyB64(valid); err != nil {
		t.Errorf("valid key: unexpected error %v", err)
	}
	if _, err := parseEdgeTelemetryPublicKeyB64("not-base64!!!"); err == nil {
		t.Error("invalid base64: expected error")
	}
	// Valid base64 but not a PEM block.
	if _, err := parseEdgeTelemetryPublicKeyB64("aGVsbG8="); err == nil {
		t.Error("non-PEM payload: expected error")
	}
}

func TestParseCertificatePEM(t *testing.T) {
	rootCert, _, _, _ := genTestInternalCA(t)
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootCert.Raw}))

	if _, err := parseCertificatePEM(certPEM); err != nil {
		t.Errorf("valid cert PEM: unexpected error %v", err)
	}
	if _, err := parseCertificatePEM("no pem here"); err == nil {
		t.Error("missing PEM block: expected error")
	}
	bad := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not-a-cert")}))
	if _, err := parseCertificatePEM(bad); err == nil {
		t.Error("malformed DER: expected error")
	}
}

func TestValidateInternalCA(t *testing.T) {
	rootCert, rootKey, interCert, interKey := genTestInternalCA(t)

	if err := validateInternalCA(rootCert, interCert, interKey); err != nil {
		t.Fatalf("valid chain: unexpected error %v", err)
	}

	// Key/cert mismatch: a freshly generated key won't match interCert.
	otherKey, _ := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err := validateInternalCA(rootCert, interCert, otherKey); err == nil {
		t.Error("key mismatch: expected error")
	}

	// Non-CA intermediate.
	leafCert, leafKey := genTestLeaf(t, rootCert, rootKey)
	if err := validateInternalCA(rootCert, leafCert, leafKey); err == nil {
		t.Error("non-CA intermediate: expected error")
	}

	// Broken chain: self-signed intermediate not issued by root.
	selfCert, selfKey := genTestSelfSignedCA(t)
	if err := validateInternalCA(rootCert, selfCert, selfKey); err == nil {
		t.Error("broken signature chain: expected error")
	}

	// Root that is not a CA is rejected before any chain checks.
	nonCARoot, _ := genTestSelfSigned(t, false, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err := validateInternalCA(nonCARoot, interCert, interKey); err == nil {
		t.Error("non-CA root: expected error")
	}

	// Expired root fails the validity-window check.
	expiredRoot, _ := genTestSelfSigned(t, true, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
	if err := validateInternalCA(expiredRoot, interCert, interKey); err == nil {
		t.Error("expired root: expected error")
	}
}

// --- test CA helpers ---

func genTestSelfSignedCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	return genTestSelfSigned(t, true, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
}

func genTestSelfSigned(t *testing.T, isCA bool, notBefore, notAfter time.Time) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  isCA,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(crand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create self-signed cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse self-signed cert: %v", err)
	}
	return cert, key
}

func genTestInternalCA(t *testing.T) (rootCert *x509.Certificate, rootKey *ecdsa.PrivateKey, interCert *x509.Certificate, interKey *ecdsa.PrivateKey) {
	t.Helper()
	rootCert, rootKey = genTestSelfSignedCA(t)

	interKey, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("gen intermediate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "test-intermediate"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(crand.Reader, tmpl, rootCert, &interKey.PublicKey, rootKey)
	if err != nil {
		t.Fatalf("create intermediate: %v", err)
	}
	interCert, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse intermediate: %v", err)
	}
	return rootCert, rootKey, interCert, interKey
}

func genTestLeaf(t *testing.T, issuer *x509.Certificate, issuerKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("gen leaf key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(3),
		Subject:               pkix.Name{CommonName: "test-leaf"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  false,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(crand.Reader, tmpl, issuer, &key.PublicKey, issuerKey)
	if err != nil {
		t.Fatalf("create leaf: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	return cert, key
}
