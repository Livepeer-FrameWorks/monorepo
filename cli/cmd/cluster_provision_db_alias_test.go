package cmd

import (
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
)

// productionYugabyteManifest mirrors the production `infrastructure.postgres` shape: a native-mode
// YugabyteDB cluster whose databases are declared at the TOP LEVEL (pg.Databases), NOT inside an
// instances[] entry. `foghorn` is a LOGICAL database that provisioning expands to one physical
// per-cell database+owner per clustered Foghorn deployment (foghorn-eu → foghorn_eu, foghorn-us →
// foghorn_us via clusterScopedDatabaseAliases). Un-clustered top-level services (quartermaster,
// commodore) keep their logical name because expansion is a no-op for them.
func productionYugabyteManifest() *inventory.Manifest {
	return &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"yuga-eu-1": {WireguardIP: "10.88.0.11"},
			"yuga-eu-2": {WireguardIP: "10.88.0.12"},
			"yuga-eu-3": {WireguardIP: "10.88.0.13"},
			"media-eu":  {WireguardIP: "10.88.0.20"},
			"media-us":  {WireguardIP: "10.88.0.30"},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"media-eu-1": {Name: "Media EU"},
			"media-us-1": {Name: "Media US"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled: true,
				Engine:  "yugabyte",
				Mode:    "native",
				Port:    5433,
				Nodes: []inventory.PostgresNode{
					{Host: "yuga-eu-1", ID: 1},
					{Host: "yuga-eu-2", ID: 2},
					{Host: "yuga-eu-3", ID: 3},
				},
				Databases: []inventory.DatabaseConfig{
					{Name: "quartermaster", Owner: "quartermaster"},
					{Name: "commodore", Owner: "commodore"},
					{Name: "foghorn", Owner: "foghorn"},
				},
			},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn-eu": {Enabled: true, Deploy: "foghorn", Host: "media-eu", Cluster: "media-eu-1"},
			"foghorn-us": {Enabled: true, Deploy: "foghorn", Host: "media-us", Cluster: "media-us-1"},
		},
	}
}

// A clustered Foghorn (foghorn-eu / foghorn-us, deploy: foghorn) must render its DSN against the
// PER-CELL physical database that provisioning actually created (foghorn_eu / foghorn_us), never the
// logical `foghorn`, which is never created when the logical name is expanded. Rendering `foghorn`
// would boot Foghorn against a nonexistent database+user and fail to connect. The physical host must
// resolve from the Yugabyte nodes, since a top-level database has no instances[] entry to key off.
func TestBuildServiceEnvVars_TopLevelYugabyteFoghornResolvesPerCellDatabase(t *testing.T) {
	manifest := productionYugabyteManifest()
	clusterEnvs := map[string]map[string]string{
		"media-eu-1": {"DATABASE_PASSWORD": "eu-cell-secret"},
		"media-us-1": {"DATABASE_PASSWORD": "us-cell-secret"},
	}

	cases := []struct {
		serviceID string
		host      string
		cluster   string
		wantDB    string
		wantHost  string
		wantPass  string
	}{
		{"foghorn-eu", "media-eu", "media-eu-1", "foghorn_eu", "yuga-eu-1.internal", "eu-cell-secret"},
		{"foghorn-us", "media-us", "media-us-1", "foghorn_us", "yuga-eu-1.internal", "us-cell-secret"},
	}

	for _, tc := range cases {
		task := &orchestrator.Task{
			Name:      tc.serviceID,
			Type:      "foghorn",
			ServiceID: tc.serviceID,
			Host:      tc.host,
			ClusterID: tc.cluster,
			Phase:     orchestrator.PhaseApplications,
		}
		env, err := buildServiceEnvVars(task, manifest, map[string]any{}, "", "", map[string]string{}, clusterEnvs, "native")
		if err != nil {
			t.Fatalf("%s: buildServiceEnvVars: %v", tc.serviceID, err)
		}
		if got := env["DATABASE_NAME"]; got != tc.wantDB {
			t.Errorf("%s: DATABASE_NAME = %q, want %q (must not be the logical `foghorn`)", tc.serviceID, got, tc.wantDB)
		}
		if got := env["DATABASE_USER"]; got != tc.wantDB {
			t.Errorf("%s: DATABASE_USER = %q, want %q (alias owner == alias name)", tc.serviceID, got, tc.wantDB)
		}
		if got := env["DATABASE_HOST"]; got == "" || got != tc.wantHost {
			t.Errorf("%s: DATABASE_HOST = %q, want %q (a real Yugabyte node)", tc.serviceID, got, tc.wantHost)
		}
		if got := env["DATABASE_PASSWORD"]; got != tc.wantPass {
			t.Errorf("%s: DATABASE_PASSWORD = %q, want %q", tc.serviceID, got, tc.wantPass)
		}
		wantDSN := tc.wantDB + ":" + tc.wantPass + "@"
		if !strings.Contains(env["DATABASE_URL"], wantDSN) || !strings.HasSuffix(strings.SplitN(env["DATABASE_URL"], "?", 2)[0], "/"+tc.wantDB) {
			t.Errorf("%s: DATABASE_URL = %q, want per-cell db %q with cell password", tc.serviceID, env["DATABASE_URL"], tc.wantDB)
		}
	}
}

