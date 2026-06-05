package config

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestApplyTLSBundleWritesReplaceableFiles(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "certs", "cert.pem")
	keyPath := filepath.Join(dir, "certs", "key.pem")
	t.Setenv("HELMSMAN_TLS_CERT_PATH", certPath)
	t.Setenv("HELMSMAN_TLS_KEY_PATH", keyPath)
	t.Setenv("CADDY_TLS_GROUP", strconv.Itoa(os.Getgid()))

	m := &Manager{logger: logging.NewLogger()}
	if !m.applyTLSBundle(&ipcpb.TLSCertBundle{CertPem: "cert-a", KeyPem: "key-a", Domain: "*.edge.example"}) {
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

	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatalf("chmod stale key mode: %v", err)
	}
	if !m.applyTLSBundle(&ipcpb.TLSCertBundle{CertPem: "cert-a", KeyPem: "key-a", Domain: "*.edge.example"}) {
		t.Fatal("metadata repair applyTLSBundle returned false")
	}
	if mode := fileMode(t, keyPath); mode != 0o640 {
		t.Fatalf("repaired key mode = %o, want 0640", mode)
	}

	if !m.applyTLSBundle(&ipcpb.TLSCertBundle{CertPem: "cert-b", KeyPem: "key-b", Domain: "*.edge.example"}) {
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

func fullFileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode()
}

func TestApplyTLSBundlesWritesPerBundleFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HELMSMAN_TLS_BUNDLE_DIR", dir)
	t.Setenv("CADDY_TLS_GROUP", strconv.Itoa(os.Getgid()))

	m := &Manager{logger: logging.NewLogger()}
	bundles := []*ipcpb.TLSCertBundle{
		{
			BundleId:      "cluster:media-us-1",
			CertPem:       "cluster-cert",
			KeyPem:        "cluster-key",
			Domain:        "*.media-us-1.frameworks.network",
			SiteAddresses: []string{"*.media-us-1.frameworks.network"},
		},
		{
			BundleId:      "tenant:acme",
			CertPem:       "tenant-cert",
			KeyPem:        "tenant-key",
			Domain:        "*.acme.cdn.frameworks.network",
			SiteAddresses: []string{"acme.cdn.frameworks.network", "*.acme.cdn.frameworks.network"},
		},
	}

	changed, results := m.applyTLSBundles(bundles)
	if !changed {
		t.Fatal("expected changed=true on first apply")
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		if !r.Success {
			t.Fatalf("bundle %s failed: %s", r.BundleID, r.Err)
		}
	}

	// Verify per-bundle files written with sanitized stems.
	if got := readFileString(t, filepath.Join(dir, "cluster_media-us-1.crt")); got != "cluster-cert" {
		t.Fatalf("cluster cert = %q", got)
	}
	if got := readFileString(t, filepath.Join(dir, "tenant_acme.crt")); got != "tenant-cert" {
		t.Fatalf("tenant cert = %q", got)
	}

	keyPath := filepath.Join(dir, "cluster_media-us-1.key")
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatalf("chmod stale bundle key mode: %v", err)
	}
	changed, results = m.applyTLSBundles(bundles)
	if !changed {
		t.Fatal("expected changed=true for bundle key metadata repair")
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 repair results, got %d", len(results))
	}
	if mode := fileMode(t, keyPath); mode != 0o640 {
		t.Fatalf("repaired bundle key mode = %o, want 0640", mode)
	}
	if mode := fullFileMode(t, dir); mode&fs.ModeSetgid == 0 || mode.Perm() != 0o770 {
		t.Fatalf("bundle dir mode = %s (%o), want setgid 0770", mode, mode.Perm())
	}
}

func TestApplyTLSBundlesRemovesStaleFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HELMSMAN_TLS_BUNDLE_DIR", dir)
	t.Setenv("CADDY_TLS_GROUP", strconv.Itoa(os.Getgid()))

	m := &Manager{logger: logging.NewLogger()}

	// Seed two bundles.
	first := []*ipcpb.TLSCertBundle{
		{BundleId: "cluster:media-us-1", CertPem: "c1", KeyPem: "k1", SiteAddresses: []string{"*.media-us-1.frameworks.network"}},
		{BundleId: "tenant:acme", CertPem: "c2", KeyPem: "k2", SiteAddresses: []string{"acme.cdn.frameworks.network"}},
	}
	m.applyTLSBundles(first)
	if _, err := os.Stat(filepath.Join(dir, "tenant_acme.crt")); err != nil {
		t.Fatalf("tenant cert missing after first apply: %v", err)
	}

	// Re-apply only the cluster bundle — tenant files should be cleaned up.
	second := []*ipcpb.TLSCertBundle{first[0]}
	m.applyTLSBundles(second)
	if _, err := os.Stat(filepath.Join(dir, "tenant_acme.crt")); !os.IsNotExist(err) {
		t.Fatalf("expected tenant cert removed, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tenant_acme.key")); !os.IsNotExist(err) {
		t.Fatalf("expected tenant key removed, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cluster_media-us-1.crt")); err != nil {
		t.Fatalf("cluster cert wrongly removed: %v", err)
	}
}

func TestRenderCaddyfileMultiBundle(t *testing.T) {
	out, err := RenderCaddyfile(CaddyfileParams{
		Bundles: []CaddyfileBundle{
			{
				SiteAddress: "*.media-us-1.frameworks.network",
				TLSCertPath: "/etc/frameworks/certs/bundles/cluster_media-us-1.crt",
				TLSKeyPath:  "/etc/frameworks/certs/bundles/cluster_media-us-1.key",
			},
			{
				SiteAddress: "acme.cdn.frameworks.network *.acme.cdn.frameworks.network",
				TLSCertPath: "/etc/frameworks/certs/bundles/tenant_acme.crt",
				TLSKeyPath:  "/etc/frameworks/certs/bundles/tenant_acme.key",
			},
		},
		CaddyAdminAddr:   "localhost:2019",
		HelmsmanUpstream: "helmsman:18007",
		ChandlerUpstream: "chandler:18020",
		MistUpstream:     "mistserver:8080",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	mustContain := []string{
		"(common_handlers)",
		"*.media-us-1.frameworks.network {",
		"acme.cdn.frameworks.network *.acme.cdn.frameworks.network {",
		"tls /etc/frameworks/certs/bundles/cluster_media-us-1.crt /etc/frameworks/certs/bundles/cluster_media-us-1.key",
		"tls /etc/frameworks/certs/bundles/tenant_acme.crt /etc/frameworks/certs/bundles/tenant_acme.key",
		"header {",
		"import common_handlers",
		"reverse_proxy mistserver:8080",
		"keepalive 64s",
	}
	for _, want := range mustContain {
		if !contains(out, want) {
			t.Errorf("rendered Caddyfile missing %q:\n%s", want, out)
		}
	}
	if contains(out, "headers {") {
		t.Fatalf("rendered Caddyfile uses invalid Caddy directive \"headers\":\n%s", out)
	}
	if contains(out, "keepalive 64\n") {
		t.Fatalf("rendered Caddyfile uses invalid unitless keepalive duration:\n%s", out)
	}
}

func TestComposeCaddyBundlesKeepsEdgeDomainWhenClusterBundleMissing(t *testing.T) {
	bundles := composeCaddyBundles(&ipcpb.ConfigSeed{
		Site: &ipcpb.SiteConfig{
			EdgeDomain: "edge-eu-1.media-eu-1.frameworks.network",
		},
		TlsBundles: []*ipcpb.TLSCertBundle{
			{
				BundleId:      "platform:edge-multi",
				SiteAddresses: []string{"edge.frameworks.network", "edge-ingest.frameworks.network"},
			},
		},
	})

	if len(bundles) != 2 {
		t.Fatalf("bundle count = %d, want 2: %#v", len(bundles), bundles)
	}
	if got := bundles[1].SiteAddress; got != "edge-eu-1.media-eu-1.frameworks.network" {
		t.Fatalf("fallback SiteAddress = %q", got)
	}
	if bundles[1].TLSCertPath != "" || bundles[1].TLSKeyPath != "" {
		t.Fatalf("fallback bundle should use Caddy-managed ACME, got cert=%q key=%q", bundles[1].TLSCertPath, bundles[1].TLSKeyPath)
	}
}

func TestComposeCaddyBundlesDoesNotDuplicateCoveredEdgeDomain(t *testing.T) {
	bundles := composeCaddyBundles(&ipcpb.ConfigSeed{
		Site: &ipcpb.SiteConfig{
			EdgeDomain: "edge-eu-1.media-eu-1.frameworks.network",
		},
		TlsBundles: []*ipcpb.TLSCertBundle{
			{
				BundleId:      "cluster:media-eu-1",
				SiteAddresses: []string{"*.media-eu-1.frameworks.network"},
			},
		},
	})

	if len(bundles) != 1 {
		t.Fatalf("bundle count = %d, want 1: %#v", len(bundles), bundles)
	}
	if got := bundles[0].SiteAddress; got != "*.media-eu-1.frameworks.network" {
		t.Fatalf("SiteAddress = %q", got)
	}
}

func TestRenderCaddyfileEmptyBundlesFails(t *testing.T) {
	if _, err := RenderCaddyfile(CaddyfileParams{}); err == nil {
		t.Fatal("expected error for empty bundles")
	}
}

func TestCaddyfileAdminAddrUsesAddressNotURL(t *testing.T) {
	t.Setenv("CADDY_ADMIN_URL", "http://localhost:2019")
	if got := caddyfileAdminAddr(); got != "localhost:2019" {
		t.Fatalf("caddyfileAdminAddr() = %q, want localhost:2019", got)
	}
}

func TestCaddyfileAdminAddrKeepsUnixSocket(t *testing.T) {
	t.Setenv("CADDY_ADMIN_SOCKET", "/run/caddy/admin.sock")
	if got := caddyfileAdminAddr(); got != "unix//run/caddy/admin.sock" {
		t.Fatalf("caddyfileAdminAddr() = %q, want unix//run/caddy/admin.sock", got)
	}
}

func TestPersistCaddyfileUsesConfiguredPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Caddyfile")
	t.Setenv("CADDY_CONFIG_PATH", path)

	m := &Manager{logger: logging.NewLogger()}
	if err := m.persistCaddyfile([]byte("edge.example { respond ok }\n")); err != nil {
		t.Fatalf("persistCaddyfile: %v", err)
	}
	if got := readFileString(t, path); got != "edge.example { respond ok }\n" {
		t.Fatalf("persisted Caddyfile = %q", got)
	}
	if mode := fileMode(t, path); mode != 0o644 {
		t.Fatalf("Caddyfile mode = %o, want 0644", mode)
	}
}

func TestReloadCaddyReadsConfiguredPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Caddyfile")
	const content = "edge.example { respond ok }\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write Caddyfile: %v", err)
	}
	t.Setenv("CADDY_CONFIG_PATH", path)

	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("CADDY_ADMIN_URL", srv.URL)
	m := &Manager{logger: logging.NewLogger()}
	if !m.reloadCaddy(nil) {
		t.Fatal("reloadCaddy returned false")
	}
	if gotBody != content {
		t.Fatalf("reload body = %q", gotBody)
	}
}

