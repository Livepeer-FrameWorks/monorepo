package worker

import (
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHostedCertificateReadinessRequiresTrustNameAndCurrentRevision(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	leaf := server.Certificate()
	if len(leaf.DNSNames) == 0 {
		t.Fatal("TLS fixture has no server name")
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	for _, test := range []struct {
		name, serverName string
		expected         []byte
		roots            *x509.CertPool
		wantErr          bool
	}{
		{"applied certificate", leaf.DNSNames[0], leaf.Raw, roots, false},
		{"reload kept prior revision", leaf.DNSNames[0], []byte("new revision"), roots, true},
		{"wrong hostname", "foghorn.wrong.example", leaf.Raw, roots, true},
		{"untrusted certificate", leaf.DNSNames[0], leaf.Raw, x509.NewCertPool(), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := verifyHostedCertificate(t.Context(), server.Listener.Addr().String(), test.serverName, test.expected, test.roots)
			if (err != nil) != test.wantErr {
				t.Fatalf("readiness error=%v wantErr=%v", err, test.wantErr)
			}
		})
	}
}
