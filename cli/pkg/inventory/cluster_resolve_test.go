package inventory

import (
	"slices"
	"testing"
)

// Intent: ResolveCluster's documented priority is
// explicit service.cluster > explicit interface.cluster > role match >
// single cluster > "<type>-<profile>" fallback. Pin each deterministic branch.
func TestResolveCluster(t *testing.T) {
	t.Run("explicit service cluster wins", func(t *testing.T) {
		m := &Manifest{
			Type: "cluster", Profile: "regional",
			Services: map[string]ServiceConfig{"foghorn": {Cluster: "media-eu"}},
			Clusters: map[string]ClusterConfig{"media-eu": {}, "media-us": {}},
		}
		if got := m.ResolveCluster("foghorn"); got != "media-eu" {
			t.Fatalf("ResolveCluster = %q, want media-eu", got)
		}
	})

	t.Run("explicit interface cluster wins when not a service", func(t *testing.T) {
		m := &Manifest{
			Type: "cluster", Profile: "regional",
			Interfaces: map[string]ServiceConfig{"grafana": {Cluster: "obs"}},
			Clusters:   map[string]ClusterConfig{"obs": {}, "other": {}},
		}
		if got := m.ResolveCluster("grafana"); got != "obs" {
			t.Fatalf("ResolveCluster = %q, want obs", got)
		}
	})

	t.Run("no clusters falls back to type-profile", func(t *testing.T) {
		m := &Manifest{Type: "cluster", Profile: "control-plane"}
		if got := m.ResolveCluster("anything"); got != "cluster-control-plane" {
			t.Fatalf("ResolveCluster = %q, want cluster-control-plane", got)
		}
	})

	t.Run("single cluster is used", func(t *testing.T) {
		m := &Manifest{
			Type: "cluster", Profile: "regional",
			Clusters: map[string]ClusterConfig{"only-one": {}},
		}
		if got := m.ResolveCluster("svc-with-no-def"); got != "only-one" {
			t.Fatalf("ResolveCluster = %q, want only-one", got)
		}
	})

	t.Run("multi-cluster no match falls back to type-profile", func(t *testing.T) {
		m := &Manifest{
			Type: "edge", Profile: "edge-gateway",
			Clusters: map[string]ClusterConfig{"a": {}, "b": {}},
		}
		if got := m.ResolveCluster("svc-with-no-def"); got != "edge-edge-gateway" {
			t.Fatalf("ResolveCluster = %q, want edge-edge-gateway", got)
		}
	})
}

// Intent: HostCluster priority is explicit host.cluster > single-cluster
// shortcut > empty. Unknown host is always empty.
func TestHostCluster(t *testing.T) {
	t.Run("explicit host cluster", func(t *testing.T) {
		m := &Manifest{
			Hosts:    map[string]Host{"h1": {Cluster: "eu-1"}},
			Clusters: map[string]ClusterConfig{"eu-1": {}, "us-1": {}},
		}
		if got := m.HostCluster("h1"); got != "eu-1" {
			t.Fatalf("HostCluster = %q, want eu-1", got)
		}
	})

	t.Run("single-cluster shortcut for unassigned host", func(t *testing.T) {
		m := &Manifest{
			Hosts:    map[string]Host{"h1": {}},
			Clusters: map[string]ClusterConfig{"only": {}},
		}
		if got := m.HostCluster("h1"); got != "only" {
			t.Fatalf("HostCluster = %q, want only", got)
		}
	})

	t.Run("multi-cluster unassigned host is empty", func(t *testing.T) {
		m := &Manifest{
			Hosts:    map[string]Host{"h1": {}},
			Clusters: map[string]ClusterConfig{"a": {}, "b": {}},
		}
		if got := m.HostCluster("h1"); got != "" {
			t.Fatalf("HostCluster = %q, want empty", got)
		}
	})

	t.Run("unknown host is empty", func(t *testing.T) {
		m := &Manifest{
			Hosts:    map[string]Host{"h1": {Cluster: "eu-1"}},
			Clusters: map[string]ClusterConfig{"eu-1": {}},
		}
		if got := m.HostCluster("ghost"); got != "" {
			t.Fatalf("HostCluster(ghost) = %q, want empty", got)
		}
	})
}

// Intent: AllClusterIDs returns the sorted set of declared cluster IDs, or the
// single auto-generated "<type>-<profile>" id when no clusters are declared.
func TestAllClusterIDs(t *testing.T) {
	t.Run("sorted declared ids", func(t *testing.T) {
		m := &Manifest{Clusters: map[string]ClusterConfig{"zeta": {}, "alpha": {}, "mike": {}}}
		got := m.AllClusterIDs()
		want := []string{"alpha", "mike", "zeta"}
		if !slices.Equal(got, want) {
			t.Fatalf("AllClusterIDs = %v, want %v (sorted)", got, want)
		}
	})

	t.Run("auto id when no clusters", func(t *testing.T) {
		m := &Manifest{Type: "cluster", Profile: "analytics-only"}
		got := m.AllClusterIDs()
		if !slices.Equal(got, []string{"cluster-analytics-only"}) {
			t.Fatalf("AllClusterIDs = %v, want [cluster-analytics-only]", got)
		}
	})
}

// Intent: GetHost is a presence-aware lookup; MeshAddress prefers the host's
// WireGuard IP and falls back to the host name for unknown/unconfigured hosts.
func TestGetHostAndMeshAddress(t *testing.T) {
	m := &Manifest{Hosts: map[string]Host{
		"wg":    {WireguardIP: "10.0.0.5", ExternalIP: "1.2.3.4"},
		"no-wg": {ExternalIP: "1.2.3.5"},
	}}

	if _, ok := m.GetHost("ghost"); ok {
		t.Fatalf("GetHost(ghost) ok = true, want false")
	}
	h, ok := m.GetHost("wg")
	if !ok || h.WireguardIP != "10.0.0.5" {
		t.Fatalf("GetHost(wg) = (%+v,%v)", h, ok)
	}

	cases := []struct {
		name string
		host string
		want string
	}{
		{"wireguard ip preferred", "wg", "10.0.0.5"},
		{"falls back to name when no wg ip", "no-wg", "no-wg"},
		{"unknown host returns name", "ghost", "ghost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := m.MeshAddress(tc.host); got != tc.want {
				t.Fatalf("MeshAddress(%q) = %q, want %q", tc.host, got, tc.want)
			}
		})
	}
}

// Intent: GetInfrastructureHosts selects only hosts carrying the
// "infrastructure" role; GetAllHosts returns every host name.
func TestInfrastructureAndAllHosts(t *testing.T) {
	m := &Manifest{Hosts: map[string]Host{
		"infra-1": {Roles: []string{"infrastructure"}},
		"infra-2": {Roles: []string{"data", "infrastructure"}},
		"edge-1":  {Roles: []string{"media"}},
		"plain":   {},
	}}

	infra := m.GetInfrastructureHosts()
	slices.Sort(infra)
	if !slices.Equal(infra, []string{"infra-1", "infra-2"}) {
		t.Fatalf("GetInfrastructureHosts = %v, want [infra-1 infra-2]", infra)
	}

	all := m.GetAllHosts()
	slices.Sort(all)
	if !slices.Equal(all, []string{"edge-1", "infra-1", "infra-2", "plain"}) {
		t.Fatalf("GetAllHosts = %v, want all 4 host names", all)
	}
}