func TestActivateCaddyReloadsEvenWhenRenderedConfigHashIsUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Caddyfile")
	t.Setenv("CADDY_CONFIG_PATH", path)
	t.Setenv("HELMSMAN_TLS_BUNDLE_DIR", t.TempDir())
	t.Setenv("CADDY_TLS_GROUP", strconv.Itoa(os.Getgid()))

	reloadCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reloadCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("CADDY_ADMIN_URL", srv.URL)

	seed := &ipcpb.ConfigSeed{
		Site: &ipcpb.SiteConfig{
			EdgeDomain: "edge-eu-1.media-eu-1.frameworks.network",
		},
		TlsBundles: []*ipcpb.TLSCertBundle{{
			BundleId:      "cluster:media-eu-1",
			CertPem:       "cert",
			KeyPem:        "key",
			SiteAddresses: []string{"*.media-eu-1.frameworks.network"},
		}},
	}
	m := &Manager{logger: logging.NewLogger()}
	changed, results := m.applyTLSBundles(seed.GetTlsBundles())
	if !changed || len(results) != 1 || !results[0].Success {
		t.Fatalf("applyTLSBundles changed=%v results=%#v", changed, results)
	}
	if !m.activateCaddy(seed, false) {
		t.Fatal("first activateCaddy returned false")
	}
	if !m.activateCaddy(seed, false) {
		t.Fatal("second activateCaddy returned false")
	}
	if reloadCount != 2 {
		t.Fatalf("reload count = %d, want 2", reloadCount)
	}
}

