package cmd

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	fwv "github.com/Livepeer-FrameWorks/monorepo/pkg/version"
)

const newDatabaseReleaseTarget = "v0.3.13"

// newDatabaseReleaseManifest declares quartermaster (present on the cluster) and
// network_observer (introduced by the release), plus a ClickHouse analytics
// store that neither service depends on.
func newDatabaseReleaseManifest() *inventory.Manifest {
	return &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"db-1": {ExternalIP: "192.0.2.10", User: "root"},
			"ch-1": {ExternalIP: "192.0.2.20", User: "root"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled:  true,
				Host:     "db-1",
				Password: "test",
				Databases: []inventory.DatabaseConfig{
					{Name: "quartermaster"},
					{Name: "network_observer"},
				},
			},
			ClickHouse: &inventory.ClickHouseConfig{
				Enabled:   true,
				Nodes:     []inventory.ClickHouseNode{{Host: "ch-1", ID: 1}},
				Databases: []string{"periscope"},
			},
		},
	}
}

// ledgerReads records every database whose migration ledger, baseline marker,
// or existence probe is read, so a test can prove a database is never
// connected to.
type ledgerReads struct {
	probed    []string
	floorPG   []string
	floorCH   []string
	missingPG []string
	missingCH []string
	existing  map[string]bool
	// pgHosts records each Postgres-family read as "<kind>@<host address>".
	pgHosts    []string
	floorCalls int
}

func (r *ledgerReads) install(t *testing.T) {
	t.Helper()
	origProbe, origLimits := readServiceDatabaseStatesFn, ensureRuntimeRoleSessionLimitsFn
	origFloorPG, origFloorCH := postgresBelowFloorGapFn, clickHouseBelowFloorGapFn
	origMissingPG, origMissingCH := missingPostgresMigrationsFn, missingClickHouseMigrationsFn
	origVer := fwv.Version
	t.Cleanup(func() {
		readServiceDatabaseStatesFn, ensureRuntimeRoleSessionLimitsFn = origProbe, origLimits
		postgresBelowFloorGapFn, clickHouseBelowFloorGapFn = origFloorPG, origFloorCH
		missingPostgresMigrationsFn, missingClickHouseMigrationsFn = origMissingPG, origMissingCH
		fwv.Version = origVer
	})
	fwv.Version = newDatabaseReleaseTarget
	readServiceDatabaseStatesFn = func(_ context.Context, _ *ssh.Pool, host inventory.Host, _ *inventory.PostgresConfig, databases []provisioner.SchemaDatabase) (map[string]provisioner.ServiceDatabaseState, error) {
		r.pgHosts = append(r.pgHosts, "probe@"+host.WireguardIP+host.ExternalIP)
		states := map[string]provisioner.ServiceDatabaseState{}
		for _, database := range databases {
			r.probed = append(r.probed, database.Name)
			states[database.Name] = provisioner.ServiceDatabaseState{}
			if r.existing[database.Name] {
				states[database.Name] = provisioner.ServiceDatabaseState{Exists: true, HasTables: true, Initialized: true}
			}
		}
		return states, nil
	}
	ensureRuntimeRoleSessionLimitsFn = func(context.Context, *ssh.Pool, inventory.Host, *inventory.PostgresConfig, []string, bool) ([]string, error) {
		return nil, nil
	}
	postgresBelowFloorGapFn = func(_ context.Context, _ *ssh.Pool, host inventory.Host, _ *inventory.PostgresConfig, _ string, databases []provisioner.SchemaDatabase) ([]provisioner.MigrationKey, error) {
		r.pgHosts = append(r.pgHosts, "floor@"+host.WireguardIP+host.ExternalIP)
		r.floorCalls++
		r.floorPG = append(r.floorPG, schemaDatabaseNameList(databases)...)
		return nil, nil
	}
	clickHouseBelowFloorGapFn = func(_ context.Context, _ *ssh.Pool, _ inventory.Host, _ int, _ string, databases []string) ([]provisioner.MigrationKey, error) {
		r.floorCH = append(r.floorCH, databases...)
		return nil, nil
	}
	missingPostgresMigrationsFn = func(_ context.Context, _ *ssh.Pool, host inventory.Host, _ *inventory.PostgresConfig, _ string, databases []provisioner.SchemaDatabase, phase, _ string) ([]provisioner.MigrationKey, error) {
		r.pgHosts = append(r.pgHosts, phase+"@"+host.WireguardIP+host.ExternalIP)
		r.missingPG = append(r.missingPG, schemaDatabaseNameList(databases)...)
		return nil, nil
	}
	missingClickHouseMigrationsFn = func(_ context.Context, _ *ssh.Pool, _ inventory.Host, _ int, _ string, databases []string, _, _ string) ([]provisioner.MigrationKey, error) {
		r.missingCH = append(r.missingCH, databases...)
		return nil, nil
	}
}

