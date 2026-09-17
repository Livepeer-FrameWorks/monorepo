package cmd

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

func postgresServiceDatabaseManifest() *inventory.Manifest {
	return &inventory.Manifest{
		Hosts: map[string]inventory.Host{"db-1": {ExternalIP: "192.0.2.10", User: "root"}},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled: true,
				Host:    "db-1",
				Databases: []inventory.DatabaseConfig{
					{Name: "commodore"},
					{Name: "lookout"},
					{Name: "chatwoot", Owner: "chatwoot"},
				},
			},
		},
	}
}

type serviceDatabaseSeams struct {
	calls            []string
	probes           [][]string
	states           []map[string]provisioner.ServiceDatabaseState
	probeErr         error
	bootstraps       [][]string
	initializeStates []map[string]provisioner.ServiceDatabaseState
}

func (s *serviceDatabaseSeams) install(t *testing.T) {
	t.Helper()
	origRead, origBootstrap := readServiceDatabaseStatesFn, bootstrapServiceDatabasesFn
	t.Cleanup(func() { readServiceDatabaseStatesFn, bootstrapServiceDatabasesFn = origRead, origBootstrap })
	readServiceDatabaseStatesFn = func(_ context.Context, _ *ssh.Pool, _ inventory.Host, _ *inventory.PostgresConfig, databases []provisioner.SchemaDatabase) (map[string]provisioner.ServiceDatabaseState, error) {
		s.calls = append(s.calls, "probe")
		s.probes = append(s.probes, schemaDatabaseNameList(databases))
		if s.probeErr != nil {
			return nil, s.probeErr
		}
		if len(s.states) == 0 {
			t.Fatal("unexpected service database probe")
		}
		next := s.states[0]
		s.states = s.states[1:]
		return next, nil
	}
	bootstrapServiceDatabasesFn = func(_ context.Context, _ *cobra.Command, _ *resolvedCluster, _ *ssh.Pool, _ inventory.Host, databases []provisioner.SchemaDatabase) error {
		s.calls = append(s.calls, "bootstrap")
		s.bootstraps = append(s.bootstraps, schemaDatabaseNameList(databases))
		return nil
	}
}

// installInitialize replaces the SSH-backed completion with one that records
// the pending databases and their states, applies them, and returns result.
func (s *serviceDatabaseSeams) installInitialize(t *testing.T, result error) {
	t.Helper()
	orig := initializeServiceDatabasesFn
	t.Cleanup(func() { initializeServiceDatabasesFn = orig })
	initializeServiceDatabasesFn = func(ctx context.Context, _ *ssh.Pool, _ inventory.Host, _ *inventory.PostgresConfig, pending []provisioner.SchemaDatabase, states map[string]provisioner.ServiceDatabaseState, apply provisioner.BaselineApplier) error {
		s.calls = append(s.calls, "initialize")
		s.initializeStates = append(s.initializeStates, states)
		if err := apply(ctx, pending); err != nil {
			return err
		}
		return result
	}
}

func commandWithOutput() (*cobra.Command, *bytes.Buffer) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	return cmd, &out
}

