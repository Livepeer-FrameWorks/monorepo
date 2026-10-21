//go:build caddy_verify

package config

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

const (
	realCaddyEdgeDomain = "edge-node-1.media-test.frameworks.test"
	realCaddyTenantHost = "acme.cdn.frameworks.test"
	realCaddyHealthBody = "helmsman-edge-health"
	// realCaddyMaintenanceBody is the maintenance.html every startRealCaddy
	// dir holds.
	realCaddyMaintenanceBody = "edge-maintenance-page"
)

// An origin-IP TLS client (Navigator's Cloudflare monitor) sends no SNI. The
// Caddyfile Helmsman renders must hand it the certificate that serves the edge
// domain, and its Host header must then reach Helmsman through @edge_health.
// Runs the release-pinned Caddy against the rendered file, for each way the
// edge domain's certificate can be configured.
func TestEdgeCaddyfile_NoSNIHandshake_RealCaddy(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	image := pinnedCaddyImage(t)
	helmsman := startFakeHelmsman(t)

	t.Run("loaded wildcard bundle among several", func(t *testing.T) {
		dir := t.TempDir()
		edgeCert := writeSelfSignedPair(t, dir, "cluster", "*.media-test.frameworks.test")
		tenantCert := writeSelfSignedPair(t, dir, "tenant", realCaddyTenantHost, "*."+realCaddyTenantHost)
		params := realCaddyParams(helmsman, []CaddyfileBundle{
			{SiteAddress: realCaddyTenantHost + " *." + realCaddyTenantHost, TLSCertPath: "/fw/tenant.crt", TLSKeyPath: "/fw/tenant.key"},
			{SiteAddress: "*.media-test.frameworks.test", TLSCertPath: "/fw/cluster.crt", TLSKeyPath: "/fw/cluster.key"},
		})
		addr := startRealCaddy(t, image, dir, params, "")

		leaf, status, body := noSNIGet(t, addr, realCaddyEdgeDomain, "/health")
		if !bytes.Equal(leaf.Raw, edgeCert.Raw) {
			t.Fatalf("no-SNI handshake served %v, want the cluster bundle certificate", leaf.DNSNames)
		}
		if status != http.StatusOK || body != realCaddyHealthBody {
			t.Fatalf("no-SNI GET /health Host %s = %d %q, want 200 %q from Helmsman", realCaddyEdgeDomain, status, body, realCaddyHealthBody)
		}
		if got := sniLeaf(t, addr, realCaddyTenantHost); !bytes.Equal(got.Raw, tenantCert.Raw) {
			t.Fatalf("SNI %s served %v, want the tenant bundle certificate", realCaddyTenantHost, got.DNSNames)
		}
		if got := sniLeaf(t, addr, realCaddyEdgeDomain); !bytes.Equal(got.Raw, edgeCert.Raw) {
			t.Fatalf("SNI %s served %v, want the cluster bundle certificate", realCaddyEdgeDomain, got.DNSNames)
		}
		if _, status, _ := noSNIGet(t, addr, "unknown.example.test", "/health"); status != http.StatusMisdirectedRequest {
			t.Fatalf("no-SNI request for an unserved host = %d, want 421", status)
		}
		// MistServer is down (nothing listens on its upstream), so the
		// request falls through to the maintenance page in MaintenanceRoot.
		if _, status, body := noSNIGet(t, addr, realCaddyEdgeDomain, "/live+stream/index.m3u8"); body != realCaddyMaintenanceBody {
			t.Fatalf("GET with MistServer down = %d %q, want the maintenance page %q", status, body, realCaddyMaintenanceBody)
		}
	})

	// The ACME issuer cannot be reached from a test, so the edge domain's
	// managed certificate comes from Caddy's internal issuer instead; both
	// store it under the site address it was obtained for.
	t.Run("managed certificate for the edge domain", func(t *testing.T) {
		dir := t.TempDir()
		tenantCert := writeSelfSignedPair(t, dir, "tenant", realCaddyTenantHost, "*."+realCaddyTenantHost)
		params := realCaddyParams(helmsman, []CaddyfileBundle{
			{SiteAddress: realCaddyTenantHost + " *." + realCaddyTenantHost, TLSCertPath: "/fw/tenant.crt", TLSKeyPath: "/fw/tenant.key"},
			{SiteAddress: realCaddyEdgeDomain},
		})
		addr := startRealCaddy(t, image, dir, params, "local_certs")

		leaf, status, body := noSNIGet(t, addr, realCaddyEdgeDomain, "/health")
		if err := leaf.VerifyHostname(realCaddyEdgeDomain); err != nil {
			t.Fatalf("no-SNI handshake served %v: %v", leaf.DNSNames, err)
		}
		if status != http.StatusOK || body != realCaddyHealthBody {
			t.Fatalf("no-SNI GET /health Host %s = %d %q, want 200 %q from Helmsman", realCaddyEdgeDomain, status, body, realCaddyHealthBody)
		}
		if got := sniLeaf(t, addr, realCaddyTenantHost); !bytes.Equal(got.Raw, tenantCert.Raw) {
			t.Fatalf("SNI %s served %v, want the tenant bundle certificate", realCaddyTenantHost, got.DNSNames)
		}
	})

	t.Run("managed wildcard certificate", func(t *testing.T) {
		dir := t.TempDir()
		params := realCaddyParams(helmsman, []CaddyfileBundle{{SiteAddress: "*.media-test.frameworks.test"}})
		addr := startRealCaddy(t, image, dir, params, "local_certs")

		leaf, status, body := noSNIGet(t, addr, realCaddyEdgeDomain, "/health")
		if err := leaf.VerifyHostname(realCaddyEdgeDomain); err != nil {
			t.Fatalf("no-SNI handshake served %v: %v", leaf.DNSNames, err)
		}
		if status != http.StatusOK || body != realCaddyHealthBody {
			t.Fatalf("no-SNI GET /health Host %s = %d %q, want 200 %q from Helmsman", realCaddyEdgeDomain, status, body, realCaddyHealthBody)
		}
	})
}

