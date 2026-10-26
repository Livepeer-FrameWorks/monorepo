package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
	"frameworks/cli/pkg/watchdog"

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
	// limitRuns records each runtime-role session-limit convergence: the roles
	// it was handed and whether it ran as a dry run. unbounded is what it
	// reports altered.
	limitRuns []sessionLimitRun
	unbounded []string
}

type sessionLimitRun struct {
	roles  []string
	dryRun bool
	// target is the server the run connected to, as host:port.
	target string
}

func (s *serviceDatabaseSeams) install(t *testing.T) {
	t.Helper()
	origRead, origBootstrap, origLimits := readServiceDatabaseStatesFn, bootstrapServiceDatabasesFn, ensureRuntimeRoleSessionLimitsFn
	t.Cleanup(func() {
		readServiceDatabaseStatesFn, bootstrapServiceDatabasesFn, ensureRuntimeRoleSessionLimitsFn = origRead, origBootstrap, origLimits
	})
	ensureRuntimeRoleSessionLimitsFn = func(_ context.Context, _ *ssh.Pool, host inventory.Host, pg *inventory.PostgresConfig, roles []string, dryRun bool) ([]string, error) {
		s.limitRuns = append(s.limitRuns, sessionLimitRun{roles: roles, dryRun: dryRun, target: fmt.Sprintf("%s:%d", host.ExternalIP, pg.EffectivePort())})
		return s.unbounded, nil
	}
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

// A cluster whose databases all exist still gets the runtime-role session
// limits: `release apply` and `cluster migrate --phase expand` attach them to
// every runtime role of the deployment, the third-party ones included, and a
// dry run only reads.
func TestEnsureServiceDatabasesConvergesRuntimeRoleSessionLimits(t *testing.T) {
	allPresent := map[string]provisioner.ServiceDatabaseState{
		"commodore": {Exists: true, HasTables: true, Initialized: true},
		"lookout":   {Exists: true, HasTables: true, Initialized: true},
	}
	wantRoles := []string{"chatwoot_runtime", "commodore_runtime", "lookout_runtime"}
	for _, dryRun := range []bool{false, true} {
		seams := &serviceDatabaseSeams{states: []map[string]provisioner.ServiceDatabaseState{allPresent}, unbounded: []string{"commodore_runtime"}}
		seams.install(t)
		cmd, out := commandWithOutput()
		if _, err := ensureServiceDatabases(context.Background(), cmd, &resolvedCluster{Manifest: postgresServiceDatabaseManifest()}, nil, dryRun, false); err != nil {
			t.Fatal(err)
		}
		if want := []sessionLimitRun{{roles: wantRoles, dryRun: dryRun, target: "192.0.2.10:5432"}}; !reflect.DeepEqual(seams.limitRuns, want) {
			t.Fatalf("dry-run %v: session limit runs = %+v, want %+v", dryRun, seams.limitRuns, want)
		}
		wantLine := "Bounded runtime role sessions (statement_timeout=60s, idle_in_transaction_session_timeout=60s): commodore_runtime"
		if dryRun {
			wantLine = "[DRY-RUN] would bound runtime role sessions (statement_timeout=60s, idle_in_transaction_session_timeout=60s): commodore_runtime"
		}
		if !strings.Contains(out.String(), wantLine) {
			t.Fatalf("dry-run %v: output lacks %q:\n%s", dryRun, wantLine, out.String())
		}
	}
}

// A database the release creates is bounded once its roles exist, after the
// verification probe.
func TestEnsureServiceDatabasesBoundsSessionsAfterBootstrap(t *testing.T) {
	seams := &serviceDatabaseSeams{states: []map[string]provisioner.ServiceDatabaseState{
		{"commodore": {Exists: true, HasTables: true, Initialized: true}, "lookout": {}},
		{"lookout": {Exists: true, HasTables: true, Initialized: true}},
	}}
	seams.install(t)
	origLimits := ensureRuntimeRoleSessionLimitsFn
	ensureRuntimeRoleSessionLimitsFn = func(ctx context.Context, pool *ssh.Pool, host inventory.Host, pg *inventory.PostgresConfig, roles []string, dryRun bool) ([]string, error) {
		seams.calls = append(seams.calls, "session-limits")
		return origLimits(ctx, pool, host, pg, roles, dryRun)
	}
	cmd, out := commandWithOutput()
	if _, err := ensureServiceDatabases(context.Background(), cmd, &resolvedCluster{Manifest: postgresServiceDatabaseManifest()}, nil, false, false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seams.calls, []string{"probe", "bootstrap", "probe", "session-limits"}) {
		t.Fatalf("calls = %v, want the limits after the verification probe", seams.calls)
	}
	if !strings.Contains(out.String(), "Runtime role sessions: all 3 role(s) carry statement_timeout=60s, idle_in_transaction_session_timeout=60s") {
		t.Fatalf("output:\n%s", out.String())
	}
}

// Databases on a named Postgres instance run on that instance's own server,
// so their runtime roles are bounded there, on the instance's host and port,
// also when the deployment itself holds no service database.
func TestEnsureServiceDatabasesBoundsPostgresInstanceRoles(t *testing.T) {
	withInstance := func(manifest *inventory.Manifest) *inventory.Manifest {
		manifest.Hosts["apps-1"] = inventory.Host{ExternalIP: "192.0.2.20", User: "root"}
		manifest.Infrastructure.Postgres.Instances = []inventory.PostgresInstance{
			{Name: "apps", Host: "apps-1", Port: 5434, Databases: []inventory.DatabaseConfig{{Name: "listmonk"}, {Name: "chatwoot", Owner: "chatwoot"}}},
			{Name: "empty", Host: "apps-1", Port: 5435},
		}
		return manifest
	}
	instanceRun := sessionLimitRun{roles: []string{"chatwoot_runtime", "listmonk_runtime"}, target: "192.0.2.20:5434"}

	seams := &serviceDatabaseSeams{states: []map[string]provisioner.ServiceDatabaseState{{
		"commodore": {Exists: true, HasTables: true, Initialized: true},
		"lookout":   {Exists: true, HasTables: true, Initialized: true},
	}}}
	seams.install(t)
	cmd, _ := commandWithOutput()
	if _, err := ensureServiceDatabases(context.Background(), cmd, &resolvedCluster{Manifest: withInstance(postgresServiceDatabaseManifest())}, nil, false, false); err != nil {
		t.Fatal(err)
	}
	want := []sessionLimitRun{{roles: []string{"chatwoot_runtime", "commodore_runtime", "lookout_runtime"}, target: "192.0.2.10:5432"}, instanceRun}
	if !reflect.DeepEqual(seams.limitRuns, want) {
		t.Fatalf("session limit runs = %+v\nwant %+v", seams.limitRuns, want)
	}

	instanceOnly := withInstance(postgresServiceDatabaseManifest())
	instanceOnly.Infrastructure.Postgres.Databases = nil
	seams = &serviceDatabaseSeams{}
	seams.install(t)
	for _, dryRun := range []bool{false, true} {
		seams.limitRuns = nil
		if _, err := ensureServiceDatabases(context.Background(), cmd, &resolvedCluster{Manifest: instanceOnly}, nil, dryRun, false); err != nil {
			t.Fatal(err)
		}
		run := instanceRun
		run.dryRun = dryRun
		if !reflect.DeepEqual(seams.limitRuns, []sessionLimitRun{run}) {
			t.Fatalf("dry-run %v, instances only: session limit runs = %+v, want %+v", dryRun, seams.limitRuns, run)
		}
	}
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

// The databases a dry-run expand step would create are skipped by the same
// release's later dry-run steps, and a real apply never skips any.
func TestRunMigrateCarriesDryRunCreatedDatabasesThroughTheRelease(t *testing.T) {
	origEnsure, origGuard := ensureServiceDatabasesFn, migrateBelowFloorGuardFn
	t.Cleanup(func() { ensureServiceDatabasesFn, migrateBelowFloorGuardFn = origEnsure, origGuard })
	ensureServiceDatabasesFn = func(context.Context, *cobra.Command, *resolvedCluster, *ssh.Pool, bool, bool) ([]provisioner.SchemaDatabase, error) {
		return []provisioner.SchemaDatabase{{Name: "network_observer"}}, nil
	}
	var guardExcluded map[string]struct{}
	migrateBelowFloorGuardFn = func(_ context.Context, _ *resolvedCluster, _ *ssh.Pool, excluded map[string]struct{}) error {
		guardExcluded = excluded
		return nil
	}
	cmd, _ := commandWithOutput()

	applied := &resolvedCluster{Manifest: &inventory.Manifest{}}
	for _, phase := range []string{"expand", "postdeploy"} {
		if err := runMigrate(cmd, applied, false, phase, true, "v0.3.13", true, false); err != nil {
			t.Fatal(err)
		}
		if guardExcluded != nil || applied.dryRunCreatedDatabases != nil {
			t.Fatalf("real %s excluded %v and recorded %v; a real apply has created every database and checks them all", phase, guardExcluded, applied.dryRunCreatedDatabases)
		}
	}

	dry := &resolvedCluster{Manifest: &inventory.Manifest{}}
	for _, phase := range []string{"expand", "postdeploy"} {
		guardExcluded = nil
		if err := runMigrate(cmd, dry, true, phase, true, "v0.3.13", true, false); err != nil {
			t.Fatal(err)
		}
		if _, ok := guardExcluded["network_observer"]; !ok {
			t.Fatalf("dry-run %s floor guard excluded %v, want network_observer", phase, guardExcluded)
		}
	}
	if _, ok := dry.withDatabaseManifest(dry.Manifest).dryRunCreatedDatabases["network_observer"]; !ok {
		t.Fatal("a database deployment view lost the dry-run's planned databases")
	}
}

func TestRunMigrateStartsItsWatchdogAfterServiceDatabaseBaselines(t *testing.T) {
	origEnsure, origGuard := ensureServiceDatabasesFn, migrateBelowFloorGuardFn
	t.Cleanup(func() { ensureServiceDatabasesFn, migrateBelowFloorGuardFn = origEnsure, origGuard })

	var ensureBounded bool
	ensureServiceDatabasesFn = func(ctx context.Context, _ *cobra.Command, _ *resolvedCluster, _ *ssh.Pool, _, _ bool) ([]provisioner.SchemaDatabase, error) {
		_, hasDeadline := ctx.Deadline()
		ensureBounded = hasDeadline || watchdog.From(ctx) != nil
		return nil, nil
	}
	var guardWatched, guardHasDeadline bool
	migrateBelowFloorGuardFn = func(ctx context.Context, _ *resolvedCluster, _ *ssh.Pool, _ map[string]struct{}) error {
		guardWatched = watchdog.From(ctx) != nil
		_, guardHasDeadline = ctx.Deadline()
		return nil
	}
	cmd, _ := commandWithOutput()
	if err := runMigrate(cmd, &resolvedCluster{Manifest: &inventory.Manifest{}}, false, "expand", true, "v0.3.8", true, false); err != nil {
		t.Fatal(err)
	}
	if ensureBounded {
		t.Fatal("service database baselines ran under the migrate watchdog or a deadline; a long completion would be cancelled by it")
	}
	if !guardWatched || guardHasDeadline {
		t.Fatalf("floor guard watched=%t deadline=%t, want it under the preflight watchdog with no wall-clock deadline", guardWatched, guardHasDeadline)
	}
}

func TestPostgresGateRefusesMissingServiceDatabaseBeforeLedgerChecks(t *testing.T) {
	seams := &serviceDatabaseSeams{states: []map[string]provisioner.ServiceDatabaseState{{"lookout": {}}}}
	seams.install(t)
	manifest := postgresServiceDatabaseManifest()
	err := checkPostgresMigrationGate(context.Background(), io.Discard, &resolvedCluster{Manifest: manifest}, nil, manifest, "lookout", "lookout", "v0.3.8")
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
	err := checkPostgresMigrationGate(context.Background(), io.Discard, &resolvedCluster{Manifest: manifest}, nil, manifest, "lookout", "lookout", "v0.3.8")
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