func TestEnsureServiceDatabasesDryRunPlansWithoutMutating(t *testing.T) {
	seams := &serviceDatabaseSeams{states: []map[string]provisioner.ServiceDatabaseState{{
		"commodore": {Exists: true, HasTables: true, Initialized: true},
		"lookout":   {},
	}}}
	seams.install(t)
	cmd, out := commandWithOutput()
	rc := &resolvedCluster{Manifest: postgresServiceDatabaseManifest()}

	planned, err := ensureServiceDatabases(context.Background(), cmd, rc, nil, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := schemaDatabaseNameList(planned); !reflect.DeepEqual(got, []string{"lookout"}) {
		t.Fatalf("planned = %v, want lookout", got)
	}
	if !reflect.DeepEqual(seams.calls, []string{"probe"}) {
		t.Fatalf("calls = %v, dry-run must only probe", seams.calls)
	}
	if !reflect.DeepEqual(seams.probes[0], []string{"commodore", "lookout"}) {
		t.Fatalf("probed %v; databases without a baseline (chatwoot) are not service databases", seams.probes[0])
	}
	text := out.String()
	if !strings.Contains(text, "lookout (owner lookout, runtime role lookout_runtime, baseline schema/lookout.sql)") || !strings.Contains(text, "[DRY-RUN]") || strings.Contains(text, "- commodore") {
		t.Fatalf("dry-run output:\n%s", text)
	}
}

func TestEnsureServiceDatabasesCreatesOnlyMissingDatabasesAndVerifies(t *testing.T) {
	seams := &serviceDatabaseSeams{states: []map[string]provisioner.ServiceDatabaseState{
		{"commodore": {Exists: true, HasTables: true, Initialized: true}, "lookout": {}},
		{"lookout": {Exists: true, HasTables: true, Initialized: true}},
	}}
	seams.install(t)
	cmd, _ := commandWithOutput()
	rc := &resolvedCluster{Manifest: postgresServiceDatabaseManifest()}

	created, err := ensureServiceDatabases(context.Background(), cmd, rc, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seams.calls, []string{"probe", "bootstrap", "probe"}) {
		t.Fatalf("calls = %v, want probe -> bootstrap -> verification probe", seams.calls)
	}
	if !reflect.DeepEqual(seams.bootstraps, [][]string{{"lookout"}}) || !reflect.DeepEqual(seams.probes[1], []string{"lookout"}) {
		t.Fatalf("bootstraps = %v, verification probe = %v; an existing database must never be bootstrapped", seams.bootstraps, seams.probes[1])
	}
	if got := schemaDatabaseNameList(created); !reflect.DeepEqual(got, []string{"lookout"}) {
		t.Fatalf("created = %v", got)
	}
}

func TestEnsureServiceDatabasesDryRunReportsUnverifiedSchemaCompletion(t *testing.T) {
	seams := &serviceDatabaseSeams{states: []map[string]provisioner.ServiceDatabaseState{{
		"commodore": {Exists: true, HasTables: true, Initialized: true},
		"lookout":   {Exists: true, HasTables: true},
	}}}
	seams.install(t)
	seams.installInitialize(t, nil)
	cmd, out := commandWithOutput()

	planned, err := ensureServiceDatabases(context.Background(), cmd, &resolvedCluster{Manifest: postgresServiceDatabaseManifest()}, nil, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := schemaDatabaseNameList(planned); !reflect.DeepEqual(got, []string{"lookout"}) || !reflect.DeepEqual(seams.calls, []string{"probe"}) {
		t.Fatalf("planned = %v calls = %v; dry-run must only probe and skip the database's ledger checks", got, seams.calls)
	}
	text := out.String()
	for _, want := range []string{"re-apply baseline", "lookout__baseline_check_<hex>", "refuse on any difference", "[DRY-RUN]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("dry-run output lacks %q:\n%s", want, text)
		}
	}
}

func TestEnsureServiceDatabasesRequiresExplicitUnverifiedSchemaCompletion(t *testing.T) {
	seams := &serviceDatabaseSeams{states: []map[string]provisioner.ServiceDatabaseState{{
		"commodore": {Exists: true, HasTables: true, Initialized: true},
		"lookout":   {Exists: true, HasTables: true},
	}}}
	seams.install(t)
	cmd, _ := commandWithOutput()

	_, err := ensureServiceDatabases(context.Background(), cmd, &resolvedCluster{Manifest: postgresServiceDatabaseManifest()}, nil, false, false)
	if err == nil || !strings.Contains(err.Error(), "--complete-interrupted-baselines") || !strings.Contains(err.Error(), "lookout") {
		t.Fatalf("err = %v, want explicit completion refusal naming lookout", err)
	}
	if !reflect.DeepEqual(seams.calls, []string{"probe"}) || len(seams.bootstraps) != 0 {
		t.Fatalf("calls = %v bootstraps = %v; refusal must happen before mutation", seams.calls, seams.bootstraps)
	}
}

func TestEnsureServiceDatabasesCompletesUnverifiedSchema(t *testing.T) {
	seams := &serviceDatabaseSeams{states: []map[string]provisioner.ServiceDatabaseState{
		{"commodore": {Exists: true, HasTables: true, Initialized: true}, "lookout": {Exists: true, HasTables: true}},
		{"lookout": {Exists: true, HasTables: true, Initialized: true}},
	}}
	seams.install(t)
	seams.installInitialize(t, nil)
	cmd, _ := commandWithOutput()

	initialized, err := ensureServiceDatabases(context.Background(), cmd, &resolvedCluster{Manifest: postgresServiceDatabaseManifest()}, nil, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seams.calls, []string{"probe", "initialize", "bootstrap", "probe"}) {
		t.Fatalf("calls = %v, want probe -> completion (applying through the bootstrap) -> verification probe", seams.calls)
	}
	if !seams.initializeStates[0]["lookout"].HasUnverifiedSchema() || !reflect.DeepEqual(seams.bootstraps, [][]string{{"lookout"}}) {
		t.Fatalf("completion states = %v bootstraps = %v", seams.initializeStates, seams.bootstraps)
	}
	if got := schemaDatabaseNameList(initialized); !reflect.DeepEqual(got, []string{"lookout"}) {
		t.Fatalf("initialized = %v", got)
	}
}

func TestEnsureServiceDatabasesFailsClosed(t *testing.T) {
	t.Run("bootstrap that leaves the database without a baseline", func(t *testing.T) {
		seams := &serviceDatabaseSeams{states: []map[string]provisioner.ServiceDatabaseState{
			{"commodore": {Exists: true, HasTables: true, Initialized: true}, "lookout": {}},
			{"lookout": {Exists: true}},
		}}
		seams.install(t)
		cmd, _ := commandWithOutput()
		_, err := ensureServiceDatabases(context.Background(), cmd, &resolvedCluster{Manifest: postgresServiceDatabaseManifest()}, nil, false, false)
		if err == nil || !strings.Contains(err.Error(), "lookout") {
			t.Fatalf("err = %v, want refusal naming lookout", err)
		}
	})
	t.Run("unverified schema whose completion finds differences", func(t *testing.T) {
		seams := &serviceDatabaseSeams{states: []map[string]provisioner.ServiceDatabaseState{
			{"commodore": {Exists: true, HasTables: true, Initialized: true}, "lookout": {Exists: true, HasTables: true}},
		}}
		seams.install(t)
		divergence := errors.New(`service database "lookout" ... column lookout.incidents.extra`)
		seams.installInitialize(t, divergence)
		cmd, _ := commandWithOutput()
		_, err := ensureServiceDatabases(context.Background(), cmd, &resolvedCluster{Manifest: postgresServiceDatabaseManifest()}, nil, false, true)
		if !errors.Is(err, divergence) || !reflect.DeepEqual(seams.calls, []string{"probe", "initialize", "bootstrap"}) {
			t.Fatalf("err = %v, calls = %v; a divergent schema must stop the release before the verification probe", err, seams.calls)
		}
	})
	t.Run("probe failure", func(t *testing.T) {
		seams := &serviceDatabaseSeams{probeErr: errors.New("ssh unreachable")}
		seams.install(t)
		cmd, _ := commandWithOutput()
		_, err := ensureServiceDatabases(context.Background(), cmd, &resolvedCluster{Manifest: postgresServiceDatabaseManifest()}, nil, false, false)
		if err == nil || len(seams.bootstraps) != 0 {
			t.Fatalf("err = %v, bootstraps = %v; a failed probe must refuse without bootstrapping", err, seams.bootstraps)
		}
	})
	t.Run("fully provisioned cluster is a no-op", func(t *testing.T) {
		seams := &serviceDatabaseSeams{states: []map[string]provisioner.ServiceDatabaseState{
			{"commodore": {Exists: true, HasTables: true, Initialized: true}, "lookout": {Exists: true, HasTables: true, Initialized: true}},
		}}
		seams.install(t)
		cmd, _ := commandWithOutput()
		created, err := ensureServiceDatabases(context.Background(), cmd, &resolvedCluster{Manifest: postgresServiceDatabaseManifest()}, nil, false, false)
		if err != nil || len(created) != 0 || len(seams.bootstraps) != 0 {
			t.Fatalf("created = %v, bootstraps = %v, err = %v", created, seams.bootstraps, err)
		}
	})
}

func TestRunMigrateCreatesServiceDatabasesBeforeLedgerChecks(t *testing.T) {
	origEnsure, origGuard := ensureServiceDatabasesFn, migrateBelowFloorGuardFn
	t.Cleanup(func() { ensureServiceDatabasesFn, migrateBelowFloorGuardFn = origEnsure, origGuard })

	var calls []string
	var guardExcluded map[string]struct{}
	var ensureDryRun bool
	ensureErr := error(nil)
	ensureServiceDatabasesFn = func(_ context.Context, _ *cobra.Command, _ *resolvedCluster, _ *ssh.Pool, dryRun, _ bool) ([]provisioner.SchemaDatabase, error) {
		calls = append(calls, "ensure")
		ensureDryRun = dryRun
		if ensureErr != nil {
			return nil, ensureErr
		}
		return []provisioner.SchemaDatabase{{Name: "lookout"}}, nil
	}
	migrateBelowFloorGuardFn = func(_ context.Context, _ *resolvedCluster, _ *ssh.Pool, excluded map[string]struct{}) error {
		calls = append(calls, "floor-guard")
		guardExcluded = excluded
		return nil
	}
	run := func(dryRun bool, phase string) error {
		calls, guardExcluded, ensureDryRun = nil, nil, false
		cmd, _ := commandWithOutput()
		return runMigrate(cmd, &resolvedCluster{Manifest: &inventory.Manifest{}}, dryRun, phase, true, "v0.3.8", true, false)
	}

	if err := run(false, "expand"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"ensure", "floor-guard"}) || ensureDryRun || guardExcluded != nil {
		t.Fatalf("expand calls = %v dryRun = %v excluded = %v; databases must exist before the floor guard reads their ledgers", calls, ensureDryRun, guardExcluded)
	}

	if err := run(true, "expand"); err != nil {
		t.Fatal(err)
	}
	if _, ok := guardExcluded["lookout"]; !ok || !ensureDryRun || !reflect.DeepEqual(calls, []string{"ensure", "floor-guard"}) {
		t.Fatalf("dry-run calls = %v excluded = %v; a database the dry-run would create must be skipped by ledger checks", calls, guardExcluded)
	}

	if err := run(false, "postdeploy"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"floor-guard"}) {
		t.Fatalf("postdeploy calls = %v; only the expand phase creates databases", calls)
	}

	ensureErr = errors.New("bootstrap failed")
	if err := run(false, "expand"); err == nil || !errors.Is(err, ensureErr) || !reflect.DeepEqual(calls, []string{"ensure"}) {
		t.Fatalf("err = %v calls = %v; a failed bootstrap must stop before any ledger check or migration", err, calls)
	}
}

