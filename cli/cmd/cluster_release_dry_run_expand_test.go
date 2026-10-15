package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
)

// recordingMigrator stands in for the migrate roles: it records each call and returns failWith for the engines it
// names, so the migrate branches run without ansible.
type recordingMigrator struct {
	engine   string
	calls    *[]string
	failWith map[string]error
}

func (m recordingMigrator) ApplyMigrations(_ context.Context, _ inventory.Host, _ provisioner.ServiceConfig, dryRun bool) error {
	*m.calls = append(*m.calls, fmt.Sprintf("%s dry-run=%t", m.engine, dryRun))
	return m.failWith[m.engine]
}

func installRecordingMigrator(t *testing.T, failWith map[string]error) *[]string {
	t.Helper()
	orig := migratorForFn
	t.Cleanup(func() { migratorForFn = orig })
	var calls []string
	migratorForFn = func(serviceName string, _ *ssh.Pool) (provisioner.Migrator, error) {
		return recordingMigrator{engine: serviceName, calls: &calls, failWith: failWith}, nil
	}
	return &calls
}

// pendingTargetExpandLedger makes every ledger read report the target release's own expand migrations as absent
// (the cluster runs the previous release) and every earlier migration as applied.
func pendingTargetExpandLedger(t *testing.T) {
	t.Helper()
	pendingFrom := func(items []map[string]any) []provisioner.MigrationKey {
		var keys []provisioner.MigrationKey
		for _, item := range items {
			if fmt.Sprint(item["version"]) != newDatabaseReleaseTarget {
				continue
			}
			seq, _ := item["sequence"].(int) //nolint:errcheck // the item builders always set an int sequence
			keys = append(keys, provisioner.MigrationKey{
				Database: fmt.Sprint(item["db"]), Version: fmt.Sprint(item["version"]), Phase: fmt.Sprint(item["phase"]),
				Seq: seq, Filename: fmt.Sprint(item["filename"]),
			})
		}
		return keys
	}
	missingPostgresMigrationsFn = func(_ context.Context, _ *ssh.Pool, _ inventory.Host, _ *inventory.PostgresConfig, _ string, databases []provisioner.SchemaDatabase, phase, target string) ([]provisioner.MigrationKey, error) {
		if phase != "expand" {
			return nil, nil
		}
		items, err := provisioner.BuildMigrationItemsForDatabases(databases, phase, target)
		return pendingFrom(items), err
	}
	missingClickHouseMigrationsFn = func(_ context.Context, _ *ssh.Pool, _ inventory.Host, _ int, _ string, databases []string, phase, target string) ([]provisioner.MigrationKey, error) {
		if phase != "expand" {
			return nil, nil
		}
		items, err := provisioner.BuildClickHouseMigrationItems(databases, phase, target)
		return pendingFrom(items), err
	}
}

// pendingExpandReleaseCluster declares the periscope Postgres database and the periscope ClickHouse database, both
// present on the cluster with the target release's expand migrations still pending. periscope-ingest depends on both.
func pendingExpandReleaseCluster(t *testing.T) *resolvedCluster {
	t.Helper()
	reads := &ledgerReads{existing: map[string]bool{"periscope": true}}
	reads.install(t)
	pendingTargetExpandLedger(t)
	manifest := newDatabaseReleaseManifest()
	manifest.Infrastructure.Postgres.Databases = []inventory.DatabaseConfig{{Name: "periscope"}}
	return &resolvedCluster{Manifest: manifest}
}

// A dry-run release runs the expand step (planned only, under --check), then periscope-ingest's pre-deploy gate. The
// gate reads both ledgers, finds the target's expand migrations missing, and must count those the dry-run expand
// step would apply as applied instead of refusing the rollout the real expand step makes safe.
func TestReleaseDryRunGateCountsExpandMigrationsTheExpandStepWouldApply(t *testing.T) {
	rc := pendingExpandReleaseCluster(t)
	calls := installRecordingMigrator(t, nil)
	cmd, out := commandWithOutput()

	if err := runMigrate(cmd, rc, true, "expand", true, newDatabaseReleaseTarget, true, false); err != nil {
		t.Fatalf("dry-run expand: %v", err)
	}
	if strings.Join(*calls, ",") != "postgres dry-run=true,clickhouse dry-run=true" {
		t.Fatalf("dry-run expand ran migrate roles %v, want both engines in check mode", *calls)
	}
	out.Reset()

	if err := runUpgradePreDeployGate(context.Background(), cmd, rc, nil, rc.Manifest, newDatabaseReleaseTarget, "periscope-ingest", "periscope-ingest", false, true); err != nil {
		t.Fatalf("dry-run gate for periscope-ingest refused expand migrations the expand step would apply: %v", err)
	}
	for _, want := range []string{
		"periscope/v0.3.13/expand/001_ordered_time_indexes.notx.sql",
		"periscope/v0.3.13/expand/007_stream_health_buffer_edge_at.sql",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dry-run gate did not report %s as applied by the expand step:\n%s", want, out.String())
		}
	}
	if got := strings.Count(out.String(), "would be applied by this release's expand step"); got != 2 {
		t.Fatalf("dry-run gate reported %d engine(s) with expand migrations the expand step would apply, want postgres and clickhouse:\n%s", got, out.String())
	}

	if err := runMigrate(cmd, rc, true, "postdeploy", true, newDatabaseReleaseTarget, true, false); err != nil {
		t.Fatalf("dry-run postdeploy: %v", err)
	}
}