// A NON-clustered top-level Yugabyte service (quartermaster) keeps its logical database name because
// clusterScopedDatabaseAliases expands nothing for it. This guards against the per-cell resolution
// accidentally renaming the services that are supposed to share the logical name.
func TestBuildServiceEnvVars_TopLevelYugabyteUnclusteredKeepsLogicalDatabase(t *testing.T) {
	manifest := productionYugabyteManifest()
	manifest.Services["quartermaster"] = inventory.ServiceConfig{Enabled: true, Host: "yuga-eu-1"}

	task := &orchestrator.Task{
		Name:      "quartermaster",
		Type:      "quartermaster",
		ServiceID: "quartermaster",
		Host:      "yuga-eu-1",
		Phase:     orchestrator.PhaseApplications,
	}
	env, err := buildServiceEnvVars(task, manifest, map[string]any{}, "", "", map[string]string{}, nil, "native")
	if err != nil {
		t.Fatalf("buildServiceEnvVars: %v", err)
	}
	if got := env["DATABASE_NAME"]; got != "quartermaster" {
		t.Errorf("DATABASE_NAME = %q, want quartermaster", got)
	}
	if got := env["DATABASE_USER"]; got != "quartermaster" {
		t.Errorf("DATABASE_USER = %q, want quartermaster", got)
	}
	if got := env["DATABASE_HOST"]; got == "" {
		t.Errorf("DATABASE_HOST empty, want a Yugabyte node")
	}
}

// The instance-scoped shape (a database declared inside pg.Instances[]) must keep resolving from that
// instance's host/credentials. This is the other supported production shape (the `support` instance:
// chatwoot/listmonk/metabase) and must not regress when top-level resolution is added.
func TestBuildServiceEnvVars_InstanceScopedDatabaseStillResolves(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"support-1": {WireguardIP: "10.88.0.40"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled: true,
				Instances: []inventory.PostgresInstance{{
					Name:     "support",
					Host:     "support-1",
					Port:     5432,
					Password: "instance-secret",
					Databases: []inventory.DatabaseConfig{
						{Name: "metabase", Owner: "metabase"},
					},
				}},
			},
		},
		Services: map[string]inventory.ServiceConfig{
			"metabase": {Enabled: true, Mode: "docker", Host: "core1"},
		},
	}

	task := &orchestrator.Task{
		Name:      "metabase",
		Type:      "metabase",
		ServiceID: "metabase",
		Host:      "core1",
		Phase:     orchestrator.PhaseApplications,
	}
	env, err := buildServiceEnvVars(task, manifest, map[string]any{}, "", "", map[string]string{}, nil, "native")
	if err != nil {
		t.Fatalf("buildServiceEnvVars: %v", err)
	}
	if got := env["DATABASE_NAME"]; got != "metabase" {
		t.Errorf("DATABASE_NAME = %q, want metabase", got)
	}
	if got := env["DATABASE_USER"]; got != "metabase" {
		t.Errorf("DATABASE_USER = %q, want metabase", got)
	}
	if got := env["DATABASE_HOST"]; got != "support-1.internal" {
		t.Errorf("DATABASE_HOST = %q, want support-1.internal", got)
	}
	if got := env["DATABASE_PASSWORD"]; got != "instance-secret" {
		t.Errorf("DATABASE_PASSWORD = %q, want instance-secret", got)
	}
}