func (r *ledgerReads) reset() {
	r.probed, r.floorPG, r.floorCH, r.missingPG, r.missingCH, r.floorCalls = nil, nil, nil, nil, nil, 0
}

func (r *ledgerReads) read(database string) bool {
	for _, names := range [][]string{r.probed, r.floorPG, r.floorCH, r.missingPG, r.missingCH} {
		for _, name := range names {
			if name == database {
				return true
			}
		}
	}
	return false
}

// A dry-run release that introduces network_observer runs the expand step
// (which only plans the database), then the pre-deploy gate of the service
// that owns it, then the postdeploy step. None of them may connect to the
// planned database, and the gate reports it as created at the current baseline.
// The manifest declares only the new database so the dry-run migration
// branches have no ansible check to run.
func TestReleaseDryRunSkipsDatabaseTheExpandStepWouldCreate(t *testing.T) {
	reads := &ledgerReads{existing: map[string]bool{"quartermaster": true}}
	reads.install(t)
	manifest := newDatabaseReleaseManifest()
	manifest.Infrastructure.Postgres.Databases = []inventory.DatabaseConfig{{Name: "network_observer"}}
	manifest.Infrastructure.ClickHouse = nil
	rc := &resolvedCluster{Manifest: manifest}
	cmd, out := commandWithOutput()

	if err := runMigrate(cmd, rc, true, "expand", true, newDatabaseReleaseTarget, true, false); err != nil {
		t.Fatalf("dry-run expand: %v", err)
	}
	if !strings.Contains(out.String(), "network_observer (owner network_observer") {
		t.Fatalf("dry-run expand did not list network_observer as created:\n%s", out.String())
	}
	if !reflect.DeepEqual(reads.probed, []string{"network_observer"}) || reads.floorCalls != 0 {
		t.Fatalf("expand probed %v and ran %d floor read(s); only the existence probe may touch a planned database", reads.probed, reads.floorCalls)
	}
	reads.reset()

	// Quartermaster upgrades first, as in the staging release that hit this.
	if err := runUpgradePreDeployGate(context.Background(), cmd, rc, nil, rc.Manifest, newDatabaseReleaseTarget, "quartermaster", "quartermaster", false, true); err != nil {
		t.Fatalf("dry-run gate for quartermaster: %v", err)
	}
	if reads.read("network_observer") {
		t.Fatalf("quartermaster's dry-run gate read network_observer (floor %v); it does not exist yet", reads.floorPG)
	}
	out.Reset()
	if err := runUpgradePreDeployGate(context.Background(), cmd, rc, nil, rc.Manifest, newDatabaseReleaseTarget, "network-observer", "network-observer", false, true); err != nil {
		t.Fatalf("dry-run gate for network-observer: %v", err)
	}
	if !strings.Contains(out.String(), "network_observer would be created at the current baseline") {
		t.Fatalf("network-observer gate did not report its planned database:\n%s", out.String())
	}
	if reads.read("network_observer") {
		t.Fatalf("dry-run gates read network_observer (probed %v floor %v ledger %v); it does not exist yet", reads.probed, reads.floorPG, reads.missingPG)
	}
	reads.reset()

	if err := runMigrate(cmd, rc, true, "postdeploy", true, newDatabaseReleaseTarget, true, false); err != nil {
		t.Fatalf("dry-run postdeploy: %v", err)
	}
	if reads.read("network_observer") || reads.floorCalls != 0 {
		t.Fatalf("dry-run postdeploy read network_observer (floor %v); it does not exist yet", reads.floorPG)
	}
}

