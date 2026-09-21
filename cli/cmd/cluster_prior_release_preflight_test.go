package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/spf13/cobra"
)

// releaseApplyHarness drives runReleaseApply against a single-host PostgreSQL manifest with scripted release metadata,
// ledgers and data-migration states. The first mutation `release apply` reaches after its preflight is the service
// database step of the expand migrations; the harness records it and stops there.
type releaseApplyHarness struct {
	missingFrom    string // prior releases at or above this version report a missing postdeploy migration
	pendingDataID  string // this required data migration reports pending; every other one is completed
	mutationCalled bool
}

var errHarnessStoppedAtMutation = errors.New("harness: stopped at the first mutation")

func (h *releaseApplyHarness) install(t *testing.T) {
	t.Helper()
	origFetch, origEnsure, origStates := fetchReleaseManifestFn, ensureServiceDatabasesFn, readServiceDatabaseStatesFn
	origPG, origCH, origData := missingPostgresMigrationsFn, missingClickHouseMigrationsFn, priorDataMigrationSourceFn
	t.Cleanup(func() {
		fetchReleaseManifestFn, ensureServiceDatabasesFn, readServiceDatabaseStatesFn = origFetch, origEnsure, origStates
		missingPostgresMigrationsFn, missingClickHouseMigrationsFn, priorDataMigrationSourceFn = origPG, origCH, origData
	})
	fetchReleaseManifestFn = func(_ gitops.FetchOptions, _ []string, _, version string) (*gitops.Manifest, error) {
		return &gitops.Manifest{PlatformVersion: version}, nil
	}
	ensureServiceDatabasesFn = func(context.Context, *cobra.Command, *resolvedCluster, *ssh.Pool, bool, bool) ([]provisioner.SchemaDatabase, error) {
		h.mutationCalled = true
		return nil, errHarnessStoppedAtMutation
	}
	readServiceDatabaseStatesFn = func(_ context.Context, _ *ssh.Pool, _ inventory.Host, _ *inventory.PostgresConfig, databases []provisioner.SchemaDatabase) (map[string]provisioner.ServiceDatabaseState, error) {
		states := map[string]provisioner.ServiceDatabaseState{}
		for _, database := range databases {
			states[database.Name] = provisioner.ServiceDatabaseState{Exists: true, HasTables: true, Initialized: true}
		}
		return states, nil
	}
	missingPostgresMigrationsFn = func(_ context.Context, _ *ssh.Pool, _ inventory.Host, _ *inventory.PostgresConfig, _ string, databases []provisioner.SchemaDatabase, phase, version string) ([]provisioner.MigrationKey, error) {
		if phase != "postdeploy" || h.missingFrom == "" || releases.CompareSemver(version, h.missingFrom) < 0 {
			return nil, nil
		}
		return []provisioner.MigrationKey{{Database: databases[0].Name, Version: h.missingFrom, Phase: "postdeploy", Seq: 1, Filename: "001_backfill.sql"}}, nil
	}
	missingClickHouseMigrationsFn = func(context.Context, *ssh.Pool, inventory.Host, int, string, []string, string, string) ([]provisioner.MigrationKey, error) {
		t.Fatal("the manifest has no ClickHouse; its ledger must not be read")
		return nil, nil
	}
	priorDataMigrationSourceFn = func(*ssh.Pool, *inventory.Manifest) datamigrate.StateSource {
		return func(_ context.Context, service, id string) datamigrate.LiveStatus {
			if id == h.pendingDataID {
				return datamigrate.LiveStatus{ID: id, Service: service, Status: datamigrate.StatusPending}
			}
			return datamigrate.LiveStatus{ID: id, Service: service, Status: datamigrate.StatusCompleted}
		}
	}
}

func (h *releaseApplyHarness) apply(t *testing.T, dryRun bool) (string, error) {
	t.Helper()
	h.mutationCalled = false
	cmd := newClusterReleaseApplyCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	if err := cmd.Flags().Set(unsafeCLIFloorFlag, "true"); err != nil {
		t.Fatal(err)
	}
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{"db-1": {Name: "db-1", ExternalIP: "192.0.2.10", User: "root"}},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{Enabled: true, Host: "db-1", Databases: []inventory.DatabaseConfig{{Name: "commodore"}}},
		},
	}
	err := runReleaseApply(cmd, &resolvedCluster{Manifest: manifest}, releaseApplyOptions{version: "v0.3.11", dryRun: dryRun, yes: true})
	return out.String(), err
}

func TestReleaseApplyRefusesIncompletePriorReleaseBeforeFirstMutation(t *testing.T) {
	h := &releaseApplyHarness{missingFrom: "v0.3.9"}
	h.install(t)
	for _, dryRun := range []bool{false, true} {
		out, err := h.apply(t, dryRun)
		if h.mutationCalled || errors.Is(err, errHarnessStoppedAtMutation) {
			t.Fatalf("dry-run=%v: release apply reached the expand step before refusing the incomplete v0.3.9 (err=%v)", dryRun, err)
		}
		if err == nil || !strings.Contains(err.Error(), "v0.3.9") || !strings.Contains(err.Error(), "commodore/v0.3.9/postdeploy/001_backfill.sql") ||
			!strings.Contains(err.Error(), "frameworks cluster release apply --version v0.3.9") {
			t.Fatalf("dry-run=%v: err = %v; want a refusal naming v0.3.9, its missing migration, and the release to apply first", dryRun, err)
		}
		if strings.Contains(out, "Pre-upgrade host convergence") || strings.Contains(out, "Expand migrations") {
			t.Fatalf("dry-run=%v: a release step started before the refusal:\n%s", dryRun, out)
		}
	}
}

func TestReleaseApplyRefusesIncompletePriorDataMigrationBeforeFirstMutation(t *testing.T) {
	h := &releaseApplyHarness{pendingDataID: "commodore_pull_source_pins_to_stream_rules_v0_3_8"}
	h.install(t)
	for _, dryRun := range []bool{false, true} {
		_, err := h.apply(t, dryRun)
		if h.mutationCalled {
			t.Fatalf("dry-run=%v: release apply reached the expand step before refusing the pending v0.3.8 data migration (err=%v)", dryRun, err)
		}
		if err == nil || !strings.Contains(err.Error(), "commodore/commodore_pull_source_pins_to_stream_rules_v0_3_8") || !strings.Contains(err.Error(), "v0.3.8") {
			t.Fatalf("dry-run=%v: err = %v; want a refusal naming the pending v0.3.8 data migration", dryRun, err)
		}
	}
}

func TestReleaseApplyProceedsWhenPriorReleasesAreComplete(t *testing.T) {
	h := &releaseApplyHarness{}
	h.install(t)
	_, err := h.apply(t, false)
	if !h.mutationCalled || !errors.Is(err, errHarnessStoppedAtMutation) {
		t.Fatalf("err = %v, mutation reached = %v; a cluster with every prior release complete must reach the expand step", err, h.mutationCalled)
	}
}