func TestRunMigrateStartsItsBudgetAfterServiceDatabaseBaselines(t *testing.T) {
	origEnsure, origGuard := ensureServiceDatabasesFn, migrateBelowFloorGuardFn
	t.Cleanup(func() { ensureServiceDatabasesFn, migrateBelowFloorGuardFn = origEnsure, origGuard })

	var ensureHadDeadline bool
	var ensureReturned time.Time
	ensureServiceDatabasesFn = func(ctx context.Context, _ *cobra.Command, _ *resolvedCluster, _ *ssh.Pool, _, _ bool) ([]provisioner.SchemaDatabase, error) {
		_, ensureHadDeadline = ctx.Deadline()
		time.Sleep(50 * time.Millisecond)
		ensureReturned = time.Now()
		return nil, nil
	}
	var guardDeadline time.Time
	migrateBelowFloorGuardFn = func(ctx context.Context, _ *resolvedCluster, _ *ssh.Pool, _ map[string]struct{}) error {
		guardDeadline, _ = ctx.Deadline()
		return nil
	}
	cmd, _ := commandWithOutput()
	if err := runMigrate(cmd, &resolvedCluster{Manifest: &inventory.Manifest{}}, false, "expand", true, "v0.3.8", true, false); err != nil {
		t.Fatal(err)
	}
	if ensureHadDeadline {
		t.Fatal("service database baselines ran under the migrate budget; a long completion would be cancelled by it")
	}
	if guardDeadline.IsZero() || guardDeadline.Before(ensureReturned.Add(migrateTimeout)) {
		t.Fatalf("guard deadline = %v, ensure returned = %v; the migrate budget must start after the baselines", guardDeadline, ensureReturned)
	}
}

