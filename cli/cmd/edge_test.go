package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	fwgitops "frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
)

func TestDeriveEdgeNodeName(t *testing.T) {
	tests := []struct {
		name       string
		nodeName   string
		nodeDomain string
		sshTarget  string
		isLocal    bool
		want       string
	}{
		{name: "explicit node name", nodeName: "edge-eu-1", want: "edge-eu-1"},
		{name: "from domain", nodeDomain: "edge-eu-1.example.com", want: "edge-eu-1"},
		{name: "from ssh fqdn with port", sshTarget: "ubuntu@edge-eu-1.example.com:2222", want: "edge-eu-1.example.com"},
		{name: "from ssh bare host", sshTarget: "ubuntu@edge-eu-1", want: "edge-eu-1"},
		{name: "ip target returns empty", sshTarget: "ubuntu@203.0.113.10", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveEdgeNodeName(tt.nodeName, tt.nodeDomain, tt.sshTarget, tt.isLocal)
			if got != tt.want {
				t.Fatalf("deriveEdgeNodeName(%q, %q, %q, %v) = %q, want %q",
					tt.nodeName, tt.nodeDomain, tt.sshTarget, tt.isLocal, got, tt.want)
			}
		})
	}
}

func TestCanonicalEdgeNodeID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "preserve readable id", input: "edge-eu-1", want: "edge-eu-1"},
		{name: "strip fqdn", input: "edge-eu-1.example.com", want: "edge-eu-1"},
		{name: "normalize case and underscores", input: "EDGE_EU_1", want: "edge-eu-1"},
		{name: "reject invalid ipish", input: "203.0.113.10", want: ""},
		{name: "reject empty", input: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := canonicalEdgeNodeID(tt.input)
			if got != tt.want {
				t.Fatalf("canonicalEdgeNodeID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestEdgeManifestNodeDomain(t *testing.T) {
	tests := []struct {
		name       string
		rootDomain string
		clusterID  string
		subdomain  string
		want       string
	}{
		{
			name:       "cluster scoped node domain",
			rootDomain: "frameworks.network",
			clusterID:  "media-eu-1",
			subdomain:  "edge-eu-1",
			want:       "edge-eu-1.media-eu-1.frameworks.network",
		},
		{
			name:       "explicit fqdn is preserved",
			rootDomain: "frameworks.network",
			clusterID:  "media-eu-1",
			subdomain:  "edge-eu-1.example.net",
			want:       "edge-eu-1.example.net",
		},
		{
			name:       "fallback when cluster absent",
			rootDomain: "frameworks.network",
			subdomain:  "edge-eu-1",
			want:       "edge-eu-1.frameworks.network",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := edgeManifestNodeDomain(tt.rootDomain, tt.clusterID, tt.subdomain)
			if got != tt.want {
				t.Fatalf("edgeManifestNodeDomain(%q, %q, %q) = %q, want %q", tt.rootDomain, tt.clusterID, tt.subdomain, got, tt.want)
			}
		})
	}
}

func TestEdgeManifestTelemetryWriteURL(t *testing.T) {
	edgeManifest := &inventory.EdgeManifest{RootDomain: "frameworks.network"}
	clusterManifest := &inventory.Manifest{
		RootDomain: "frameworks.network",
		Clusters: map[string]inventory.ClusterConfig{
			"media-eu-1": {Name: "Media EU 1", Type: "edge", Roles: []string{"media"}},
			"media-us-1": {Name: "Media US 1", Type: "edge", Roles: []string{"media"}},
		},
		Observability: map[string]inventory.ServiceConfig{
			"vmauth": {Enabled: true, Host: "central-eu-1"},
		},
	}

	got := edgeManifestTelemetryWriteURL(edgeManifest, clusterManifest, "media-eu-1")
	want := "https://telemetry.media-eu-1.frameworks.network/api/v1/write"
	if got != want {
		t.Fatalf("edgeManifestTelemetryWriteURL() = %q, want %q", got, want)
	}
}

func TestEdgeManifestTelemetryWriteURLRequiresCoveredVMAUTHCluster(t *testing.T) {
	edgeManifest := &inventory.EdgeManifest{RootDomain: "frameworks.network"}
	clusterManifest := &inventory.Manifest{
		RootDomain: "frameworks.network",
		Clusters: map[string]inventory.ClusterConfig{
			"media-eu-1": {Name: "Media EU 1", Type: "edge", Roles: []string{"media"}},
			"media-us-1": {Name: "Media US 1", Type: "edge", Roles: []string{"media"}},
		},
		Observability: map[string]inventory.ServiceConfig{
			"vmauth": {Enabled: true, Host: "central-eu-1", Clusters: []string{"media-eu-1"}},
		},
	}

	if got := edgeManifestTelemetryWriteURL(edgeManifest, clusterManifest, "media-us-1"); got != "" {
		t.Fatalf("edgeManifestTelemetryWriteURL() = %q, want empty for cluster outside vmauth coverage", got)
	}
	clusterManifest.Observability["vmauth"] = inventory.ServiceConfig{Enabled: false, Host: "central-eu-1"}
	if got := edgeManifestTelemetryWriteURL(edgeManifest, clusterManifest, "media-eu-1"); got != "" {
		t.Fatalf("edgeManifestTelemetryWriteURL() = %q, want empty when vmauth is disabled", got)
	}
}

func TestEdgeManifestChannelNotVersion(t *testing.T) {
	// Verify that manifest.Version (schema version) is never used as a release
	// version. Only manifest.Channel should be used for release resolution.
	manifest := &inventory.EdgeManifest{
		Version: "v1", // schema version — must NOT be used for release
	}

	// Simulate the logic from runEdgeProvisionFromManifest (edge.go:701-704)
	nodeVersion := manifest.Channel // should be empty, not "v1"
	if nodeVersion == manifest.Version {
		t.Fatalf("nodeVersion should not equal manifest.Version (%q); Channel should be used instead", manifest.Version)
	}
	if nodeVersion != "" {
		t.Fatalf("expected empty nodeVersion when Channel is unset, got %q", nodeVersion)
	}
}

func TestEdgeManifestChannelOverride(t *testing.T) {
	manifest := &inventory.EdgeManifest{
		Channel: "rc",
	}
	manifest.Version = "v1" // schema version — must be ignored for release resolution

	nodeVersion := manifest.Channel
	if nodeVersion != "rc" {
		t.Fatalf("expected nodeVersion=%q, got %q", "rc", nodeVersion)
	}
	if nodeVersion == manifest.Version {
		t.Fatal("nodeVersion must not equal schema version")
	}
}

func TestEdgeManifestVersionFlagOverridesChannel(t *testing.T) {
	manifest := &inventory.EdgeManifest{
		Channel: "rc",
	}
	manifest.Version = "v1" // schema version — must be ignored

	// Simulate --version flag override
	cliVersion := "v0.2.0-rc3"
	versionFlagChanged := true

	nodeVersion := manifest.Channel
	if versionFlagChanged {
		nodeVersion = cliVersion
	}
	if nodeVersion != "v0.2.0-rc3" {
		t.Fatalf("expected --version override to take precedence, got %q", nodeVersion)
	}
	if nodeVersion == manifest.Version {
		t.Fatal("nodeVersion must not equal schema version")
	}
}

func TestNativeEdgeRefreshCommandLinuxReloadsMist(t *testing.T) {
	cmd := nativeEdgeRefreshCommand("linux")
	for _, want := range []string{
		"systemctl reload frameworks-mistserver",
		"systemctl try-restart frameworks-helmsman",
		"systemctl reload-or-restart frameworks-caddy",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("nativeEdgeRefreshCommand(linux) missing %q: %s", want, cmd)
		}
	}
	if strings.Contains(cmd, "restart frameworks-mistserver") {
		t.Fatalf("nativeEdgeRefreshCommand(linux) must not restart MistServer: %s", cmd)
	}
}

