package cmd

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/ssh"
)

func regionalSQLManifest() *inventory.Manifest {
	return &inventory.Manifest{
		Profile: "dev", Hosts: map[string]inventory.Host{
			"eu": {Name: "eu", ExternalIP: "192.0.2.1"},
			"us": {Name: "us", ExternalIP: "192.0.2.2"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{Enabled: true, Engine: "yugabyte", Nodes: []inventory.PostgresNode{{Host: "eu", ID: 1}}, Databases: []inventory.DatabaseConfig{{Name: "foghorn", Owner: "foghorn"}, {Name: "commodore", Owner: "commodore"}}},
			DatabaseDeployments: map[string]*inventory.PostgresConfig{
				"us-east": {Enabled: true, Engine: "yugabyte", Nodes: []inventory.PostgresNode{{Host: "us", ID: 1}}, Databases: []inventory.DatabaseConfig{{Name: "foghorn", Owner: "foghorn"}}},
			},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn-eu": {Enabled: true, Deploy: "foghorn", Cluster: "eu", Host: "eu"},
			"foghorn-us": {Enabled: true, Deploy: "foghorn", Cluster: "us", Host: "us", DatabaseDeployment: "us-east"},
			"commodore":  {Enabled: true, Host: "eu"},
		},
	}
}

func TestDatabaseRelocationCanKeepWritersStopped(t *testing.T) {
	if newClusterRestoreCmd().Flags().Lookup("leave-stopped") == nil {
		t.Fatal("relocation restore must support leaving writers stopped until their DSNs have been redeployed")
	}
}

func TestDatabaseRelocationNeverRestartsOldDSNs(t *testing.T) {
	for _, failStop := range []bool{false, true} {
		control := &recordingControl{}
		if failStop {
			control.failFor = "foghorn-us"
		}
		_ = stopSwapWithRestart(context.Background(), io.Discard, control, []string{"foghorn-us"}, false, func() error { return errors.New("restore interrupted") })
		for _, call := range control.calls {
			if strings.HasPrefix(call, "start ") {
				t.Fatal("cutover restarted a writer with its old DSN")
			}
		}
	}
}

func TestDatabaseDeploymentAliasesStayWithTheirOwner(t *testing.T) {
	m := regionalSQLManifest()
	if err := validateSQLDatabaseOwnership(m); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ deployment, want, absent string }{{"primary", "foghorn_eu", "foghorn_us"}, {"us-east", "foghorn_us", "foghorn_eu"}} {
		view, err := m.WithDatabaseDeployment(tc.deployment)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, db := range yugabyteSchemaDatabases(view.Infrastructure.Postgres.Databases, view) {
			if db.Name == tc.absent || db.Name == "foghorn" {
				t.Fatalf("%s owns unexpected database %s", tc.deployment, db.Name)
			}
			if db.Name == tc.want {
				found = true
				if db.SourceName != "foghorn" {
					t.Fatalf("lost baseline source: %+v", db)
				}
			}
		}
		if !found {
			t.Fatalf("%s missing %s", tc.deployment, tc.want)
		}
	}
	if m.Infrastructure.Postgres.Nodes[0].Host != "eu" {
		t.Fatal("scope mutated original manifest")
	}
}

func TestDatabaseDeploymentRejectsForeignServiceResolution(t *testing.T) {
	m := regionalSQLManifest()
	m.Services["commodore"] = inventory.ServiceConfig{Enabled: true, Host: "eu", DatabaseDeployment: "us-east"}
	view, err := m.WithDatabaseDeployment("primary")
	if err != nil {
		t.Fatal(err)
	}
	_, _, ok := declaredPostgresDatabaseForService(&orchestrator.Task{Type: "commodore", ServiceID: "commodore"}, view, nil)
	if ok {
		t.Fatal("maintenance resolved a service database in a deployment that does not own the service")
	}
}

func TestDatabaseDeploymentPostgresAliasesHaveMigrationFloors(t *testing.T) {
	m := regionalSQLManifest()
	view, err := m.WithDatabaseDeployment("us-east")
	if err != nil {
		t.Fatal(err)
	}
	view.Infrastructure.Postgres.Engine = "postgres"
	dbs := provisionOwningDatabases(view, view.Infrastructure.Postgres, "foghorn")
	if len(dbs) != 1 || dbs[0].Name != "foghorn_us" || dbs[0].SourceName != "foghorn" {
		t.Fatalf("Postgres alias lost its physical migration floor: %+v", dbs)
	}
}

