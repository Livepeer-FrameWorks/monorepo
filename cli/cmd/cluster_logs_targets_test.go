package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"frameworks/cli/pkg/inventory"
)

// logTargetsTestManifest builds a manifest exercising every routing branch of
// resolveLogTargets: enabled infra (postgres/kafka/clickhouse), an enabled
// multi-host service, a disabled service, and a service pointing at a host that
// is absent from the Hosts map.
func logTargetsTestManifest() *inventory.Manifest {
	return &inventory.Manifest{
		Type: "cluster",
		Hosts: map[string]inventory.Host{
			"pg1": {},
			"k1":  {},
			"ch1": {},
			"f1":  {},
			"f2":  {},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres:   &inventory.PostgresConfig{Enabled: true, Host: "pg1"},
			Kafka:      &inventory.KafkaConfig{Enabled: true, Brokers: []inventory.KafkaBroker{{Host: "k1", ID: 1}}},
			ClickHouse: &inventory.ClickHouseConfig{Enabled: true, Nodes: []inventory.ClickHouseNode{{Host: "ch1", ID: 1}}},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn": {Enabled: true, Hosts: []string{"f1", "f2"}},
			"bridge":  {Enabled: false, Host: "f1"},
			"decklog": {Enabled: true, Hosts: []string{"ghost"}}, // ghost not in Hosts
		},
	}
}

func TestResolveLogTargets_InfrastructureRouting(t *testing.T) {
	m := logTargetsTestManifest()
	cases := []struct {
		service    string
		wantHosts  []string
		wantDeploy string
	}{
		{"postgres", []string{"pg1"}, "postgres"},
		{"kafka", []string{"k1"}, "kafka"},
		{"clickhouse", []string{"ch1"}, "clickhouse"},
	}
	for _, tc := range cases {
		t.Run(tc.service, func(t *testing.T) {
			targets, err := resolveLogTargets(m, tc.service)
			if err != nil {
				t.Fatalf("resolveLogTargets(%s) error: %v", tc.service, err)
			}
			if len(targets) != len(tc.wantHosts) {
				t.Fatalf("targets = %d, want %d", len(targets), len(tc.wantHosts))
			}
			for i, want := range tc.wantHosts {
				if targets[i].HostName != want {
					t.Errorf("target[%d].HostName = %q, want %q", i, targets[i].HostName, want)
				}
				if targets[i].DeployName != tc.wantDeploy {
					t.Errorf("target[%d].DeployName = %q, want %q", i, targets[i].DeployName, tc.wantDeploy)
				}
			}
		})
	}
}

func TestResolveLogTargets_DisabledInfraFallsThrough(t *testing.T) {
	// Postgres present but disabled — the infra branch must NOT fire; the name
	// is then looked up as a service and (being absent) yields a not-found error.
	m := logTargetsTestManifest()
	m.Infrastructure.Postgres.Enabled = false
	if _, err := resolveLogTargets(m, "postgres"); err == nil {
		t.Fatal("expected error when postgres is disabled and not a known service")
	}
}

func TestResolveLogTargets_EnabledServiceMultiHost(t *testing.T) {
	m := logTargetsTestManifest()
	targets, err := resolveLogTargets(m, "foghorn")
	if err != nil {
		t.Fatalf("resolveLogTargets(foghorn) error: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(targets))
	}
	for _, tg := range targets {
		if tg.DeployName != "foghorn" {
			t.Errorf("DeployName = %q, want foghorn", tg.DeployName)
		}
		// hostLogTargets fills Host.Name from the key when unset.
		if tg.Host.Name != tg.HostName {
			t.Errorf("Host.Name = %q, want %q (fallback to key)", tg.Host.Name, tg.HostName)
		}
	}
}

func TestResolveLogTargets_DisabledServiceErrors(t *testing.T) {
	m := logTargetsTestManifest()
	if _, err := resolveLogTargets(m, "bridge"); err == nil {
		t.Fatal("expected error for disabled service bridge")
	}
}

func TestResolveLogTargets_UnknownHostErrors(t *testing.T) {
	m := logTargetsTestManifest()
	if _, err := resolveLogTargets(m, "decklog"); err == nil {
		t.Fatal("expected error: decklog references unknown host ghost")
	}
}

func TestResolveLogTargets_UnknownServiceErrors(t *testing.T) {
	m := logTargetsTestManifest()
	if _, err := resolveLogTargets(m, "does-not-exist"); err == nil {
		t.Fatal("expected not-found error for unknown service")
	}
}

func TestResolveLogTargets_NilManifest(t *testing.T) {
	if _, err := resolveLogTargets(nil, "postgres"); err == nil {
		t.Fatal("expected error for nil manifest")
	}
}

// serviceLogTargets' bool return is the fall-through contract: false means
// "name absent here, keep looking"; true means "this is the owner" (even when
// returning an error). All three states must be distinguishable.
func TestServiceLogTargets_FallThroughContract(t *testing.T) {
	m := logTargetsTestManifest()

	if targets, ok, err := serviceLogTargets(m, "ghostsvc", m.Services); ok || err != nil || targets != nil {
		t.Errorf("absent: got (targets=%v, ok=%v, err=%v), want (nil, false, nil)", targets, ok, err)
	}
	if _, ok, err := serviceLogTargets(m, "bridge", m.Services); !ok || err == nil {
		t.Errorf("disabled: got (ok=%v, err=%v), want (true, non-nil)", ok, err)
	}
	if targets, ok, err := serviceLogTargets(m, "foghorn", m.Services); !ok || err != nil || len(targets) != 2 {
		t.Errorf("enabled: got (len=%d, ok=%v, err=%v), want (2, true, nil)", len(targets), ok, err)
	}
}

func TestInfrastructureLogTargets_DedupesHosts(t *testing.T) {
	m := logTargetsTestManifest()
	targets, err := infrastructureLogTargets(m, "postgres", "postgres", []string{"pg1", "pg1"})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1 (deduped)", len(targets))
	}
}

func TestInfrastructureLogTargets_EmptyHostsErrors(t *testing.T) {
	m := logTargetsTestManifest()
	if _, err := infrastructureLogTargets(m, "postgres", "postgres", nil); err == nil {
		t.Fatal("expected error for empty host set")
	}
}

func TestDefaultEdgeManifestPath(t *testing.T) {
	dir := t.TempDir()
	clusterPath := filepath.Join(dir, "cluster.yaml")

	// No sibling edge.yaml yet → empty.
	if got := defaultEdgeManifestPath(clusterPath); got != "" {
		t.Errorf("without edge.yaml: got %q, want \"\"", got)
	}

	// Empty input → empty.
	if got := defaultEdgeManifestPath(""); got != "" {
		t.Errorf("empty input: got %q, want \"\"", got)
	}

	// Sibling edge.yaml present → its path.
	edgePath := filepath.Join(dir, "edge.yaml")
	if err := os.WriteFile(edgePath, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatalf("write edge.yaml: %v", err)
	}
	if got := defaultEdgeManifestPath(clusterPath); got != edgePath {
		t.Errorf("with edge.yaml: got %q, want %q", got, edgePath)
	}
}