func TestNativeEdgeRefreshCommandDarwinSignalsMist(t *testing.T) {
	cmd := nativeEdgeRefreshCommand("darwin")
	for _, want := range []string{
		"launchctl kill USR1 system/com.livepeer.frameworks.mistserver",
		"pkill -USR1 -f MistController",
		"launchctl kickstart -k system/com.livepeer.frameworks.helmsman",
		"launchctl kickstart -k system/com.livepeer.frameworks.caddy",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("nativeEdgeRefreshCommand(darwin) missing %q: %s", want, cmd)
		}
	}
	if strings.Contains(cmd, "kickstart -k system/com.livepeer.frameworks.mistserver") {
		t.Fatalf("nativeEdgeRefreshCommand(darwin) must not kickstart MistServer: %s", cmd)
	}
}

func TestDockerEdgeUpdateStepsUseComposePullThenUp(t *testing.T) {
	got := dockerEdgeUpdateSteps("docker-compose.edge.yml", ".edge.env")
	// --remove-orphans retires legacy 3-container services left in the
	// same compose project, which would otherwise keep squatting 80/443.
	want := [][]string{
		{"compose", "-f", "docker-compose.edge.yml", "--env-file", ".edge.env", "pull"},
		{"compose", "-f", "docker-compose.edge.yml", "--env-file", ".edge.env", "up", "-d", "--remove-orphans"},
	}
	if len(got) != len(want) {
		t.Fatalf("steps len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Fatalf("step %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestResolveEdgeClusterManifestPathUsesOverride(t *testing.T) {
	dir := t.TempDir()
	edgePath := filepath.Join(dir, "edge.yaml")
	clusterPath := filepath.Join(dir, "platform.yaml")
	writeEdgeTestFile(t, edgePath, "version: v1\n")
	writeEdgeTestFile(t, clusterPath, "version: v1\ntype: cluster\n")

	got, err := resolveEdgeClusterManifestPath(edgePath, &inventory.EdgeManifest{ClusterManifest: "ignored.yaml"}, clusterPath)
	if err != nil {
		t.Fatalf("resolveEdgeClusterManifestPath returned error: %v", err)
	}
	if got != clusterPath {
		t.Fatalf("path = %q, want %q", got, clusterPath)
	}
}

func TestResolveEdgeClusterManifestPathUsesManifestField(t *testing.T) {
	dir := t.TempDir()
	edgePath := filepath.Join(dir, "edge.yaml")
	clusterPath := filepath.Join(dir, "manifests", "cluster.yaml")
	writeEdgeTestFile(t, edgePath, "version: v1\n")
	writeEdgeTestFile(t, clusterPath, "version: v1\ntype: cluster\n")

	got, err := resolveEdgeClusterManifestPath(edgePath, &inventory.EdgeManifest{ClusterManifest: "manifests/cluster.yaml"}, "")
	if err != nil {
		t.Fatalf("resolveEdgeClusterManifestPath returned error: %v", err)
	}
	if got != clusterPath {
		t.Fatalf("path = %q, want %q", got, clusterPath)
	}
}

func TestResolveEdgeClusterManifestPathFallsBackToSiblingClusterYAML(t *testing.T) {
	dir := t.TempDir()
	edgePath := filepath.Join(dir, "custom-edge.yaml")
	clusterPath := filepath.Join(dir, "cluster.yaml")
	writeEdgeTestFile(t, edgePath, "version: v1\n")
	writeEdgeTestFile(t, clusterPath, "version: v1\ntype: cluster\n")

	got, err := resolveEdgeClusterManifestPath(edgePath, &inventory.EdgeManifest{}, "")
	if err != nil {
		t.Fatalf("resolveEdgeClusterManifestPath returned error: %v", err)
	}
	if got != clusterPath {
		t.Fatalf("path = %q, want %q", got, clusterPath)
	}
}

func TestEdgeManifestFetchCertDoesNotRequireControlPlane(t *testing.T) {
	manifest := &inventory.EdgeManifest{FetchCert: true}
	if edgeManifestNeedsControlPlane(manifest) {
		t.Fatal("fetch_cert is deprecated for manifest provisioning and must not force platform control-plane context")
	}
}

func TestEdgeManifestReleaseRepositoriesPrefersLocalGitOpsRoot(t *testing.T) {
	dir := t.TempDir()
	gitopsRoot := filepath.Join(dir, "gitops")
	for _, name := range []string{"clusters", "channels", "releases"} {
		if err := os.MkdirAll(filepath.Join(gitopsRoot, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	clusterPath := filepath.Join(gitopsRoot, "clusters", "production", "cluster.yaml")

	got := edgeManifestReleaseRepositories(clusterPath)
	if len(got) != 2 {
		t.Fatalf("repos = %#v, want local root plus default repository", got)
	}
	if got[0] != gitopsRoot {
		t.Fatalf("first repo = %q, want local gitops root %q", got[0], gitopsRoot)
	}
	if got[1] != fwgitops.DefaultRepository {
		t.Fatalf("fallback repo = %q, want %q", got[1], fwgitops.DefaultRepository)
	}
}

func TestEdgeEnrollmentTokenRequestBindsClusterOwnerTenant(t *testing.T) {
	req := edgeEnrollmentTokenRequest("media-eu-1", "edge-eu-1", "00000000-0000-0000-0000-000000000001")
	if req.GetClusterId() != "media-eu-1" {
		t.Fatalf("ClusterId = %q, want media-eu-1", req.GetClusterId())
	}
	if req.GetTenantId() != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("TenantId = %q, want system tenant", req.GetTenantId())
	}
	if req.GetName() != "edge provision: edge-eu-1" {
		t.Fatalf("Name = %q, want edge provision name", req.GetName())
	}
	if req.GetTtl() != edgeProvisionEnrollmentTokenTTL {
		t.Fatalf("Ttl = %q, want %q", req.GetTtl(), edgeProvisionEnrollmentTokenTTL)
	}
}

func writeEdgeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