func TestActivateCaddyRepairsUnreadableKeyBeforeReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Caddyfile")
	t.Setenv("CADDY_CONFIG_PATH", path)
	bundleDir := t.TempDir()
	t.Setenv("HELMSMAN_TLS_BUNDLE_DIR", bundleDir)
	t.Setenv("CADDY_TLS_GROUP", strconv.Itoa(os.Getgid()))

	reloadCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reloadCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("CADDY_ADMIN_URL", srv.URL)

	seed := &ipcpb.ConfigSeed{
		TlsBundles: []*ipcpb.TLSCertBundle{{
			BundleId:      "cluster:media-eu-1",
			CertPem:       "cert",
			KeyPem:        "key",
			SiteAddresses: []string{"*.media-eu-1.frameworks.network"},
		}},
	}
	m := &Manager{logger: logging.NewLogger()}
	changed, results := m.applyTLSBundles(seed.GetTlsBundles())
	if !changed || len(results) != 1 || !results[0].Success {
		t.Fatalf("applyTLSBundles changed=%v results=%#v", changed, results)
	}
	keyPath := filepath.Join(bundleDir, "cluster_media-eu-1.key")
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatalf("chmod key: %v", err)
	}
	if !m.activateCaddy(seed, false) {
		t.Fatal("activateCaddy returned false for repairable key metadata")
	}
	if mode := fileMode(t, keyPath); mode != 0o640 {
		t.Fatalf("repaired key mode = %o, want 0640", mode)
	}
	if reloadCount != 1 {
		t.Fatalf("reload count = %d, want 1", reloadCount)
	}
}

func TestActivateCaddyRejectsMissingKeyBeforeReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Caddyfile")
	t.Setenv("CADDY_CONFIG_PATH", path)
	bundleDir := t.TempDir()
	t.Setenv("HELMSMAN_TLS_BUNDLE_DIR", bundleDir)
	t.Setenv("CADDY_TLS_GROUP", strconv.Itoa(os.Getgid()))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("reload should not be called when TLS preflight fails")
	}))
	defer srv.Close()
	t.Setenv("CADDY_ADMIN_URL", srv.URL)

	seed := &ipcpb.ConfigSeed{
		TlsBundles: []*ipcpb.TLSCertBundle{{
			BundleId:      "cluster:media-eu-1",
			CertPem:       "cert",
			KeyPem:        "key",
			SiteAddresses: []string{"*.media-eu-1.frameworks.network"},
		}},
	}
	m := &Manager{logger: logging.NewLogger()}
	changed, results := m.applyTLSBundles(seed.GetTlsBundles())
	if !changed || len(results) != 1 || !results[0].Success {
		t.Fatalf("applyTLSBundles changed=%v results=%#v", changed, results)
	}
	keyPath := filepath.Join(bundleDir, "cluster_media-eu-1.key")
	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("remove key: %v", err)
	}
	if m.activateCaddy(seed, false) {
		t.Fatal("activateCaddy returned true for missing key")
	}
}

func TestReloadCaddyAcceptsEmptyOKResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/load" {
			t.Fatalf("path = %s, want /load", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("CADDY_ADMIN_URL", srv.URL)
	m := &Manager{logger: logging.NewLogger()}
	if !m.reloadCaddy([]byte("edge.example { respond ok }")) {
		t.Fatal("reloadCaddy returned false for empty 200 response")
	}
}

func TestReloadCaddyAcceptsCaddyAdapterWarningBodyOnOKResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"file":"Caddyfile","line":2,"message":"Caddyfile input is not formatted; run 'caddy fmt --overwrite' to fix inconsistencies"}]`))
	}))
	defer srv.Close()

	t.Setenv("CADDY_ADMIN_URL", srv.URL)
	m := &Manager{logger: logging.NewLogger()}
	if !m.reloadCaddy([]byte("edge.example { respond ok }")) {
		t.Fatal("reloadCaddy returned false for Caddy adapter warning body")
	}
}

func TestReloadCaddyRejectsUnexpectedBodyOnOKResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("loading config: permission denied"))
	}))
	defer srv.Close()

	t.Setenv("CADDY_ADMIN_URL", srv.URL)
	m := &Manager{logger: logging.NewLogger()}
	if m.reloadCaddy([]byte("edge.example { respond ok }")) {
		t.Fatal("reloadCaddy returned true for 200 response with unexpected body")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