func realCaddyParams(helmsmanUpstream string, bundles []CaddyfileBundle) CaddyfileParams {
	return CaddyfileParams{
		Bundles:          bundles,
		CaddyAdminAddr:   "localhost:2019",
		HelmsmanUpstream: helmsmanUpstream,
		ChandlerUpstream: "127.0.0.1:9",
		MistUpstream:     "127.0.0.1:9",
		MaintenanceRoot:  "/fw",
		EdgeDomain:       realCaddyEdgeDomain,
	}
}

// pinnedCaddyImage returns the Caddy image and digest config/infrastructure.yaml
// pins for the release, the version edges install.
func pinnedCaddyImage(t *testing.T) string {
	t.Helper()
	if override := strings.TrimSpace(os.Getenv("FRAMEWORKS_CADDY_TEST_IMAGE")); override != "" {
		return override
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		raw, readErr := os.ReadFile(filepath.Join(dir, "config", "infrastructure.yaml"))
		if readErr == nil {
			image, digest, inCaddy := "", "", false
			for line := range strings.SplitSeq(string(raw), "\n") {
				field := strings.TrimSpace(line)
				if name, ok := strings.CutPrefix(field, "- name:"); ok {
					if inCaddy {
						break
					}
					inCaddy = strings.TrimSpace(name) == "caddy"
					continue
				}
				if !inCaddy {
					continue
				}
				if v, ok := strings.CutPrefix(field, "image:"); ok {
					image = strings.TrimSpace(v)
				}
				if v, ok := strings.CutPrefix(field, "digest:"); ok {
					digest = strings.TrimSpace(v)
				}
			}
			if image == "" || digest == "" {
				t.Fatalf("config/infrastructure.yaml: caddy must declare image and digest")
			}
			return image + "@" + digest
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("config/infrastructure.yaml not found from the test working directory")
		}
		dir = parent
	}
}

// startFakeHelmsman answers /health with a fixed body so a response proves the
// request was routed to the Helmsman upstream. It returns the upstream address
// as the Caddy container reaches it.
func startFakeHelmsman(t *testing.T) string {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health" || r.Host != realCaddyEdgeDomain {
				http.Error(w, "unexpected request "+r.Host+r.URL.Path, http.StatusTeapot)
				return
			}
			_, _ = io.WriteString(w, realCaddyHealthBody)
		}),
	}
	var wg sync.WaitGroup
	wg.Go(func() { _ = server.Serve(listener) })
	t.Cleanup(func() {
		_ = server.Close()
		wg.Wait()
	})
	return fmt.Sprintf("host.docker.internal:%d", listener.Addr().(*net.TCPAddr).Port)
}

