package provisioner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frameworks/cli/internal/templates"
	"frameworks/cli/pkg/inventory"
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

func TestBuildEdgeVars_Docker(t *testing.T) {
	ep := NewEdgeProvisioner(nil)
	config := EdgeProvisionConfig{
		Mode:            "docker",
		NodeID:          "node-123",
		PoolDomain:      "edge.example.com",
		Email:           "ops@example.com",
		FoghornGRPCAddr: "foghorn.example.com:18008",
		EnrollmentToken: "tok-abc",
		CertPEM:         "-----BEGIN CERTIFICATE-----\nMIIB...\n",
		KeyPEM:          "-----BEGIN PRIVATE KEY-----\nMIIE...\n",
		CABundlePEM:     "-----BEGIN CERTIFICATE-----\nCA...\n",
	}

	vars := ep.buildEdgeVars(config, "linux")

	if vars.Mode != "docker" {
		t.Errorf("Mode = %q, want docker", vars.Mode)
	}
	if vars.NodeID != "node-123" {
		t.Errorf("NodeID = %q, want node-123", vars.NodeID)
	}
	if vars.EdgeDomain != "edge.example.com" {
		t.Errorf("EdgeDomain = %q, want edge.example.com", vars.EdgeDomain)
	}
	if vars.GRPCTLSCAPath != "/etc/frameworks/pki/ca.crt" {
		t.Errorf("GRPCTLSCAPath = %q, want /etc/frameworks/pki/ca.crt", vars.GRPCTLSCAPath)
	}
	// Bootstrap leaves cert paths empty until ConfigSeed delivers the tenant certs.
	if vars.CertPath != "" || vars.KeyPath != "" {
		t.Error("CertPath/KeyPath should be empty (certs delivered via ConfigSeed)")
	}
	// Bootstrap config targets the primary domain. Helmsman manages wildcard routing.
	if vars.SiteAddress != "edge.example.com" {
		t.Errorf("SiteAddress = %q, want edge.example.com (bootstrap uses primary domain)", vars.SiteAddress)
	}
}

func TestBuildEdgeVars_NoCertFallsBackToSingleDomain(t *testing.T) {
	ep := NewEdgeProvisioner(nil)
	config := EdgeProvisionConfig{
		Mode:       "docker",
		PoolDomain: "edge.us-west.example.com",
		NodeDomain: "edge-abc123.us-west.example.com",
	}

	vars := ep.buildEdgeVars(config, "linux")

	if vars.SiteAddress != "edge.us-west.example.com" {
		t.Errorf("SiteAddress = %q, want edge.us-west.example.com (single domain when no cert)", vars.SiteAddress)
	}
}

func TestBuildEdgeVars_NativeNoCerts(t *testing.T) {
	ep := NewEdgeProvisioner(nil)
	config := EdgeProvisionConfig{
		Mode:       "native",
		NodeDomain: "edge-1.example.com",
	}

	vars := ep.buildEdgeVars(config, "linux")

	if vars.Mode != "native" {
		t.Errorf("Mode = %q, want native", vars.Mode)
	}
	if vars.CertPath != "" || vars.KeyPath != "" {
		t.Error("CertPath/KeyPath should be empty when no certs provided")
	}
}

func TestBuildEdgeVars_DarwinUserCAPATH(t *testing.T) {
	ep := NewEdgeProvisioner(nil)
	t.Setenv("HOME", "/Users/tester")
	config := EdgeProvisionConfig{
		Mode:         "native",
		NodeDomain:   "edge-1.example.com",
		CABundlePEM:  "-----BEGIN CERTIFICATE-----\nCA...\n",
		DarwinDomain: DomainUser,
	}

	vars := ep.buildEdgeVars(config, "darwin")

	want := "/Users/tester/.config/frameworks/pki/ca.crt"
	if vars.GRPCTLSCAPath != want {
		t.Errorf("GRPCTLSCAPath = %q, want %q", vars.GRPCTLSCAPath, want)
	}
}

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

	// All 3 files should be written in docker mode
	for _, f := range []string{"docker-compose.edge.yml", "Caddyfile", ".edge.env"} {
		path := filepath.Join(tmpDir, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected file %s to exist", f)
		}
	}

	// Bootstrap Caddyfile: maintenance mode with tls internal
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

	// Verify .edge.env has DEPLOY_MODE=docker
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

	// Docker-compose should NOT be written in native mode
	composePath := filepath.Join(tmpDir, "docker-compose.edge.yml")
	if _, err := os.Stat(composePath); !os.IsNotExist(err) {
		t.Error("docker-compose.edge.yml should NOT exist in native mode")
	}

	// Caddyfile and .edge.env should exist
	for _, f := range []string{"Caddyfile", ".edge.env"} {
		path := filepath.Join(tmpDir, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected file %s to exist in native mode", f)
		}
	}

	// Bootstrap Caddyfile: maintenance mode with native upstream for webhooks
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

	// Verify .edge.env has DEPLOY_MODE=native
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
	// Bootstrap Caddyfile uses tls internal, not file-based TLS or ACME
	if !strings.Contains(content, "tls internal") {
		t.Error("Bootstrap Caddyfile should use 'tls internal'")
	}
	if strings.Contains(content, "tls /etc") {
		t.Error("Bootstrap Caddyfile should NOT contain file-based TLS directive")
	}
	// Should serve 503 bootstrap page, not proxy to upstreams
	if !strings.Contains(content, "503") {
		t.Error("Bootstrap Caddyfile should serve 503")
	}
}

