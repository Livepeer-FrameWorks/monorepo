package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
	fwv "github.com/Livepeer-FrameWorks/monorepo/pkg/version"

	"github.com/spf13/cobra"
)

// TestCatalogDatabaseOwnershipCoversEveryDatabaseBackedService is the fail-closed completeness check the runtime gate
// relies on: topology (InfraDatabase) is the authority for which services need a primary database, and every such
// service MUST have a catalog ownership entry so the upgrade gate runs its migration check instead of skipping. Any
// omission is a release blocker, caught at CI rather than at deploy time.
func TestCatalogDatabaseOwnershipCoversEveryDatabaseBackedService(t *testing.T) {
	for id := range servicedefs.Services {
		if !serviceIsDatabaseBacked(id) {
			continue
		}
		if _, ok := releases.ServiceDatabaseLookup(id); !ok {
			t.Errorf("service %q is database-backed per topology but has no release-catalog database ownership; add it to service_databases", id)
		}
	}
	// Sanity: the authority itself must classify at least the known control-plane DBs, else the loop is vacuous.
	for _, must := range []string{"commodore", "quartermaster", "navigator", "skipper"} {
		if !serviceIsDatabaseBacked(must) {
			t.Fatalf("topology no longer marks %q database-backed; the completeness check would silently pass", must)
		}
		if dep := topology.InfraDependencies(must); len(dep) == 0 {
			t.Fatalf("topology has no infra dependencies for %q", must)
		}
	}
}

// A corrupt embedded catalog must FAIL CLOSED at every gate that consults it —
// the bug this guards against is a parse failure being reinterpreted as an
// empty catalog, which silently disables the migration/data-migration gates.
// Each gate is reached on the corrupt path BEFORE it touches SSH/manifest state,
// so nil pools/manifests are safe here: the catalog error must short-circuit.

// withCorruptCatalog overrides the gate seam so the catalog reports a parse
// failure, then restores it. This is dependency injection at the consumer, not
// mutation of release-package state.
func withCorruptCatalog(t *testing.T) {
	t.Helper()
	orig := loadReleaseCatalog
	loadReleaseCatalog = func() ([]releases.Release, error) {
		return nil, errors.New("synthetic parse failure")
	}
	t.Cleanup(func() { loadReleaseCatalog = orig })
}

func TestUpgradePreDeployGate_FailsClosedOnCorruptCatalog(t *testing.T) {
	withCorruptCatalog(t)

	err := runUpgradePreDeployGate(context.Background(), &cobra.Command{}, nil, nil, nil,
		"v0.2.96", "foghorn", "foghorn", false, false)
	if err == nil {
		t.Fatal("a corrupt catalog must refuse the pre-deploy gate, not skip it")
	}
	if !strings.Contains(err.Error(), "release catalog failed to load") {
		t.Fatalf("error must name the catalog load failure, got: %v", err)
	}
}

func TestPhaseDataMigrationGate_FailsClosedOnCorruptCatalog(t *testing.T) {
	withCorruptCatalog(t)

	err := runPhaseDataMigrationGate(context.Background(), &cobra.Command{}, nil, nil,
		"contract", "v0.2.96", false)
	if err == nil {
		t.Fatal("a corrupt catalog must refuse the contract-phase gate, not proceed")
	}
	if !strings.Contains(err.Error(), "release catalog failed to load") {
		t.Fatalf("error must name the catalog load failure, got: %v", err)
	}
}

func TestDoctorDataMigrations_ReportsDegradedOnCorruptCatalog(t *testing.T) {
	withCorruptCatalog(t)

	result := doctorDataMigrations(context.Background(), nil, nil, "v0.2.96")
	if result.OK {
		t.Fatal("a corrupt catalog must report NOT ok (a green doctor would green-light an unverified deploy)")
	}
	if result.Status != "degraded" {
		t.Fatalf("status = %q, want degraded", result.Status)
	}
	if !strings.Contains(result.Error, "release catalog failed to load") {
		t.Fatalf("error must name the catalog load failure, got: %q", result.Error)
	}
}