func TestPostgresGateRefusesMissingServiceDatabaseBeforeLedgerChecks(t *testing.T) {
	seams := &serviceDatabaseSeams{states: []map[string]provisioner.ServiceDatabaseState{{"lookout": {}}}}
	seams.install(t)
	manifest := postgresServiceDatabaseManifest()
	err := checkPostgresMigrationGate(context.Background(), &resolvedCluster{Manifest: manifest}, nil, manifest, "lookout", "lookout", "v0.3.8")
	if err == nil {
		t.Fatal("gate passed for a service whose database does not exist")
	}
	for _, want := range []string{"lookout", "missing or have no baseline", "frameworks cluster release apply --version v0.3.8", "frameworks cluster migrate --phase expand --to-version v0.3.8"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("gate refusal %q lacks %q", err, want)
		}
	}
	if !reflect.DeepEqual(seams.probes, [][]string{{"lookout"}}) {
		t.Fatalf("probes = %v, want only the service's own database", seams.probes)
	}
}

func TestPostgresGateIncludesExplicitCompletionForUnverifiedDatabase(t *testing.T) {
	seams := &serviceDatabaseSeams{states: []map[string]provisioner.ServiceDatabaseState{{
		"lookout": {Exists: true, HasTables: true},
	}}}
	seams.install(t)
	manifest := postgresServiceDatabaseManifest()
	err := checkPostgresMigrationGate(context.Background(), &resolvedCluster{Manifest: manifest}, nil, manifest, "lookout", "lookout", "v0.3.8")
	if err == nil || !strings.Contains(err.Error(), "--complete-interrupted-baselines") {
		t.Fatalf("err = %v, want explicit completion remediation", err)
	}
}