// A real release has created the database before the upgrades, so every gate
// checks its own existing database, including the one the release introduced.
func TestReleaseApplyGateChecksIntroducedDatabaseAfterCreation(t *testing.T) {
	reads := &ledgerReads{existing: map[string]bool{"quartermaster": true, "network_observer": true}}
	reads.install(t)
	rc := &resolvedCluster{Manifest: newDatabaseReleaseManifest()}
	cmd, _ := commandWithOutput()

	if err := runUpgradePreDeployGate(context.Background(), cmd, rc, nil, rc.Manifest, newDatabaseReleaseTarget, "network-observer", "network-observer", false, true); err != nil {
		t.Fatalf("gate: %v", err)
	}
	if !reflect.DeepEqual(reads.probed, []string{"network_observer"}) {
		t.Fatalf("probed %v, want network_observer's existence checked", reads.probed)
	}
	if !reflect.DeepEqual(reads.floorPG, []string{"network_observer"}) {
		t.Fatalf("floor guard read %v, want network_observer", reads.floorPG)
	}
}

// The pre-deploy gate guards one service's binary: its floor check covers the
// databases that service's expand and postdeploy checks read, not every
// database in the manifest on every service upgrade.
func TestUpgradeGateFloorCheckCoversOnlyTheServiceDatabases(t *testing.T) {
	reads := &ledgerReads{existing: map[string]bool{"quartermaster": true, "network_observer": true}}
	reads.install(t)
	rc := &resolvedCluster{Manifest: newDatabaseReleaseManifest()}
	cmd, _ := commandWithOutput()

	if err := runUpgradePreDeployGate(context.Background(), cmd, rc, nil, rc.Manifest, newDatabaseReleaseTarget, "quartermaster", "quartermaster", false, true); err != nil {
		t.Fatalf("gate: %v", err)
	}
	if !reflect.DeepEqual(reads.floorPG, []string{"quartermaster"}) || len(reads.floorCH) != 0 {
		t.Fatalf("quartermaster gate floor reads postgres=%v clickhouse=%v, want only quartermaster", reads.floorPG, reads.floorCH)
	}
}

// The gate's existence probe, ledger reads, and floor read share one host: the
// first Yugabyte tserver whose local YSQL answers, so a down first node does
// not block the gate.
func TestUpgradeGateReadsFromFirstHealthyYugabyteTserver(t *testing.T) {
	reads := &ledgerReads{existing: map[string]bool{"quartermaster": true}}
	reads.install(t)
	origProbe := yugabyteHostSelectionProbeFn
	t.Cleanup(func() { yugabyteHostSelectionProbeFn = origProbe })
	var probedNodes []string
	yugabyteHostSelectionProbeFn = func(_ context.Context, _ *ssh.Pool, host inventory.Host, _ *inventory.PostgresConfig) *health.CheckResult {
		probedNodes = append(probedNodes, host.WireguardIP)
		return &health.CheckResult{OK: host.WireguardIP != "10.88.0.11"}
	}
	manifest := productionYugabyteManifest()
	manifest.Infrastructure.Postgres.Password = "test"
	rc := &resolvedCluster{Manifest: manifest}
	cmd, _ := commandWithOutput()

	if err := runUpgradePreDeployGate(context.Background(), cmd, rc, nil, manifest, newDatabaseReleaseTarget, "quartermaster", "quartermaster", false, true); err != nil {
		t.Fatalf("gate with the first tserver down: %v", err)
	}
	if len(reads.pgHosts) == 0 {
		t.Fatal("gate read nothing")
	}
	for _, read := range reads.pgHosts {
		if !strings.HasSuffix(read, "@10.88.0.12") {
			t.Fatalf("gate reads %v, want every read on the first healthy tserver 10.88.0.12", reads.pgHosts)
		}
	}
	if !reflect.DeepEqual(probedNodes, []string{"10.88.0.11", "10.88.0.12"}) {
		t.Fatalf("health probes %v, want one host selection shared by every read", probedNodes)
	}

	yugabyteHostSelectionProbeFn = func(context.Context, *ssh.Pool, inventory.Host, *inventory.PostgresConfig) *health.CheckResult {
		return &health.CheckResult{}
	}
	reads.reset()
	reads.pgHosts = nil
	if err := runUpgradePreDeployGate(context.Background(), cmd, rc, nil, manifest, newDatabaseReleaseTarget, "quartermaster", "quartermaster", false, true); err == nil {
		t.Fatalf("gate passed with no healthy tserver (reads %v)", reads.pgHosts)
	}
}
