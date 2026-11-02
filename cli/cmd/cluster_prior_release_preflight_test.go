package cmd

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/backup"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/spf13/cobra"
)

// releaseApplyHarness drives runReleaseApply against a single-host PostgreSQL manifest with scripted release metadata,
// ledgers and data-migration states. The first mutation `release apply` reaches after its preflight is the node
// baseline convergence, which the harness records; it stops at the service database step of the expand migrations.
type releaseApplyHarness struct {
	missingFrom       string // prior releases at or above this version report a missing postdeploy migration
	pendingDataID     string // this required data migration reports pending; every other one is completed
	mutationCalled    bool
	nodeBaselineHosts []string
}

var errHarnessStoppedAtMutation = errors.New("harness: stopped at the first mutation")

func (h *releaseApplyHarness) install(t *testing.T) {
	t.Helper()
	origFetch, origEnsure, origStates := fetchReleaseManifestFn, ensureServiceDatabasesFn, readServiceDatabaseStatesFn
	origPG, origCH, origData := missingPostgresMigrationsFn, missingClickHouseMigrationsFn, priorDataMigrationSourceFn
	origHostConvergence := releaseNewHostConvergenceFn
	t.Cleanup(func() {
		fetchReleaseManifestFn, ensureServiceDatabasesFn, readServiceDatabaseStatesFn = origFetch, origEnsure, origStates
		missingPostgresMigrationsFn, missingClickHouseMigrationsFn, priorDataMigrationSourceFn = origPG, origCH, origData
		releaseNewHostConvergenceFn = origHostConvergence
	})
	releaseNewHostConvergenceFn = func(cmd *cobra.Command, rc *resolvedCluster, _ string, _ *ssh.Pool) (*releaseHostConvergence, error) {
		return &releaseHostConvergence{
			cmd:         cmd,
			manifest:    rc.Manifest,
			runtimeData: map[string]any{},
			sharedEnv:   map[string]string{"SERVICE_TOKEN": "token"},
			nodeHostFn: func(_ context.Context, host inventory.Host, _ bool, _ io.Writer) error {
				h.mutationCalled = true
				h.nodeBaselineHosts = append(h.nodeBaselineHosts, host.Name)
				return nil
			},
			verifySSHFn: func(context.Context, []string) error { return nil },
		}, nil
	}
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

const harnessPendingDataMigration = "commodore_pull_source_pins_to_stream_rules_v0_3_8"

// stubPriorDataMigrationRun records each data-migrations run `release apply` sends and marks the migration completed
// after a real (non-dry) run that runErr does not fail. irreversible decides the backup gate.
type stubPriorDataMigrationRun struct {
	h            *releaseApplyHarness
	runs         [][]string
	runErr       error
	irreversible bool
}

func (s *stubPriorDataMigrationRun) install(t *testing.T) {
	t.Helper()
	origRun, origEntry, origDigests := runDataMigrateRemoteFn, remoteDataMigrationEntryFn, serviceDatabaseDigestsFn
	t.Cleanup(func() {
		runDataMigrateRemoteFn, remoteDataMigrationEntryFn, serviceDatabaseDigestsFn = origRun, origEntry, origDigests
	})
	runDataMigrateRemoteFn = func(_ *cobra.Command, _ *resolvedCluster, service string, args []string) error {
		s.runs = append(s.runs, append([]string{service}, args...))
		if s.runErr != nil {
			return s.runErr
		}
		if !slices.Contains(args, "--dry-run") {
			s.h.pendingDataID = ""
		}
		return nil
	}
	remoteDataMigrationEntryFn = func(_ context.Context, _ *resolvedCluster, _ *ssh.Pool, _, id string) (dataMigrationListEntry, bool, error) {
		irreversible := s.irreversible
		return dataMigrationListEntry{ID: id, Irreversible: &irreversible}, true, nil
	}
	serviceDatabaseDigestsFn = func(context.Context, *resolvedCluster, *ssh.Pool, string) (map[string]string, error) {
		return map[string]string{"postgres/commodore": "digest"}, nil
	}
}

func TestReleaseApplyRunsPendingPriorDataMigrationFirst(t *testing.T) {
	h := &releaseApplyHarness{pendingDataID: harnessPendingDataMigration}
	h.install(t)
	stub := &stubPriorDataMigrationRun{h: h}
	stub.install(t)
	out, err := h.apply(t, false)
	if !h.mutationCalled || !errors.Is(err, errHarnessStoppedAtMutation) {
		t.Fatalf("err = %v, mutation reached = %v; a pending earlier-release data migration must be run, not refused\n%s", err, h.mutationCalled, out)
	}
	if len(stub.runs) != 1 || strings.Join(stub.runs[0], " ") != "commodore data-migrations run "+harnessPendingDataMigration+" --batch-size 1000" {
		t.Fatalf("runs = %v; want one real run of %s", stub.runs, harnessPendingDataMigration)
	}
	if !strings.Contains(out, "0. required data migrations of earlier releases: commodore."+harnessPendingDataMigration+" (v0.3.8, pending)") {
		t.Fatalf("the release plan does not list the earlier data migration:\n%s", out)
	}
	if strings.Index(out, "Required data migrations of earlier releases") > strings.Index(out, "Pre-upgrade host convergence") {
		t.Fatalf("the earlier data migration did not run before host convergence:\n%s", out)
	}
}

func TestReleaseApplyDryRunPreviewsPendingPriorDataMigration(t *testing.T) {
	h := &releaseApplyHarness{pendingDataID: harnessPendingDataMigration}
	h.install(t)
	stub := &stubPriorDataMigrationRun{h: h}
	stub.install(t)
	_, err := h.apply(t, true)
	if !h.mutationCalled || !errors.Is(err, errHarnessStoppedAtMutation) {
		t.Fatalf("err = %v; a dry run must preview the earlier data migration and continue", err)
	}
	if len(stub.runs) != 1 || !slices.Contains(stub.runs[0], "--dry-run") {
		t.Fatalf("runs = %v; want one read-only run", stub.runs)
	}
}

// The dry run refuses an irreversible earlier data migration without --backup exactly as the real run does, so a
// green dry run means the real apply passes the same gate.
func TestReleaseApplyGatesIrreversiblePriorDataMigrationOnBackup(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		h := &releaseApplyHarness{pendingDataID: harnessPendingDataMigration}
		h.install(t)
		stub := &stubPriorDataMigrationRun{h: h, irreversible: true}
		stub.install(t)
		_, err := h.apply(t, dryRun)
		if h.mutationCalled || len(stub.runs) != 0 {
			t.Fatalf("dry-run=%v: an irreversible earlier data migration ran or the release mutated without a backup (runs=%v, err=%v)", dryRun, stub.runs, err)
		}
		if err == nil || !errors.Is(err, backup.ErrBackupRequired) || !strings.Contains(err.Error(), "commodore."+harnessPendingDataMigration) ||
			!strings.Contains(err.Error(), "--database commodore") {
			t.Fatalf("dry-run=%v: err = %v; want the backup gate refusal naming the migration and the database to back up", dryRun, err)
		}
	}
}