// TestFormatMigrationRemediation pins the separated expand/postdeploy remediation:
// each phase contributes its own command only when it has missing migrations, and
// the postdeploy command targets the HIGHEST PRIOR release version, never the
// deploy target (whose own postdeploy runs after the deploy).
func TestFormatMigrationRemediation(t *testing.T) {
	expand := []provisioner.MigrationKey{{Database: "foghorn", Version: "v0.2.96", Phase: "expand", Seq: 1, Filename: "001_a.sql"}}
	postdeploy := []provisioner.MigrationKey{{Database: "foghorn", Version: "v0.2.90", Phase: "postdeploy", Seq: 1, Filename: "001_b.sql"}}

	t.Run("expand only", func(t *testing.T) {
		msg := formatMigrationRemediation("foghorn", "v0.2.96", "v0.2.90", expand, nil)
		if !strings.Contains(msg, "--phase expand --to-version v0.2.96") {
			t.Fatalf("expected expand remediation, got:\n%s", msg)
		}
		if strings.Contains(msg, "--phase postdeploy") {
			t.Fatalf("no postdeploy missing, must not suggest postdeploy:\n%s", msg)
		}
	})

	t.Run("postdeploy only targets prior version", func(t *testing.T) {
		msg := formatMigrationRemediation("foghorn", "v0.2.96", "v0.2.90", nil, postdeploy)
		if !strings.Contains(msg, "--phase postdeploy --to-version v0.2.90") {
			t.Fatalf("postdeploy remediation must target the prior release v0.2.90, got:\n%s", msg)
		}
		if strings.Contains(msg, "--phase postdeploy --to-version v0.2.96") {
			t.Fatalf("postdeploy must NOT target the deploy version v0.2.96, got:\n%s", msg)
		}
		if strings.Contains(msg, "--phase expand") {
			t.Fatalf("no expand missing, must not suggest expand:\n%s", msg)
		}
	})

	t.Run("both phases each get their own command", func(t *testing.T) {
		msg := formatMigrationRemediation("foghorn", "v0.2.96", "v0.2.90", expand, postdeploy)
		if !strings.Contains(msg, "--phase expand --to-version v0.2.96") ||
			!strings.Contains(msg, "--phase postdeploy --to-version v0.2.90") {
			t.Fatalf("both remediation commands must appear with their own versions, got:\n%s", msg)
		}
	})
}

// TestServiceDependsOnClickHouse pins the pre-deploy gate's engine-awareness: Periscope services are detected as
// ClickHouse-dependent (so the gate verifies ClickHouse expand migrations for them), while a Postgres-only service and
// a DB-less one are not — mirroring topology's InfraClickHouse declarations.
func TestServiceDependsOnClickHouse(t *testing.T) {
	for _, s := range []string{"periscope-ingest", "periscope-query"} {
		if !serviceDependsOnClickHouse(s) {
			t.Errorf("%s must be detected as ClickHouse-dependent so the gate verifies ClickHouse migrations", s)
		}
		if len(topology.InfraDependencies(s)) == 0 {
			t.Errorf("topology has no infra dependencies for %q", s)
		}
	}
	for _, s := range []string{"foghorn", "purser"} {
		if serviceDependsOnClickHouse(s) {
			t.Errorf("%s is not ClickHouse-dependent; the gate must not require ClickHouse migrations for it", s)
		}
	}
}