// writeSelfSignedPair writes <dir>/<name>.crt and .key for the given DNS names.
func writeSelfSignedPair(t *testing.T, dir, name string, dnsNames ...string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		DNSNames:     dnsNames,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(30 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err = os.WriteFile(filepath.Join(dir, name+".crt"), certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, name+".key"), keyPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// startRealCaddy runs Caddy on the Caddyfile RenderCaddyfile produces for
// params, with dir mounted at /fw, and returns the published HTTPS address.
// globalOption, when set, is added to the rendered global options block.
func startRealCaddy(t *testing.T, image, dir string, params CaddyfileParams, globalOption string) string {
	t.Helper()
	rendered, err := RenderCaddyfile(params)
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}
	if globalOption != "" {
		rendered = strings.Replace(rendered, "{\n", "{\n\t"+globalOption+"\n", 1)
	}
	if err = os.WriteFile(filepath.Join(dir, "Caddyfile"), []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "maintenance.html"), []byte(realCaddyMaintenanceBody), 0o644); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("fw-edge-caddy-sni-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if t.Failed() {
			logs, logErr := dockerpg.CLI("logs", "--tail", "40", name)
			if logErr != nil {
				logs += logErr.Error()
			}
			t.Logf("rendered Caddyfile:\n%s\ncaddy logs:\n%s", rendered, logs)
		}
		_, _ = dockerpg.CLI("rm", "-fv", name)
	})
	out, err := dockerpg.Run("run", "-d", "--name", name,
		"--add-host", "host.docker.internal:host-gateway",
		"-p", "127.0.0.1::443/tcp",
		"-v", dir+":/fw:ro",
		image, "caddy", "run", "--config", "/fw/Caddyfile", "--adapter", "caddyfile")
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "443/tcp")
	if err != nil {
		t.Fatal(err)
	}
	addr := net.JoinHostPort("127.0.0.1", port)
	deadline := time.Now().Add(60 * time.Second)
	for {
		conn, dialErr := dialTLS(t, addr, realCaddyEdgeDomain)
		if dialErr == nil {
			_ = conn.Close()
			// Helmsman loads this file through Caddy's admin API, which warns on
			// every load when it differs from caddy fmt output.
			if logs, _ := dockerpg.CLI("logs", name); strings.Contains(logs, "input is not formatted") {
				t.Fatalf("rendered Caddyfile is not caddy fmt clean:\n%s", logs)
			}
			return addr
		}
		if time.Now().After(deadline) {
			t.Fatalf("caddy never completed a TLS handshake for %s: %v", realCaddyEdgeDomain, dialErr)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// sniLeaf returns the leaf certificate Caddy presents for serverName.
func sniLeaf(t *testing.T, addr, serverName string) *x509.Certificate {
	t.Helper()
	conn, err := dialTLS(t, addr, serverName)
	if err != nil {
		t.Fatalf("TLS handshake with SNI %s: %v", serverName, err)
	}
	defer func() { _ = conn.Close() }()
	return conn.ConnectionState().PeerCertificates[0]
}

// noSNIGet completes a TLS handshake without a server name, then sends one
// HTTP/1.1 GET with the given Host header over that connection. A managed
// certificate can still be in issuance right after start, so a failed
// handshake is retried for a short while.
func noSNIGet(t *testing.T, addr, host, path string) (*x509.Certificate, int, string) {
	t.Helper()
	var conn *tls.Conn
	var err error
	deadline := time.Now().Add(20 * time.Second)
	for {
		conn, err = dialTLS(t, addr, "")
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("TLS handshake without SNI: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if sni := conn.ConnectionState().ServerName; sni != "" {
		t.Fatalf("handshake carried SNI %q", sni)
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", path, host); err != nil {
		t.Fatalf("write request: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return conn.ConnectionState().PeerCertificates[0], resp.StatusCode, strings.TrimSpace(string(body))
}

// dialTLS completes one HTTP/1.1 TLS handshake with addr. An empty serverName
// makes crypto/tls omit the server_name extension. The served certificate is
// not verified here; callers inspect it.
func dialTLS(t *testing.T, addr, serverName string) (*tls.Conn, error) {
	t.Helper()
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 5 * time.Second},
		Config:    &tls.Config{ServerName: serverName, NextProtos: []string{"http/1.1"}, InsecureSkipVerify: true}, //nolint:gosec // fixture certificates are self-signed
	}
	conn, err := dialer.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	return conn.(*tls.Conn), nil
}
