package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"

	"gopkg.in/yaml.v3"
)

// manifestWithTwoMediaClusters extends manifestWithMedia with a second edge
// cluster and extra hosts, so node → cluster mapping can be exercised. Host
// keys become Quartermaster node IDs.
func manifestWithTwoMediaClusters(allowPrivate bool) *inventory.Manifest {
	m := manifestWithMedia(allowPrivate)
	m.Clusters["media-edge-secondary"] = inventory.ClusterConfig{
		Name:                    "Media Edge Secondary",
		Type:                    "edge",
		PlatformOfficial:        true,
		OwnerTenant:             "frameworks",
		Roles:                   []string{"media"},
		AllowPrivatePullSources: allowPrivate,
	}
	m.Hosts["media-eu-2"] = inventory.Host{
		Name: "media-eu-2", ExternalIP: "203.0.113.21", User: "root",
		WireguardIP: "10.99.0.3", WireguardPublicKey: "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=", WireguardPort: 51820,
		Cluster: "media-edge-primary",
	}
	m.Hosts["media-us-1"] = inventory.Host{
		Name: "media-us-1", ExternalIP: "203.0.113.30", User: "root",
		WireguardIP: "10.99.0.4", WireguardPublicKey: "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD=", WireguardPort: 51820,
		Cluster: "media-edge-secondary",
	}
	return m
}