// A dry-run expand step that fails records nothing the gates may rely on.
func TestReleaseDryRunGateRefusesWhenTheExpandStepFailed(t *testing.T) {
	rc := pendingExpandReleaseCluster(t)
	installRecordingMigrator(t, map[string]error{"clickhouse": errors.New("check mode failed")})
	cmd, _ := commandWithOutput()

	if err := runMigrate(cmd, rc, true, "expand", true, newDatabaseReleaseTarget, true, false); err == nil {
		t.Fatal("dry-run expand passed with a failing clickhouse migrate role")
	}
	err := runUpgradePreDeployGate(context.Background(), cmd, rc, nil, rc.Manifest, newDatabaseReleaseTarget, "periscope-ingest", "periscope-ingest", false, true)
	if err == nil || !strings.Contains(err.Error(), "periscope/v0.3.13/expand/007_stream_health_buffer_edge_at.sql") {
		t.Fatalf("gate after a failed clickhouse dry-run expand: %v, want the clickhouse expand migrations refused", err)
	}
}

// A real expand step that fails leaves the ledger without the target's expand migrations, and the gate refuses on
// the ledger alone: only a dry-run counts planned migrations as applied.
func TestReleaseApplyGateRefusesExpandMigrationsAfterRealExpandFailure(t *testing.T) {
	rc := pendingExpandReleaseCluster(t)
	calls := installRecordingMigrator(t, map[string]error{"postgres": errors.New("apply failed"), "clickhouse": errors.New("apply failed")})
	cmd, _ := commandWithOutput()

	if err := runMigrate(cmd, rc, false, "expand", true, newDatabaseReleaseTarget, true, false); err == nil {
		t.Fatal("real expand passed with failing migrate roles")
	}
	if strings.Join(*calls, ",") != "postgres dry-run=false,clickhouse dry-run=false" {
		t.Fatalf("real expand ran migrate roles %v, want both engines applying", *calls)
	}
	if rc.dryRunExpandMigrations != nil {
		t.Fatalf("real expand recorded %d planned migration(s); a real apply checks the ledger strictly", len(rc.dryRunExpandMigrations))
	}
	err := runUpgradePreDeployGate(context.Background(), cmd, rc, nil, rc.Manifest, newDatabaseReleaseTarget, "periscope-ingest", "periscope-ingest", false, true)
	if err == nil || !strings.Contains(err.Error(), "periscope/v0.3.13/expand/001_ordered_time_indexes.notx.sql") {
		t.Fatalf("real gate after a failed expand: %v, want the postgres expand migrations refused", err)
	}

	checkClickHouse := checkClickHouseMigrationGate(context.Background(), cmd.OutOrStdout(), rc, nil, rc.Manifest, "periscope-ingest", newDatabaseReleaseTarget)
	if checkClickHouse == nil || !strings.Contains(checkClickHouse.Error(), "periscope/v0.3.13/expand/007_stream_health_buffer_edge_at.sql") {
		t.Fatalf("real clickhouse gate after a failed expand: %v, want the clickhouse expand migrations refused", checkClickHouse)
	}
}

// A ledgered migration whose checksum differs is never counted as applied by a dry-run expand step: the migrate role
// refuses it rather than re-applying it.
func TestDryRunExpandSplitKeepsChecksumMismatches(t *testing.T) {
	recorded := map[string]struct{}{}
	recordDryRunExpandMigrations(recorded, dryRunEngineClickHouse, []map[string]any{
		{"db": "periscope", "version": "v0.3.13", "phase": "expand", "sequence": 7},
		{"db": "periscope", "version": "v0.3.13", "phase": "expand", "sequence": 6},
	})
	missing := []provisioner.MigrationKey{
		{Database: "periscope", Version: "v0.3.13", Phase: "expand", Seq: 7},
		{Database: "periscope", Version: "v0.3.13", Phase: "expand", Seq: 6, MismatchedChecksum: "abc"},
		{Database: "periscope", Version: "v0.3.13", Phase: "expand", Seq: 8},
	}
	remaining, wouldApply := splitDryRunExpandMigrations(missing, dryRunEngineClickHouse, recorded)
	if len(wouldApply) != 1 || wouldApply[0].Seq != 7 {
		t.Fatalf("would apply %v, want only the recorded migration without a checksum mismatch", wouldApply)
	}
	if len(remaining) != 2 || remaining[0].Seq != 6 || remaining[1].Seq != 8 {
		t.Fatalf("remaining %v, want the checksum mismatch and the unrecorded migration", remaining)
	}
	if remaining, _ := splitDryRunExpandMigrations(missing, dryRunEnginePostgres, recorded); len(remaining) != 3 {
		t.Fatalf("postgres split used clickhouse records: remaining %v", remaining)
	}
}