func TestServiceDatabasesResolveYugabyteRegionalAliases(t *testing.T) {
	manifest := productionYugabyteManifest()
	manifest.Infrastructure.Postgres.Databases = append(manifest.Infrastructure.Postgres.Databases, inventory.DatabaseConfig{Name: "lookout", Owner: "lookout"})

	databases, err := manifestServiceDatabases(manifest, manifest.Infrastructure.Postgres.Databases)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]provisioner.SchemaDatabase{}
	for _, database := range databases {
		byName[database.Name] = database
	}
	if _, logical := byName["foghorn"]; logical {
		t.Fatalf("service databases = %+v; the logical foghorn database is never created when it expands per cell", databases)
	}
	for _, cell := range []string{"foghorn_eu", "foghorn_us"} {
		database, ok := byName[cell]
		if !ok || database.SourceName != "foghorn" || database.Schema != "foghorn" || database.Owner != cell || database.RuntimeRole != cell+"_runtime" {
			t.Fatalf("%s = %+v (present %v), want the foghorn baseline under the per-cell owner and runtime role", cell, database, ok)
		}
	}
	if lookout := byName["lookout"]; lookout.SourceName != "lookout" || lookout.Schema != "lookout" || lookout.RuntimeRole != "lookout_runtime" {
		t.Fatalf("lookout = %+v", lookout)
	}

	// The gate keys by the catalog database of one deploy and must resolve the
	// same physical per-cell databases the release step creates.
	cells, err := manifestServiceDatabases(manifest, []inventory.DatabaseConfig{{Name: "foghorn"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := schemaDatabaseNameList(cells); !reflect.DeepEqual(got, []string{"foghorn_eu", "foghorn_us"}) {
		t.Fatalf("gate databases for foghorn = %v", got)
	}
}

func TestReleasePlanListsServiceDatabases(t *testing.T) {
	if got := releasePlanServiceDatabases(postgresServiceDatabaseManifest()); !reflect.DeepEqual(got, []string{"commodore", "lookout"}) {
		t.Fatalf("release plan service databases = %v", got)
	}
	if got := releasePlanServiceDatabases(&inventory.Manifest{}); got != nil {
		t.Fatalf("manifest without postgres = %v, want none", got)
	}
}