func renderPullLocation(t *testing.T, location *SourceLocation, uri string, allowPrivate bool) (*Rendered, error) {
	t.Helper()
	d, err := Derive(manifestWithTwoMediaClusters(allowPrivate), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	overlay := &Overlay{Commodore: CommodoreSection{PullStreams: []PullStream{{
		PlaybackID: "located", OwnerTenant: TenantRefSystem(), Title: "Located",
		SourceURI: uri, Enabled: true, SourceLocation: location,
	}}}}
	return Render(d, overlay, nil)
}

func TestRenderSourceLocationClustersOnly(t *testing.T) {
	r, err := renderPullLocation(t, &SourceLocation{Clusters: []string{"media-edge-secondary", "media-edge-primary"}}, "https://example.com/live.m3u8", false)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := r.Commodore.PullStreams[0].SourceLocation
	if got == nil || len(got.Clusters) != 2 || got.Clusters[0].ClusterID != "media-edge-primary" || got.Clusters[1].ClusterID != "media-edge-secondary" {
		t.Fatalf("clusters not sorted: %+v", got)
	}
	if len(got.Clusters[0].NodeIDs) != 0 || len(got.Clusters[1].NodeIDs) != 0 || len(got.AvoidNodeIDs) != 0 {
		t.Fatalf("unexpected nodes: %+v", got)
	}
}

func TestRenderSourceLocationAbsentOrEmptyIsUnrestricted(t *testing.T) {
	for name, location := range map[string]*SourceLocation{"absent": nil, "empty": {}} {
		r, err := renderPullLocation(t, location, "https://example.com/live.m3u8", false)
		if err != nil {
			t.Fatalf("%s: Render: %v", name, err)
		}
		if r.Commodore.PullStreams[0].SourceLocation != nil {
			t.Fatalf("%s: unrestricted location rendered as %+v", name, r.Commodore.PullStreams[0].SourceLocation)
		}
	}
}

func TestRenderSourceLocationMapsNodesToClusters(t *testing.T) {
	r, err := renderPullLocation(t, &SourceLocation{
		Clusters:   []string{"media-edge-primary", "media-edge-secondary"},
		Nodes:      []string{"media-eu-2", "media-eu-1", "media-eu-2"},
		AvoidNodes: []string{"media-us-1"},
	}, "https://example.com/live.m3u8", false)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := r.Commodore.PullStreams[0].SourceLocation
	if strings.Join(got.Clusters[0].NodeIDs, ",") != "media-eu-1,media-eu-2" {
		t.Fatalf("primary nodes = %v, want sorted deduplicated [media-eu-1 media-eu-2]", got.Clusters[0].NodeIDs)
	}
	if len(got.Clusters[1].NodeIDs) != 0 {
		t.Fatalf("secondary cluster gained nodes: %v", got.Clusters[1].NodeIDs)
	}
	if strings.Join(got.AvoidNodeIDs, ",") != "media-us-1" {
		t.Fatalf("avoid nodes = %v", got.AvoidNodeIDs)
	}
}

func TestRenderSourceLocationRejectsInvalidNodes(t *testing.T) {
	for name, tc := range map[string]struct {
		location *SourceLocation
		want     string
	}{
		"node cluster not listed":  {&SourceLocation{Clusters: []string{"media-edge-primary"}, Nodes: []string{"media-us-1"}}, `belongs to cluster "media-edge-secondary"`},
		"avoid cluster not listed": {&SourceLocation{Clusters: []string{"media-edge-primary"}, AvoidNodes: []string{"media-us-1"}}, "source_location.avoid_nodes"},
		"unknown host":             {&SourceLocation{Clusters: []string{"media-edge-primary"}, Nodes: []string{"ghost-host"}}, `"ghost-host" is not a manifest host`},
		"nodes without clusters":   {&SourceLocation{Nodes: []string{"media-eu-1"}}, "require source_location.clusters"},
		"avoid without clusters":   {&SourceLocation{AvoidNodes: []string{"media-eu-1"}}, "require source_location.clusters"},
		"node in both lists":       {&SourceLocation{Clusters: []string{"media-edge-primary"}, Nodes: []string{"media-eu-1"}, AvoidNodes: []string{"media-eu-1"}}, "both nodes and avoid_nodes"},
		"non-media cluster":        {&SourceLocation{Clusters: []string{"ghost-cluster"}}, "ghost-cluster"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := renderPullLocation(t, tc.location, "https://example.com/live.m3u8", false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestRenderSourceLocationPrivateSourceNeedsClusters(t *testing.T) {
	if _, err := renderPullLocation(t, nil, "tsudp://10.0.0.5:9000", true); err == nil || !strings.Contains(err.Error(), "requires explicit source_location.clusters") {
		t.Fatalf("private source without clusters: %v", err)
	}
	r, err := renderPullLocation(t, &SourceLocation{Clusters: []string{"media-edge-primary"}, Nodes: []string{"media-eu-1"}}, "tsudp://10.0.0.5:9000", true)
	if err != nil {
		t.Fatalf("private source restricted to a consenting cluster node: %v", err)
	}
	if got := r.Commodore.PullStreams[0].SourceLocation; strings.Join(got.Clusters[0].NodeIDs, ",") != "media-eu-1" {
		t.Fatalf("source location = %+v", got)
	}
}

func TestRenderedSourceLocationYAMLShape(t *testing.T) {
	r, err := renderPullLocation(t, &SourceLocation{
		Clusters:   []string{"media-edge-primary", "media-edge-secondary"},
		Nodes:      []string{"media-eu-1"},
		AvoidNodes: []string{"media-us-1"},
	}, "https://example.com/live.m3u8", false)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	encoded, err := yaml.Marshal(r.Commodore.PullStreams[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `source_location:
    clusters:
        - cluster_id: media-edge-primary
          node_ids:
            - media-eu-1
        - cluster_id: media-edge-secondary
    avoid_node_ids:
        - media-us-1
`
	if !strings.Contains(string(encoded), want) {
		t.Fatalf("rendered YAML:\n%s\nwant fragment:\n%s", encoded, want)
	}
	if strings.Contains(string(encoded), "allowed_cluster_ids") {
		t.Fatalf("rendered YAML still carries allowed_cluster_ids:\n%s", encoded)
	}
}

func TestOverlayRejectsLegacyAllowedClusterIDs(t *testing.T) {
	d, err := Derive(manifestWithMedia(false), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	for name, body := range map[string]string{
		"pull stream": `commodore:
  pull_streams:
    - playback_id: legacy
      owner_tenant: {ref: quartermaster.system_tenant}
      title: Legacy
      source_uri: https://example.com/live.m3u8
      enabled: true
      allowed_cluster_ids: [media-edge-primary]
`,
		"mist native empty list": `commodore:
  mist_native_streams:
    - playback_id: legacy
      owner_tenant: {ref: quartermaster.system_tenant}
      title: Legacy
      source: "ts-exec:cat /dev/null"
      source_kind: exec
      always_on: true
      allowed_cluster_ids: []
`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bootstrap.yaml")
			if writeErr := os.WriteFile(path, []byte(body), 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
			overlay, loadErr := LoadOverlay(path)
			if loadErr != nil {
				t.Fatalf("strict overlay parse must reach render for a targeted message: %v", loadErr)
			}
			_, err := Render(d, overlay, nil)
			if err == nil || !strings.Contains(err.Error(), "allowed_cluster_ids is no longer supported") || !strings.Contains(err.Error(), "source_location") {
				t.Fatalf("error = %v, want legacy-key rejection naming source_location", err)
			}
		})
	}
}

func TestOverlayParsesSourceLocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bootstrap.yaml")
	body := `commodore:
  mist_native_streams:
    - playback_id: demo
      owner_tenant: {ref: quartermaster.system_tenant}
      title: Demo
      source: "ts-exec:cat /dev/null"
      source_kind: exec
      always_on: true
      source_location:
        clusters: [media-edge-primary]
        nodes: [media-eu-1]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	overlay, err := LoadOverlay(path)
	if err != nil {
		t.Fatalf("LoadOverlay: %v", err)
	}
	d, err := Derive(manifestWithMedia(false), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	r, err := Render(d, overlay, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := r.Commodore.MistNativeStreams[0].SourceLocation
	if got == nil || len(got.Clusters) != 1 || got.Clusters[0].ClusterID != "media-edge-primary" || strings.Join(got.Clusters[0].NodeIDs, ",") != "media-eu-1" {
		t.Fatalf("source location = %+v", got)
	}
}
