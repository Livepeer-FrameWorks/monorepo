package provisioner

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

type testCA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM string
}

func newTestCA(t *testing.T, name string, parent *testCA) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	signer, signerKey := template, key
	if parent != nil {
		signer, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: cert, key: key, certPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

func (ca *testCA) issueLeaf(t *testing.T, notAfter time.Time) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "commodore"},
		NotBefore:    time.Now().Add(-48 * time.Hour),
		NotAfter:     notAfter,
		DNSNames:     []string{"commodore.internal"},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

// Privateer owns the PKI tree after bootstrap, so a provision or upgrade must
// leave a valid Privateer-written bundle and leaf alone even though the CLI
// hands the role a freshly minted leaf and differently formatted bundle.
func TestGoServicePKIKeepsValidPrivateerMaterial(t *testing.T) {
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		t.Skip("ansible-playbook not available")
	}
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	pkiTasks := filepath.Join(filepath.Dir(current), "..", "..", "..", "ansible", "collections", "ansible_collections", "frameworks", "infra", "roles", "go_service", "tasks", "pki.yml")

	root := newTestCA(t, "root", nil)
	intermediate := newTestCA(t, "intermediate", root)
	foreignRoot := newTestCA(t, "foreign-root", nil)
	desiredBundle := root.certPEM + intermediate.certPEM
	// Navigator's bundle carries the same certificates in its own layout.
	privateerBundle := strings.TrimSpace(intermediate.certPEM) + "\n" + strings.TrimSpace(root.certPEM)
	bootstrapCert, bootstrapKey := intermediate.issueLeaf(t, time.Now().Add(72*time.Hour))
	validCert, validKey := intermediate.issueLeaf(t, time.Now().Add(48*time.Hour))
	expiredCert, expiredKey := intermediate.issueLeaf(t, time.Now().Add(-time.Minute))
	foreignCert, foreignKey := foreignRoot.issueLeaf(t, time.Now().Add(48*time.Hour))
	_, otherKey := intermediate.issueLeaf(t, time.Now().Add(48*time.Hour))

	cases := []struct {
		name          string
		bundle        string
		cert, key     string
		wantCAUpdate  bool
		wantTLSUpdate bool
	}{
		{name: "valid privateer material", bundle: privateerBundle, cert: validCert, key: validKey},
		{name: "bundle lacks intermediate", bundle: root.certPEM, cert: validCert, key: validKey, wantCAUpdate: true},
		{name: "leaf from foreign CA", bundle: privateerBundle, cert: foreignCert, key: foreignKey, wantTLSUpdate: true},
		{name: "expired leaf", bundle: privateerBundle, cert: expiredCert, key: expiredKey, wantTLSUpdate: true},
		{name: "mismatched key", bundle: privateerBundle, cert: validCert, key: otherKey, wantTLSUpdate: true},
		{name: "nothing installed", wantCAUpdate: true, wantTLSUpdate: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			paths := map[string]string{
				"GRPC_TLS_CA_PATH":   filepath.Join(dir, "ca.crt"),
				"GRPC_TLS_CERT_PATH": filepath.Join(dir, "services", "commodore", "tls.crt"),
				"GRPC_TLS_KEY_PATH":  filepath.Join(dir, "services", "commodore", "tls.key"),
			}
			for key, content := range map[string]string{"GRPC_TLS_CA_PATH": tc.bundle, "GRPC_TLS_CERT_PATH": tc.cert, "GRPC_TLS_KEY_PATH": tc.key} {
				if content == "" {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(paths[key]), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(paths[key], []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			playbook := filepath.Join(dir, "p.yml")
			if err := os.WriteFile(playbook, []byte(`- hosts: localhost
  gather_facts: false
  tasks:
    - ansible.builtin.include_tasks: "`+pkiTasks+`"
`), 0o600); err != nil {
				t.Fatal(err)
			}
			vars, err := json.Marshal(map[string]any{
				"go_service_name":                   "commodore",
				"go_service_group":                  "frameworks",
				"go_service_env":                    paths,
				"go_service_internal_ca_bundle_pem": desiredBundle,
				"go_service_internal_tls_cert_pem":  bootstrapCert,
				"go_service_internal_tls_key_pem":   bootstrapKey,
			})
			if err != nil {
				t.Fatal(err)
			}
			varsPath := filepath.Join(dir, "vars.json")
			if err = os.WriteFile(varsPath, vars, 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), "ansible-playbook", "--check", "-v", "-i", "localhost,", "-c", "local", playbook, "-e", "@"+varsPath)
			cmd.Env = append(os.Environ(), "ANSIBLE_NOCOLOR=1")
			output, runErr := cmd.CombinedOutput()
			if runErr != nil {
				t.Fatalf("ansible-playbook: %v\n%s", runErr, output)
			}
			gotCA := regexp.MustCompile(`would update internal CA trust bundle`).Match(output)
			gotTLS := regexp.MustCompile(`would update bootstrap internal gRPC certificate`).Match(output)
			if gotCA != tc.wantCAUpdate || gotTLS != tc.wantTLSUpdate {
				t.Fatalf("CA update = %v (want %v), leaf update = %v (want %v)\n%s", gotCA, tc.wantCAUpdate, gotTLS, tc.wantTLSUpdate, output)
			}
		})
	}
}