func TestDatabaseDeploymentRuntimeAndProvisioningAgree(t *testing.T) {
	m := regionalSQLManifest()
	task := &orchestrator.Task{Type: "foghorn", ServiceID: "foghorn-us", Host: "us", ClusterID: "us"}
	env, err := buildServiceEnvVars(task, m, nil, "", "", map[string]string{"DATABASE_PASSWORD": "shared"}, map[string]map[string]string{"us": {"DATABASE_PASSWORD": "us-secret"}}, "native")
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := url.Parse(env["DATABASE_URL"])
	if err != nil {
		t.Fatal(err)
	}
	if dsn.Host != "us.internal:5433" || dsn.Path != "/foghorn_us" {
		t.Fatalf("wrong binding: host=%s database=%s", dsn.Host, dsn.Path)
	}
	if password, _ := dsn.User.Password(); password != "us-secret" {
		t.Fatal("lost cell password")
	}
	config, err := buildTaskConfig(&orchestrator.Task{Type: "yugabyte", ServiceID: "postgres", InstanceID: "1", Host: "us", Phase: orchestrator.PhaseInfrastructure}, m, nil, false, "", map[string]string{"DATABASE_PASSWORD": "shared", "DATABASE_RUNTIME_PASSWORD": "runtime"}, map[string]map[string]string{"us": {"DATABASE_PASSWORD": "us-secret"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(config.Metadata["master_addresses"].(string), "192.0.2.1") {
		t.Fatal("US node joined EU masters")
	}
	dbs := config.Metadata["databases"].([]map[string]string)
	if len(dbs) != 1 || dbs[0]["name"] != "foghorn_us" || dbs[0]["password"] != "us-secret" {
		t.Fatal("provisioned database/credentials differ from runtime binding")
	}
}

func TestDatabaseDeploymentRejectsStaleEUOverride(t *testing.T) {
	m := regionalSQLManifest()
	task := &orchestrator.Task{Type: "foghorn", ServiceID: "foghorn-us", Host: "us", ClusterID: "us"}
	_, err := buildServiceEnvVars(task, m, nil, "", "", map[string]string{"DATABASE_URL": "postgres://foghorn_us@eu.internal:5433/foghorn_us"}, nil, "native")
	if err == nil || !strings.Contains(err.Error(), "conflicts with database deployment") {
		t.Fatalf("stale EU URL must fail: %v", err)
	}
}

func TestDatabaseDeploymentRejectsDuplicateHAContacts(t *testing.T) {
	m := regionalSQLManifest()
	pg := m.Infrastructure.DatabaseDeployments["us-east"]
	pg.Nodes = append(pg.Nodes, inventory.PostgresNode{Host: "us-2", ID: 2}, inventory.PostgresNode{Host: "us-3", ID: 3})
	m.Hosts["us-2"] = inventory.Host{}
	m.Hosts["us-3"] = inventory.Host{}
	view, err := m.WithDatabaseDeployment("us-east")
	if err != nil {
		t.Fatal(err)
	}
	err = validateServiceDatabaseDeploymentEnv(&orchestrator.Task{Type: "foghorn", ServiceID: "foghorn-us"}, view, map[string]string{"DATABASE_URL": "postgres://foghorn_us@us.internal:5433,us.internal:5433,us.internal:5433/foghorn_us"})
	if err == nil {
		t.Fatal("three copies of one endpoint passed the HA contact list check")
	}
}

func TestDatabaseDeploymentBackupTargetsReachBothRegions(t *testing.T) {
	m := regionalSQLManifest()
	old := backupPostgresHostFn
	t.Cleanup(func() { backupPostgresHostFn = old })
	backupPostgresHostFn = func(_ context.Context, manifest *inventory.Manifest, pg *inventory.PostgresConfig, _ *ssh.Pool) (inventory.Host, error) {
		return manifest.Hosts[pg.Nodes[0].Host], nil
	}
	plan, err := planBackupTargets(context.Background(), &resolvedCluster{Manifest: m}, nil, backupSelection{all: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.databases) != 3 {
		t.Fatalf("backup covers %d databases, want 3", len(plan.databases))
	}
	for _, db := range plan.databases {
		want := "eu"
		if db.entry.Name == "foghorn_us" {
			want = "us"
		}
		if db.host.Name != want {
			t.Fatalf("%s backed up from %s, want %s", db.entry.Name, db.host.Name, want)
		}
	}
}

func TestDatabaseDeploymentProvisionPlanIsIndependent(t *testing.T) {
	m := regionalSQLManifest()
	m.Services["privateer"] = inventory.ServiceConfig{Enabled: true}
	m.Infrastructure.ClickHouse = &inventory.ClickHouseConfig{Enabled: true, Nodes: []inventory.ClickHouseNode{{Host: "eu", ID: 1}}}
	view, err := m.WithDatabaseDeployment("us-east")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := orchestrator.NewPlanner(view).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseInfrastructure})
	if err != nil {
		t.Fatal(err)
	}
	seenDB := false
	for _, task := range plan.AllTasks {
		if task.Host != "us" || (task.Type != "privateer" && task.Type != "yugabyte") {
			t.Fatalf("US-only infrastructure plan touches foreign task %+v", task)
		}
		seenDB = seenDB || task.Type == "yugabyte"
	}
	if !seenDB {
		t.Fatal("US plan has no database")
	}
}

func TestDatabaseDeploymentRPCAndPlacementReachRole(t *testing.T) {
	m := regionalSQLManifest()
	pg := m.Infrastructure.DatabaseDeployments["us-east"]
	pg.PlacementCloud, pg.PlacementRegion = "hetzner", "us-east"
	pg.Nodes[0].PlacementZone, pg.Nodes[0].RpcPort = "ash-dc1", 7101
	cfg, err := buildTaskConfig(&orchestrator.Task{Type: "yugabyte", ServiceID: "postgres", InstanceID: "1", Host: "us", Phase: orchestrator.PhaseInfrastructure}, m, nil, false, "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Metadata["master_rpc_port"] != 7101 || cfg.Metadata["placement_zone"] != "ash-dc1" || cfg.Metadata["placement_region"] != "us-east" {
		t.Fatalf("lost declared placement or port: %+v", cfg.Metadata)
	}
}

func TestPostgresDeploymentInitializesBeforeRemainingYugabyteBatches(t *testing.T) {
	m := regionalSQLManifest()
	m.Infrastructure.DatabaseDeployments["us-east"] = &inventory.PostgresConfig{Enabled: true, Host: "us"}
	batch := []*orchestrator.Task{{Type: "postgres", ServiceID: "postgres", Host: "us"}}
	remaining := [][]*orchestrator.Task{{{Type: "yugabyte", ServiceID: "postgres", Host: "eu"}}}
	if !shouldInitializePrimaryPostgresAfterBatch(m, batch, remaining) {
		t.Fatal("PostgreSQL initialization is lost when the last SQL batch is Yugabyte")
	}
}

func TestDatabaseDeploymentRejectsURLRoutingOverrides(t *testing.T) {
	m := regionalSQLManifest()
	view, err := m.WithDatabaseDeployment("us-east")
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"host=eu.internal", "port=5432", "dbname=foghorn_eu"} {
		t.Run(query, func(t *testing.T) {
			err := validateServiceDatabaseDeploymentEnv(&orchestrator.Task{Type: "foghorn", ServiceID: "foghorn-us"}, view, map[string]string{"DATABASE_URL": "postgres://foghorn_us@us.internal:5433/foghorn_us?" + query})
			if err == nil {
				t.Fatal("URL query parameter bypassed regional database binding")
			}
		})
	}
}

func TestRegionalLedgerDoctorSkipsForeignPurser(t *testing.T) {
	m := regionalSQLManifest()
	m.Services["purser"] = inventory.ServiceConfig{Enabled: true, Host: "eu"}
	view, err := m.WithDatabaseDeployment("us-east")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, found := purserServiceFor(view); found {
		t.Fatal("regional doctor resolved Purser outside its deployment")
	}
}