// TestUpgradeGate_ClickHouseCheckedWhenPostgresDisabled exercises the gate's engine ROUTING through the real
// runUpgradePreDeployGate (not just the topology classifier): for a ClickHouse-only service (periscope-ingest) it must
// run the ClickHouse branch even with Postgres disabled, and it must NOT run the Postgres branch — the engine gates are
// independent topology-driven branches, so a disabled Postgres never suppresses the ClickHouse check. The SSH-backed
// engine checks are stubbed via their seams so the routing is observable with no live cluster.
func TestUpgradeGate_ClickHouseCheckedWhenPostgresDisabled(t *testing.T) {
	// This test exercises the migration-gate routing, not the CLI version floor; give it a concrete version at/above
	// the catalog's min_cli_version so the shared floor check (checkCLIVersionFloor) passes.
	origVer := fwv.Version
	t.Cleanup(func() { fwv.Version = origVer })
	fwv.Version = "v0.2.96"

	origPG, origCH, origFloor := checkPostgresMigrationGateFn, checkClickHouseMigrationGateFn, runBelowFloorGuardFn
	t.Cleanup(func() {
		checkPostgresMigrationGateFn, checkClickHouseMigrationGateFn, runBelowFloorGuardFn = origPG, origCH, origFloor
	})
	pgCalled, chCalled, floorCalled := false, false, false
	checkPostgresMigrationGateFn = func(_ context.Context, _ *resolvedCluster, _ *ssh.Pool, _ *inventory.Manifest, _, _, _ string) error {
		pgCalled = true
		return nil
	}
	checkClickHouseMigrationGateFn = func(_ context.Context, _ *resolvedCluster, _ *ssh.Pool, _ *inventory.Manifest, _, _ string) error {
		chCalled = true
		return nil
	}
	runBelowFloorGuardFn = func(_ context.Context, _ *resolvedCluster, _ *ssh.Pool) error {
		floorCalled = true
		return nil
	}

	// periscope-ingest is ClickHouse-only (topology: InfraClickHouse, no InfraDatabase). Postgres is NOT enabled.
	manifest := &inventory.Manifest{}
	rc := &resolvedCluster{Manifest: manifest}
	err := runUpgradePreDeployGate(context.Background(), &cobra.Command{}, rc, nil, manifest,
		"v0.2.96", "periscope-ingest", "periscope-ingest", false, true /* skipDataMigrationCheck */)
	if err != nil {
		t.Fatalf("gate must not error for a ClickHouse-only service with stubbed checks: %v", err)
	}
	if !chCalled {
		t.Error("ClickHouse migration check must run for a ClickHouse-dependent service even with Postgres disabled")
	}
	if pgCalled {
		t.Error("Postgres check must not run for a ClickHouse-only service")
	}
	if !floorCalled {
		t.Error("baseline-floor guard must run")
	}
}

// TestUpgradeGate_DataMigrationCheckedForDBLessService pins the fix that decouples the catalog-wide data-migration
// check from database schema ownership: a DB-less service (signalman: Kafka-only) must STILL reach the data-migration
// check, so it cannot deploy ahead of an incomplete required_before_version data migration. Under the previous
// early-return for non-DB services, the check was skipped entirely. The shipped catalog declares zero data migrations,
// so the check passes with its "no required data migrations" line — which only prints if the block was actually reached.
func TestUpgradeGate_DataMigrationCheckedForDBLessService(t *testing.T) {
	// A concrete version at/above the catalog floor so the shared CLI-version-floor check passes and the test isolates
	// the data-migration routing.
	origVer := fwv.Version
	t.Cleanup(func() { fwv.Version = origVer })
	fwv.Version = "v0.2.96"

	// signalman is DB-less; the engine branches are not taken, so no SSH is touched. Stub the floor guard defensively.
	origFloor := runBelowFloorGuardFn
	t.Cleanup(func() { runBelowFloorGuardFn = origFloor })
	runBelowFloorGuardFn = func(_ context.Context, _ *resolvedCluster, _ *ssh.Pool) error { return nil }

	manifest := &inventory.Manifest{}
	rc := &resolvedCluster{Manifest: manifest}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	err := runUpgradePreDeployGate(context.Background(), cmd, rc, nil, manifest,
		"v0.2.96", "signalman", "signalman", false, false /* run the data-migration check */)
	if err != nil {
		t.Fatalf("gate must not error for a DB-less service: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "no engine schema checks") {
		t.Errorf("a DB-less service should report no engine schema checks, output:\n%s", got)
	}
	if !strings.Contains(got, "no required data migrations declared") {
		t.Errorf("a DB-less service must still reach the data-migration check (not be skipped), output:\n%s", got)
	}
}