func TestReleaseApplyNamesTheFailedPriorDataMigration(t *testing.T) {
	h := &releaseApplyHarness{pendingDataID: harnessPendingDataMigration}
	h.install(t)
	stub := &stubPriorDataMigrationRun{h: h, runErr: errors.New("commodore data-migrations exit 1")}
	stub.install(t)
	_, err := h.apply(t, false)
	if h.mutationCalled {
		t.Fatalf("release apply continued after the earlier data migration failed (err=%v)", err)
	}
	if err == nil || !strings.Contains(err.Error(), "required data migration commodore."+harnessPendingDataMigration+" of release v0.3.8 failed") ||
		strings.Contains(err.Error(), "release apply --version v0.3.8") {
		t.Fatalf("err = %v; want a failure naming the migration, not an instruction to apply v0.3.8", err)
	}
}

func TestReleaseApplyRefusesPausedPriorDataMigrationWithItsRemedy(t *testing.T) {
	h := &releaseApplyHarness{}
	h.install(t)
	priorDataMigrationSourceFn = func(*ssh.Pool, *inventory.Manifest) datamigrate.StateSource {
		return func(_ context.Context, service, id string) datamigrate.LiveStatus {
			if id == harnessPendingDataMigration {
				return datamigrate.LiveStatus{ID: id, Service: service, Status: datamigrate.StatusPaused}
			}
			return datamigrate.LiveStatus{ID: id, Service: service, Status: datamigrate.StatusCompleted}
		}
	}
	_, err := h.apply(t, false)
	if h.mutationCalled {
		t.Fatal("release apply mutated with a paused earlier data migration")
	}
	if err == nil || !strings.Contains(err.Error(), "data-migrate resume commodore."+harnessPendingDataMigration) || strings.Contains(err.Error(), "release apply --version") {
		t.Fatalf("err = %v; want a refusal naming the resume command and no release to apply", err)
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

func TestDryRunGatesSkipOnlyTheDataMigrationsTheDryRunRan(t *testing.T) {
	ran := datamigrate.Blocker{Requirement: datamigrate.Requirement{Service: "purser", ID: "purser_eur_ledger_conversion_v0_3_11"}, Reason: "pending"}
	other := datamigrate.Blocker{Requirement: datamigrate.Requirement{Service: "commodore", ID: harnessPendingDataMigration}, Reason: "pending"}
	unreadable := datamigrate.Blocker{Requirement: ran.Requirement, Live: datamigrate.LiveStatus{FetchError: errors.New("ssh")}}
	rc := &resolvedCluster{dryRunDataMigrations: map[string]struct{}{"purser.purser_eur_ledger_conversion_v0_3_11": {}}}
	got := withoutDryRunDataMigrations([]datamigrate.Blocker{ran, other, unreadable}, rc)
	if len(got) != 2 || got[0].Requirement.ID != harnessPendingDataMigration || !got[1].Unreadable() {
		t.Fatalf("kept %+v; want the migration the dry run did not run and the unreadable state", got)
	}
	if got := withoutDryRunDataMigrations([]datamigrate.Blocker{ran}, nil); len(got) != 1 {
		t.Fatal("a real run must keep every blocker")
	}
}
