package inventory

import "testing"

func TestPostgresConfigIsYugabyte(t *testing.T) {
	if !(&PostgresConfig{Engine: "yugabyte"}).IsYugabyte() {
		t.Error("yugabyte engine should be Yugabyte")
	}
	if (&PostgresConfig{Engine: "postgres"}).IsYugabyte() {
		t.Error("postgres engine should not be Yugabyte")
	}
	if (&PostgresConfig{}).IsYugabyte() {
		t.Error("empty engine should not be Yugabyte")
	}
}

func TestPostgresConfigEffectivePort(t *testing.T) {
	tests := []struct {
		name string
		cfg  PostgresConfig
		want int
	}{
		{"explicit_port_wins", PostgresConfig{Port: 6000}, 6000},
		{"postgres_default", PostgresConfig{}, 5432},
		{"yugabyte_default", PostgresConfig{Engine: "yugabyte"}, 5433},
		{"explicit_overrides_yugabyte_default", PostgresConfig{Engine: "yugabyte", Port: 6000}, 6000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.EffectivePort(); got != tt.want {
				t.Fatalf("EffectivePort() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPostgresConfigAllHosts(t *testing.T) {
	t.Run("nodes_take_priority_over_host", func(t *testing.T) {
		cfg := PostgresConfig{
			Host:  "ignored",
			Nodes: []PostgresNode{{Host: "n1"}, {Host: "n2"}},
		}
		got := cfg.AllHosts()
		if len(got) != 2 || got[0] != "n1" || got[1] != "n2" {
			t.Fatalf("AllHosts() = %v, want [n1 n2]", got)
		}
	})
	t.Run("single_host_when_no_nodes", func(t *testing.T) {
		got := (&PostgresConfig{Host: "solo"}).AllHosts()
		if len(got) != 1 || got[0] != "solo" {
			t.Fatalf("AllHosts() = %v, want [solo]", got)
		}
	})
	t.Run("empty_when_nothing_set", func(t *testing.T) {
		if got := (&PostgresConfig{}).AllHosts(); len(got) != 0 {
			t.Fatalf("AllHosts() = %v, want []", got)
		}
	})
}

func TestPostgresConfigMasterAddresses(t *testing.T) {
	identity := func(h string) string { return h }

	t.Run("empty_without_nodes", func(t *testing.T) {
		if got := (&PostgresConfig{}).MasterAddresses(identity); got != "" {
			t.Fatalf("MasterAddresses() = %q, want empty", got)
		}
	})
	t.Run("default_and_custom_rpc_ports", func(t *testing.T) {
		cfg := PostgresConfig{Nodes: []PostgresNode{
			{Host: "h1"},
			{Host: "h2", RpcPort: 7200},
		}}
		got := cfg.MasterAddresses(func(h string) string { return "10." + h })
		want := "10.h1:7100,10.h2:7200"
		if got != want {
			t.Fatalf("MasterAddresses() = %q, want %q", got, want)
		}
	})
}

func TestPostgresConfigEffectiveReplicationFactor(t *testing.T) {
	tests := []struct {
		name string
		cfg  PostgresConfig
		want int
	}{
		{"explicit_factor", PostgresConfig{ReplicationFactor: 3}, 3},
		{"defaults_to_node_count", PostgresConfig{Nodes: []PostgresNode{{Host: "a"}, {Host: "b"}}}, 2},
		{"defaults_to_one", PostgresConfig{}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.EffectiveReplicationFactor(); got != tt.want {
				t.Fatalf("EffectiveReplicationFactor() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestClickHouseConfigHelpers(t *testing.T) {
	// Nodes intentionally out of ID order to prove CoordinatorHost picks the
	// lowest positive ID, not the first list entry.
	cluster := &ClickHouseConfig{
		Port:  9000,
		Nodes: []ClickHouseNode{{Host: "ch2", ID: 2}, {Host: "ch1", ID: 1}},
	}

	t.Run("EffectivePort_default", func(t *testing.T) {
		if got := (&ClickHouseConfig{}).EffectivePort(); got != 9000 {
			t.Fatalf("EffectivePort() = %d, want 9000", got)
		}
		if got := (&ClickHouseConfig{Port: 9100}).EffectivePort(); got != 9100 {
			t.Fatalf("EffectivePort() = %d, want 9100", got)
		}
	})

	t.Run("AllHosts_nodes_only", func(t *testing.T) {
		got := cluster.AllHosts()
		if len(got) != 2 || got[0] != "ch2" || got[1] != "ch1" {
			t.Fatalf("AllHosts() = %v, want [ch2 ch1]", got)
		}
		if got := (&ClickHouseConfig{}).AllHosts(); len(got) != 0 {
			t.Fatalf("AllHosts() = %v, want []", got)
		}
	})

	t.Run("AllAddrs_applies_cluster_port_and_resolve", func(t *testing.T) {
		got := cluster.AllAddrs(func(h string) string { return h + ".internal" })
		want := []string{"ch2.internal:9000", "ch1.internal:9000"}
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("AllAddrs() = %v, want %v", got, want)
		}
		// nil resolve passes host through untouched.
		if got := (&ClickHouseConfig{Port: 9000, Nodes: []ClickHouseNode{{Host: "solo", ID: 1}}}).AllAddrs(nil); len(got) != 1 || got[0] != "solo:9000" {
			t.Fatalf("AllAddrs(nil) = %v, want [solo:9000]", got)
		}
	})

	t.Run("CoordinatorHost_lowest_positive_id", func(t *testing.T) {
		if got := cluster.CoordinatorHost(); got != "ch1" {
			t.Fatalf("CoordinatorHost() = %q, want ch1 (lowest id, not first entry)", got)
		}
		if got := (&ClickHouseConfig{}).CoordinatorHost(); got != "" {
			t.Fatalf("CoordinatorHost() = %q, want empty", got)
		}
	})

	t.Run("HasHost", func(t *testing.T) {
		if !cluster.HasHost("ch2") {
			t.Error("HasHost(ch2) = false, want true")
		}
		if cluster.HasHost("nope") {
			t.Error("HasHost(nope) = true, want false")
		}
	})

	t.Run("EndpointFor_read_write_overrides", func(t *testing.T) {
		ch := &ClickHouseConfig{ReadEndpoint: "old-ro", WriteEndpoint: "old-rw"}
		if got := ch.EndpointFor("periscope-query"); got != "old-ro" {
			t.Fatalf("EndpointFor(query) = %q, want old-ro", got)
		}
		if got := ch.EndpointFor("periscope-ingest"); got != "old-rw" {
			t.Fatalf("EndpointFor(ingest) = %q, want old-rw", got)
		}
		// Unset endpoints and unrelated services fall back to "" (→ Nodes).
		if got := ch.EndpointFor("some-other-service"); got != "" {
			t.Fatalf("EndpointFor(other) = %q, want empty", got)
		}
		if got := (&ClickHouseConfig{}).EndpointFor("periscope-query"); got != "" {
			t.Fatalf("EndpointFor(query) with no override = %q, want empty", got)
		}
	})
}

func TestManifestSharedEnvFiles(t *testing.T) {
	t.Run("nil_manifest", func(t *testing.T) {
		var m *Manifest
		if got := m.SharedEnvFiles(); got != nil {
			t.Fatalf("SharedEnvFiles() = %v, want nil", got)
		}
	})
	t.Run("filters_blank_entries", func(t *testing.T) {
		m := &Manifest{EnvFiles: []string{"a.env", "  ", "", "b.env"}}
		got := m.SharedEnvFiles()
		if len(got) != 2 || got[0] != "a.env" || got[1] != "b.env" {
			t.Fatalf("SharedEnvFiles() = %v, want [a.env b.env]", got)
		}
	})
}

func TestEdgeNodeResolvedCluster(t *testing.T) {
	t.Run("per_node_cluster_wins", func(t *testing.T) {
		n := EdgeNode{Cluster: "edge-ams"}
		if got := n.ResolvedCluster("manifest-default"); got != "edge-ams" {
			t.Fatalf("ResolvedCluster() = %q, want edge-ams", got)
		}
	})
	t.Run("falls_back_to_manifest_cluster", func(t *testing.T) {
		if got := (EdgeNode{}).ResolvedCluster("manifest-default"); got != "manifest-default" {
			t.Fatalf("ResolvedCluster() = %q, want manifest-default", got)
		}
	})
}
