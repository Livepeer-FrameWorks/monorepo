package provisioner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frameworks/cli/internal/templates"
	"frameworks/cli/pkg/gitops"
)

func TestEdgeProvisionConfig_PrimaryDomain(t *testing.T) {
	tests := []struct {
		name       string
		poolDomain string
		nodeDomain string
		want       string
	}{
		{"pool takes precedence", "edge.example.com", "edge-1.example.com", "edge.example.com"},
		{"falls back to node domain", "", "edge-1.example.com", "edge-1.example.com"},
		{"both empty", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := EdgeProvisionConfig{PoolDomain: tt.poolDomain, NodeDomain: tt.nodeDomain}
			if got := c.primaryDomain(); got != tt.want {
				t.Errorf("primaryDomain() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEdgeProvisionConfig_ResolvedMode(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want string
	}{
		{"explicit docker", "docker", "docker"},
		{"explicit native", "native", "native"},
		{"empty defaults to docker", "", "docker"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := EdgeProvisionConfig{Mode: tt.mode}
			if got := c.resolvedMode(); got != tt.want {
				t.Errorf("resolvedMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The templates package is still used by `edge init` for operators who
// render the compose/env/Caddyfile locally before running the role against
// their own host. These tests pin the bootstrap shape so renames don't
// silently drop fields from that local-render path.

func TestWriteEdgeTemplates_DockerMode(t *testing.T) {
	tmpDir := t.TempDir()
	vars := templates.EdgeVars{
		NodeID:          "test-node",
		EdgeDomain:      "edge-1.example.com",
		SiteAddress:     "edge-1.example.com",
		AcmeEmail:       "ops@example.com",
		FoghornGRPCAddr: "foghorn.example.com:18008",
		EnrollmentToken: "tok-abc",
		Mode:            "docker",
	}

	if err := templates.WriteEdgeTemplates(tmpDir, vars, true); err != nil {
		t.Fatalf("WriteEdgeTemplates failed: %v", err)
	}

	for _, f := range []string{"docker-compose.edge.yml", "Caddyfile", ".edge.env"} {
		path := filepath.Join(tmpDir, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected file %s to exist", f)
		}
	}

	caddyfile, err := os.ReadFile(filepath.Join(tmpDir, "Caddyfile"))
	if err != nil {
		t.Fatalf("failed to read Caddyfile: %v", err)
	}
	content := string(caddyfile)
	if !strings.Contains(content, "tls internal") {
		t.Error("Bootstrap Caddyfile should contain 'tls internal'")
	}
	if !strings.Contains(content, "helmsman:18007") {
		t.Error("Bootstrap Caddyfile should contain Docker upstream helmsman:18007 for webhooks")
	}
	if !strings.Contains(content, "503") {
		t.Error("Bootstrap Caddyfile should serve 503 during bootstrap")
	}

	envContent, _ := os.ReadFile(filepath.Join(tmpDir, ".edge.env"))
	if !strings.Contains(string(envContent), "DEPLOY_MODE=docker") {
		t.Error(".edge.env should contain DEPLOY_MODE=docker")
	}

	composeContent, err := os.ReadFile(filepath.Join(tmpDir, "docker-compose.edge.yml"))
	if err != nil {
		t.Fatalf("failed to read docker-compose.edge.yml: %v", err)
	}
	if strings.Contains(string(composeContent), "./pki:/etc/frameworks/pki:ro") {
		t.Error("docker compose should not mount ./pki read-only; Helmsman updates the CA bundle via ConfigSeed")
	}
}

func TestWriteEdgeTemplates_DockerModeDefaultsMistServerImage(t *testing.T) {
	tmpDir := t.TempDir()
	vars := templates.EdgeVars{Mode: "docker"}

	if err := templates.WriteEdgeTemplates(tmpDir, vars, true); err != nil {
		t.Fatalf("WriteEdgeTemplates failed: %v", err)
	}

	composeContent, err := os.ReadFile(filepath.Join(tmpDir, "docker-compose.edge.yml"))
	if err != nil {
		t.Fatalf("failed to read docker-compose.edge.yml: %v", err)
	}
	if !strings.Contains(string(composeContent), "image: mistserver:latest") {
		t.Error("docker compose should default to mistserver:latest for local edge init")
	}
}

func TestWriteEdgeTemplates_NativeMode(t *testing.T) {
	tmpDir := t.TempDir()
	vars := templates.EdgeVars{
		NodeID:          "test-node",
		EdgeDomain:      "edge-1.example.com",
		SiteAddress:     "edge-1.example.com",
		AcmeEmail:       "ops@example.com",
		FoghornGRPCAddr: "foghorn.example.com:18008",
		EnrollmentToken: "tok-abc",
		Mode:            "native",
	}

	if err := templates.WriteEdgeTemplates(tmpDir, vars, true); err != nil {
		t.Fatalf("WriteEdgeTemplates failed: %v", err)
	}

	composePath := filepath.Join(tmpDir, "docker-compose.edge.yml")
	if _, err := os.Stat(composePath); !os.IsNotExist(err) {
		t.Error("docker-compose.edge.yml should NOT exist in native mode")
	}

	for _, f := range []string{"Caddyfile", ".edge.env"} {
		path := filepath.Join(tmpDir, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected file %s to exist in native mode", f)
		}
	}

	caddyfile, err := os.ReadFile(filepath.Join(tmpDir, "Caddyfile"))
	if err != nil {
		t.Fatalf("failed to read Caddyfile: %v", err)
	}
	content := string(caddyfile)
	if !strings.Contains(content, "tls internal") {
		t.Error("Bootstrap Caddyfile should contain 'tls internal'")
	}
	if !strings.Contains(content, "localhost:18007") {
		t.Error("Bootstrap Caddyfile should contain native upstream localhost:18007 for webhooks")
	}
	if !strings.Contains(content, "localhost:2019") {
		t.Error("Bootstrap Caddyfile should contain native admin localhost:2019")
	}

	envContent, _ := os.ReadFile(filepath.Join(tmpDir, ".edge.env"))
	if !strings.Contains(string(envContent), "DEPLOY_MODE=native") {
		t.Error(".edge.env should contain DEPLOY_MODE=native")
	}
}

func TestWriteEdgeTemplates_BootstrapCaddyfile(t *testing.T) {
	tmpDir := t.TempDir()
	vars := templates.EdgeVars{
		EdgeDomain:  "edge-1.example.com",
		SiteAddress: "edge-1.example.com",
		AcmeEmail:   "ops@example.com",
		Mode:        "docker",
	}

	if err := templates.WriteEdgeTemplates(tmpDir, vars, true); err != nil {
		t.Fatalf("WriteEdgeTemplates failed: %v", err)
	}

	caddyfile, _ := os.ReadFile(filepath.Join(tmpDir, "Caddyfile"))
	content := string(caddyfile)
	if !strings.Contains(content, "tls internal") {
		t.Error("Bootstrap Caddyfile should use 'tls internal'")
	}
	if strings.Contains(content, "tls /etc") {
		t.Error("Bootstrap Caddyfile should NOT contain file-based TLS directive")
	}
	if !strings.Contains(content, "503") {
		t.Error("Bootstrap Caddyfile should serve 503")
	}
}

func TestParseUnameOutput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantOS   string
		wantArch string
		wantErr  bool
	}{
		{"linux amd64", "Linux x86_64", "linux", "amd64", false},
		{"linux arm64 aarch64", "Linux aarch64", "linux", "arm64", false},
		{"linux arm64 native", "Linux arm64", "linux", "arm64", false},
		{"linux armv7l", "Linux armv7l", "linux", "arm", false},
		{"trailing newline", "Linux x86_64\n", "linux", "amd64", false},
		{"leading whitespace", "  Linux x86_64  ", "linux", "amd64", false},
		{"empty", "", "", "", true},
		{"single field", "Linux", "", "", true},
		{"three fields", "Linux x86_64 extra", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			osName, arch, err := ParseUnameOutput(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseUnameOutput() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if osName != tt.wantOS {
				t.Errorf("ParseUnameOutput() osName = %q, want %q", osName, tt.wantOS)
			}
			if arch != tt.wantArch {
				t.Errorf("ParseUnameOutput() arch = %q, want %q", arch, tt.wantArch)
			}
		})
	}
}

func TestEdgeExternalImagePinsDigest(t *testing.T) {
	manifest := &gitops.Manifest{
		ExternalDependencies: []gitops.ExternalDependency{{
			Name:   "mistserver",
			Image:  "ghcr.io/livepeer-frameworks/mistserver:development-c97caf1",
			Digest: "sha256:abc123",
		}},
	}

	got, err := edgeExternalImage(manifest, "mistserver")
	if err != nil {
		t.Fatalf("edgeExternalImage returned error: %v", err)
	}
	want := "ghcr.io/livepeer-frameworks/mistserver:development-c97caf1@sha256:abc123"
	if got != want {
		t.Fatalf("edgeExternalImage = %q, want %q", got, want)
	}
}

func TestEdgeExternalBinaryMatchesMistServerReleaseAssetName(t *testing.T) {
	manifest := &gitops.Manifest{
		ExternalDependencies: []gitops.ExternalDependency{{
			Name: "mistserver",
			Binaries: []gitops.ExternalBinary{
				{Name: "docker-tag.txt", URL: "https://example.test/docker-tag.txt"},
				{Name: "mistserver-darwin-arm64-development-c97caf1.tar.gz", URL: "https://example.test/darwin.tar.gz"},
				{Name: "mistserver-linux-amd64-development-c97caf1.tar.gz", URL: "https://example.test/linux-amd64.tar.gz", Checksum: "sha256:abc"},
				{Name: "mistserver-linux-arm64-development-c97caf1.tar.gz", URL: "https://example.test/linux-arm64.tar.gz"},
			},
		}},
	}

	url, checksum, err := edgeExternalBinary(manifest, "mistserver", "linux-amd64")
	if err != nil {
		t.Fatalf("edgeExternalBinary returned error: %v", err)
	}
	if url != "https://example.test/linux-amd64.tar.gz" {
		t.Fatalf("url = %q", url)
	}
	if checksum != "sha256:abc" {
		t.Fatalf("checksum = %q", checksum)
	}
}