func TestGenerateSystemdUnit_WithLimitNOFILE(t *testing.T) {
	data := SystemdUnitData{
		ServiceName: "frameworks-mistserver",
		Description: "MistServer",
		WorkingDir:  "/opt/frameworks/mistserver",
		ExecStart:   "/opt/frameworks/mistserver/MistServer",
		User:        "frameworks",
		EnvFile:     "/etc/frameworks/mistserver.env",
		LimitNOFILE: "1048576",
	}

	content, err := GenerateSystemdUnit(data)
	if err != nil {
		t.Fatalf("GenerateSystemdUnit failed: %v", err)
	}

	if !strings.Contains(content, "LimitNOFILE=1048576") {
		t.Error("systemd unit should contain LimitNOFILE=1048576")
	}
}

func TestGenerateSystemdUnit_WithoutLimitNOFILE(t *testing.T) {
	data := SystemdUnitData{
		ServiceName: "frameworks-helmsman",
		Description: "Helmsman",
		WorkingDir:  "/opt/frameworks/helmsman",
		ExecStart:   "/opt/frameworks/helmsman/helmsman",
		User:        "frameworks",
	}

	content, err := GenerateSystemdUnit(data)
	if err != nil {
		t.Fatalf("GenerateSystemdUnit failed: %v", err)
	}

	if strings.Contains(content, "LimitNOFILE") {
		t.Error("systemd unit should NOT contain LimitNOFILE when not set")
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

func TestSudoPrefix(t *testing.T) {
	ep := NewEdgeProvisioner(nil)
	tests := []struct {
		name string
		user string
		want string
	}{
		{"root user", "root", ""},
		{"empty user defaults to root", "", ""},
		{"non-root user", "ubuntu", "sudo "},
		{"another non-root user", "deploy", "sudo "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := inventory.Host{User: tt.user}
			if got := ep.sudoPrefix(host); got != tt.want {
				t.Errorf("sudoPrefix() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildEdgeVars_NativeEdgeEnv(t *testing.T) {
	ep := NewEdgeProvisioner(nil)
	config := EdgeProvisionConfig{
		Mode:            "native",
		NodeDomain:      "edge-1.example.com",
		Email:           "ops@example.com",
		FoghornGRPCAddr: "foghorn.example.com:18008",
		EnrollmentToken: "tok-abc",
		NodeID:          "node-456",
	}

	vars := ep.buildEdgeVars(config, "linux")
	vars.Mode = "native"
	vars.SetModeDefaults()

	tmpDir := t.TempDir()
	if err := templates.WriteEdgeTemplates(tmpDir, vars, true); err != nil {
		t.Fatalf("WriteEdgeTemplates failed: %v", err)
	}

	envContent, err := os.ReadFile(filepath.Join(tmpDir, ".edge.env"))
	if err != nil {
		t.Fatalf("failed to read .edge.env: %v", err)
	}
	content := string(envContent)
	if !strings.Contains(content, "DEPLOY_MODE=native") {
		t.Error(".edge.env should contain DEPLOY_MODE=native for native mode")
	}
	if !strings.Contains(content, "NODE_ID=node-456") {
		t.Error(".edge.env should contain NODE_ID")
	}
	if !strings.Contains(content, "EDGE_DOMAIN=edge-1.example.com") {
		t.Error(".edge.env should contain EDGE_DOMAIN")
	}
}
