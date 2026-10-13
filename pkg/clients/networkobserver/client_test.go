package networkobserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// internalObserver serves the inventory route with a leaf issued for
// InternalServerName by a private CA, as Navigator issues internal leaves,
// and returns the PEM file of that CA.
func internalObserver(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "FrameWorks internal test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: InternalServerName}, DNSNames: []string{"network-observer", InternalServerName, "localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer observer-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(Inventory{Provider: "livepeer", Network: "42161", Environment: "production"})
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	caFile := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return server, caFile
}

// Bridge reaches the observer at a mesh address whose certificate is issued by
// the internal CA for network-observer.internal. A release build accepts that
// path once it verifies the internal CA, and never falls back to plaintext.
func TestNewWithTLSVerifiesTheObserversInternalIdentity(t *testing.T) {
	server, caFile := internalObserver(t)
	// The route is an IP; the certificate names the service.
	client, err := NewWithTLS(server.URL, "observer-secret", false, TLS{CAFile: caFile})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := client.Inventory(context.Background(), false)
	if err != nil || inventory.Network != "42161" {
		t.Fatalf("internal inventory = %+v, err %v", inventory, err)
	}

	systemRoots, err := New(server.URL, "observer-secret", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, inventoryErr := systemRoots.Inventory(context.Background(), false); inventoryErr == nil {
		t.Fatal("an internal-CA certificate verified without the internal CA")
	}
	wrongName, err := NewWithTLS(server.URL, "observer-secret", false, TLS{CAFile: caFile, ServerName: "commodore.internal"})
	if err != nil {
		t.Fatal(err)
	}
	if _, inventoryErr := wrongName.Inventory(context.Background(), false); inventoryErr == nil {
		t.Fatal("a leaf for another identity was accepted")
	}
	if _, plainErr := NewWithTLS("http://network-observer.internal:18024", "observer-secret", false, TLS{CAFile: caFile}); plainErr == nil {
		t.Fatal("a release client accepted a plaintext observer URL")
	}
}

// The relay passes the prober's own credential through, replaces any
// forwarded address with the relay's view of the source, and returns the
// observer's status and body unchanged.
func TestRelayForwardsProberCredentialAndSourceAddress(t *testing.T) {
	var got *http.Request
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	client, err := New(server.URL, "bridge-held-secret", true)
	if err != nil {
		t.Fatal(err)
	}
	relayed, err := client.Relay(context.Background(), http.MethodPost, "/v1/observations", "a=1", "Bearer prober-secret", "198.51.100.7", strings.NewReader(`{"id":"batch"}`))
	if err != nil {
		t.Fatal(err)
	}
	if relayed.Status != http.StatusAccepted || string(relayed.Body) != `{"ok":true}` || relayed.ContentType != "application/json" {
		t.Fatalf("relayed response = %+v", relayed)
	}
	if got.URL.Path != "/v1/observations" || got.URL.RawQuery != "a=1" || gotBody != `{"id":"batch"}` {
		t.Fatalf("observer received %s?%s %q", got.URL.Path, got.URL.RawQuery, gotBody)
	}
	if got.Header.Get("Authorization") != "Bearer prober-secret" {
		t.Fatalf("observer authenticated %q, want the prober's own credential", got.Header.Get("Authorization"))
	}
	if got.Header.Get("X-Forwarded-For") != "198.51.100.7" {
		t.Fatalf("forwarded source = %q, want the relay-resolved address", got.Header.Get("X-Forwarded-For"))
	}
}
