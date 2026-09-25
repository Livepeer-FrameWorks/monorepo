//go:build schema_verify

package provisioner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// dockerSQLProbe is the serviceDatabaseProbe transport over `docker exec` psql
// or ysqlsh, so the release completion code runs unchanged against a real
// engine.
type dockerSQLProbe struct {
	t         *testing.T
	container string
	yb        bool
}

func (p dockerSQLProbe) run(database, query string) (string, error) {
	p.t.Helper()
	args := []string{"exec", p.container, "psql", "-U", "postgres"}
	if p.yb {
		args = []string{"exec", p.container, "ysqlsh", "-h", ybSQLHost(p.container), "-U", "yugabyte"}
	}
	args = append(args, "-d", database, "-X", "-v", "ON_ERROR_STOP=1", "-tAc", query)
	return dockerWithTimeout(p.t, 10*time.Minute, "", args...)
}

func (p dockerSQLProbe) databaseNames(context.Context) (map[string]struct{}, error) {
	out, err := p.run(p.maintenanceDatabase(), listDatabasesQuery)
	if err != nil {
		return nil, err
	}
	return parseDatabaseNameLines(out), nil
}

func (p dockerSQLProbe) schemaHasTables(ctx context.Context, database, schema string) (bool, error) {
	return p.scalarBool(ctx, database, schemaHasTablesQuery(schema))
}

func (p dockerSQLProbe) scalarText(_ context.Context, database, query string) (string, error) {
	out, err := p.run(database, query)
	return strings.TrimSpace(out), err
}

func (p dockerSQLProbe) scalarBool(_ context.Context, database, query string) (bool, error) {
	out, err := p.run(database, query)
	if err != nil {
		return false, err
	}
	return parsePsqlBoolScalar(out)
}

func (p dockerSQLProbe) textRows(_ context.Context, database, query string) ([]string, error) {
	out, err := p.run(database, query)
	if err != nil {
		return nil, err
	}
	return parseTextRows(out), nil
}

func (p dockerSQLProbe) exec(_ context.Context, database, statement string) error {
	_, err := p.run(database, statement)
	return err
}

func (p dockerSQLProbe) maintenanceDatabase() string {
	if p.yb {
		return "yugabyte"
	}
	return "postgres"
}

func (p dockerSQLProbe) yugabyte() bool { return p.yb }

func (p dockerSQLProbe) apply(database, sql string) {
	p.t.Helper()
	if p.yb {
		ybApply(p.t, p.container, database, sql)
		return
	}
	pgApply(p.t, p.container, database, sql)
}

func (p dockerSQLProbe) createDatabase(database string) {
	p.t.Helper()
	if err := p.exec(context.Background(), p.maintenanceDatabase(), "CREATE DATABASE "+database); err != nil {
		p.t.Fatalf("create database %s: %v", database, err)
	}
}

func (p dockerSQLProbe) catalog(database, schema string) map[string]string {
	p.t.Helper()
	catalog, err := readServiceSchemaCatalog(context.Background(), p, database, schema)
	if err != nil {
		p.t.Fatalf("read %s catalog: %v", database, err)
	}
	if len(catalog) == 0 {
		p.t.Fatalf("%s catalog for schema %s is empty", database, schema)
	}
	return catalog
}

func (p dockerSQLProbe) state(database SchemaDatabase) ServiceDatabaseState {
	p.t.Helper()
	states, err := readServiceDatabaseStates(context.Background(), p, []SchemaDatabase{database})
	if err != nil {
		p.t.Fatalf("probe %s: %v", database.Name, err)
	}
	return states[database.Name]
}

func (p dockerSQLProbe) requireNoBaselineReferences() {
	p.t.Helper()
	names, err := p.databaseNames(context.Background())
	if err != nil {
		p.t.Fatal(err)
	}
	for name := range names {
		if strings.Contains(name, baselineReferenceInfix) {
			p.t.Fatalf("baseline reference database %s was left behind", name)
		}
	}
}

// baselineApplier mirrors the schema role's apply step: every target receives
// its embedded baseline, as the role does for reapply items and for schemas
// without tables.
func (p dockerSQLProbe) baselineApplier() BaselineApplier {
	return func(_ context.Context, databases []SchemaDatabase) error {
		for _, database := range databases {
			p.apply(database.Name, embeddedBaselineForTest(p.t, database.SourceName))
		}
		return nil
	}
}

func embeddedBaselineForTest(t *testing.T, source string) string {
	t.Helper()
	sql, ok, err := embeddedBaselineSQL(source)
	if err != nil || !ok {
		t.Fatalf("embedded baseline %s: ok=%v err=%v", source, ok, err)
	}
	return sql
}

// interruptedBaselinePrefix returns the baseline up to the top-level CREATE
// TABLE statement nearest its middle: the state a baseline apply leaves when it
// stops between statements on an engine without transactional DDL.
func interruptedBaselinePrefix(t *testing.T, source string) string {
	t.Helper()
	lines := strings.Split(embeddedBaselineForTest(t, source), "\n")
	cut := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "CREATE TABLE IF NOT EXISTS ") && (cut < 0 || abs(i-len(lines)/2) < abs(cut-len(lines)/2)) {
			cut = i
		}
	}
	if cut <= 0 {
		t.Fatalf("baseline %s has no top-level CREATE TABLE to interrupt at", source)
	}
	return strings.Join(lines[:cut], "\n") + "\n"
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func serviceDatabaseForTest(name, source string) SchemaDatabase {
	database, _ := normalizeSchemaDatabase(SchemaDatabase{Name: name, SourceName: source, Schema: source})
	return database
}

func requireCatalogsEqual(t *testing.T, label string, expected, actual map[string]string) {
	t.Helper()
	if differences := diffServiceSchemaCatalogs(expected, actual); len(differences) > 0 {
		t.Fatalf("%s:\n%s", label, formatBaselineDivergence(SchemaDatabase{Name: label, SourceName: label, Schema: label}, differences))
	}
}

func requireBaselineFloor(t *testing.T, probe dockerSQLProbe, database string) {
	t.Helper()
	floor, err := probe.scalarText(context.Background(), database, baselineMarkerQuery)
	if err != nil || floor != schemaMigrationBaselineFloor {
		t.Fatalf("%s baseline marker = %q, %v; want %s", database, floor, err, schemaMigrationBaselineFloor)
	}
}

// TestPostgresServiceBaselinesApplyTwice guards the property the release
// completion of an interrupted baseline depends on: every service baseline
// applies a second time without error and leaves its schema catalog unchanged.
func TestPostgresServiceBaselinesApplyTwice(t *testing.T) {
	requireDocker(t)
	name := uniqueContainerName("fw-sv-pg-baseline-twice")
	pgStart(t, name)
	probe := dockerSQLProbe{t: t, container: name}

	for _, file := range pgBaselineFiles(t) {
		source := strings.TrimSuffix(filepath.Base(file), ".sql")
		t.Run(source, func(t *testing.T) {
			probe := probe
			probe.t = t
			database := source + "_twice"
			probe.createDatabase(database)
			baseline := embeddedBaselineForTest(t, source)
			probe.apply(database, baseline)
			once := probe.catalog(database, source)
			if source == "purser" {
				probe.apply(database, "UPDATE purser.meter_definitions SET display_name = 'Operator override', default_priceable = FALSE, active = FALSE WHERE meter = 'delivered_minutes'")
			}
			if out, err := docker(t, baseline, "exec", "-i", "-e", "PGOPTIONS=-c client_min_messages=warning", name, "psql", "-U", "postgres", "-d", database, "-v", "ON_ERROR_STOP=1", "-q"); err != nil {
				t.Fatalf("schema/%s.sql is not idempotent; second apply failed: %v\n%s", source, err, out)
			}
			requireCatalogsEqual(t, "schema/"+source+".sql applied twice", once, probe.catalog(database, source))
			if source == "purser" {
				preserved, err := probe.scalarBool(context.Background(), database, "SELECT display_name = 'Operator override' AND NOT default_priceable AND NOT active FROM purser.meter_definitions WHERE meter = 'delivered_minutes'")
				if err != nil {
					t.Fatalf("read operator-managed meter: %v", err)
				}
				if !preserved {
					t.Fatal("reapplying the Purser baseline changed an operator-managed meter")
				}
			}
		})
	}
}

// TestPostgresServiceDatabaseCompletesInterruptedBaseline completes a database
// holding the first half of its baseline and no marker, with a reference left
// by an earlier interrupted completion still present.
func TestPostgresServiceDatabaseCompletesInterruptedBaseline(t *testing.T) {
	requireDocker(t)
	name := uniqueContainerName("fw-sv-pg-baseline-complete")
	pgStart(t, name)
	probe := dockerSQLProbe{t: t, container: name}
	ctx := context.Background()
	database := serviceDatabaseForTest("commodore", "commodore")

	probe.createDatabase("commodore_fresh")
	probe.apply("commodore_fresh", embeddedBaselineForTest(t, "commodore"))

	probe.createDatabase("commodore")
	probe.apply("commodore", interruptedBaselinePrefix(t, "commodore"))
	probe.createDatabase("commodore" + baselineReferenceInfix + "0000000000000000")
	state := probe.state(database)
	if !state.HasUnverifiedSchema() {
		t.Fatalf("interrupted baseline state = %+v, want tables without marker", state)
	}

	if err := initializeServiceDatabases(ctx, probe, []SchemaDatabase{database}, map[string]ServiceDatabaseState{"commodore": state}, probe.baselineApplier(), randomReferenceSuffix, serviceBaselineTimeout); err != nil {
		t.Fatal(err)
	}
	if state := probe.state(database); !state.Initialized {
		t.Fatalf("completed database state = %+v, want initialized", state)
	}
	requireBaselineFloor(t, probe, "commodore")
	probe.requireNoBaselineReferences()
	requirePGSchemasEqual(t, "completed interrupted baseline vs fresh baseline", pgIntrospect(t, name, "commodore_fresh"), pgIntrospect(t, name, "commodore"))
}

// TestPostgresServiceDatabaseRefusesDivergentSchema proves a schema that the
// baseline cannot complete is refused with the differing object, that the
// refusal repeats on the next release, and that the database is accepted once
// the difference is removed.
func TestPostgresServiceDatabaseRefusesDivergentSchema(t *testing.T) {
	requireDocker(t)
	name := uniqueContainerName("fw-sv-pg-baseline-divergent")
	pgStart(t, name)
	probe := dockerSQLProbe{t: t, container: name}
	ctx := context.Background()
	database := serviceDatabaseForTest("navigator", "navigator")

	probe.createDatabase("navigator")
	probe.apply("navigator", embeddedBaselineForTest(t, "navigator"))
	table, err := probe.scalarText(ctx, "navigator", "SELECT min(table_name) FROM information_schema.tables WHERE table_schema = 'navigator' AND table_type = 'BASE TABLE'")
	if err != nil || table == "" {
		t.Fatalf("pick navigator table: %q, %v", table, err)
	}
	probe.apply("navigator", fmt.Sprintf("ALTER TABLE navigator.%s ADD COLUMN operator_note text; DELETE FROM public._schema_baseline;", table))

	wantObject := fmt.Sprintf("  column navigator.%s.operator_note\n    expected: (absent)\n    actual:   text", table)
	for attempt := 1; attempt <= 2; attempt++ {
		state := probe.state(database)
		if !state.HasUnverifiedSchema() {
			t.Fatalf("attempt %d: state = %+v, want an unverified schema", attempt, state)
		}
		err := initializeServiceDatabases(ctx, probe, []SchemaDatabase{database}, map[string]ServiceDatabaseState{"navigator": state}, probe.baselineApplier(), randomReferenceSuffix, serviceBaselineTimeout)
		if err == nil || !strings.Contains(err.Error(), `service database "navigator"`) || !strings.Contains(err.Error(), wantObject) {
			t.Fatalf("attempt %d: err = %v, want refusal naming %q", attempt, err, wantObject)
		}
		if !strings.Contains(err.Error(), "(1 difference(s))") {
			t.Fatalf("attempt %d: re-applying the baseline reported unrelated differences:\n%v", attempt, err)
		}
		probe.requireNoBaselineReferences()
		if floor, _ := probe.scalarText(ctx, "navigator", baselineMarkerQuery); floor != baselineVerificationPendingFloor {
			t.Fatalf("attempt %d: marker = %q, want the verification-pending marker", attempt, floor)
		}
		if gap := validateBaselineFloor("navigator", baselineVerificationPendingFloor); gap == nil {
			t.Fatal("floor readers must refuse an unverified database")
		}
	}

	probe.apply("navigator", fmt.Sprintf("ALTER TABLE navigator.%s DROP COLUMN operator_note;", table))
	state := probe.state(database)
	if err := initializeServiceDatabases(ctx, probe, []SchemaDatabase{database}, map[string]ServiceDatabaseState{"navigator": state}, probe.baselineApplier(), randomReferenceSuffix, serviceBaselineTimeout); err != nil {
		t.Fatalf("schema matching its baseline was refused: %v", err)
	}
	if state := probe.state(database); !state.Initialized {
		t.Fatalf("state = %+v, want initialized", state)
	}
	requireBaselineFloor(t, probe, "navigator")
}

// TestPostgresServiceDatabaseDropsReferenceOnFailedApply proves the reference
// is dropped and the database stays unverified when the apply step fails.
func TestPostgresServiceDatabaseDropsReferenceOnFailedApply(t *testing.T) {
	requireDocker(t)
	name := uniqueContainerName("fw-sv-pg-baseline-apply-fails")
	pgStart(t, name)
	probe := dockerSQLProbe{t: t, container: name}
	ctx := context.Background()
	database := serviceDatabaseForTest("skipper", "skipper")

	probe.createDatabase("skipper")
	probe.apply("skipper", interruptedBaselinePrefix(t, "skipper"))
	state := probe.state(database)

	applyErr := errors.New("schema role failed")
	var referenceSeen bool
	failing := func(_ context.Context, databases []SchemaDatabase) error {
		for _, target := range databases {
			if strings.Contains(target.Name, baselineReferenceInfix) {
				names, err := probe.databaseNames(ctx)
				if _, exists := names[target.Name]; err != nil || !exists {
					t.Fatalf("reference %s does not exist during apply: %v", target.Name, err)
				}
				referenceSeen = true
			}
		}
		return applyErr
	}
	err := initializeServiceDatabases(ctx, probe, []SchemaDatabase{database}, map[string]ServiceDatabaseState{"skipper": state}, failing, randomReferenceSuffix, serviceBaselineTimeout)
	if !errors.Is(err, applyErr) || !referenceSeen {
		t.Fatalf("err = %v referenceSeen = %v", err, referenceSeen)
	}
	probe.requireNoBaselineReferences()
	if state := probe.state(database); !state.HasUnverifiedSchema() {
		t.Fatalf("state after failed apply = %+v, want still unverified", state)
	}
}

// TestYugabyteServiceBaselineReapplyAndCompletion runs the completion on
// YugabyteDB, where an interrupted baseline keeps its partial DDL, and proves
// the selected baseline applies twice with an unchanged catalog.
// TestYugabyteServiceBaselineReapplyAndCompletion completes an interrupted baseline in both shapes a service database
// has on YugabyteDB: distributed, as every database created before layouts existed, and in its declared layout, as a
// release creates a missing one. Both receive the layout-rewritten baseline the yugabyte role applies, so the
// distributed shape also proves COLOCATION = false is accepted outside a colocated database.
func TestYugabyteServiceBaselineReapplyAndCompletion(t *testing.T) {
	requireDocker(t)
	name := ybStart(t, fmt.Sprintf("fw-sv-yb-complete-%d", time.Now().UnixNano()))
	probe := dockerSQLProbe{t: t, container: name, yb: true}
	ctx := context.Background()
	services, _ := yugabyteServiceDatabases(t)

	for _, source := range services {
		for _, shape := range []string{"distributed", "declared"} {
			t.Run(source+"/"+shape, func(t *testing.T) {
				probe := probe
				probe.t = t
				layout := ybDatabaseLayout(t, source)
				// Short suffixes keep <database>__baseline_check_<hex> within the 63-byte identifier limit.
				suffix := "d"
				if shape == "declared" {
					suffix = "c"
				}
				databaseName := source + "_cmp_" + suffix
				database := serviceDatabaseForTest(databaseName, source)
				if shape == "declared" {
					ybCreateDatabase(t, name, databaseName, layout)
				} else {
					probe.createDatabase(databaseName)
				}
				t.Cleanup(func() { ybDropDatabase(t, name, databaseName) })
				rewrite := func(sql string) string { return ybLayoutSQL(t, layout, sql) }
				applier := func(_ context.Context, databases []SchemaDatabase) error {
					for _, target := range databases {
						probe.apply(target.Name, rewrite(embeddedBaselineForTest(t, target.SourceName)))
					}
					return nil
				}

				probe.apply(databaseName, rewrite(interruptedBaselinePrefix(t, source)))
				state := probe.state(database)
				if !state.HasUnverifiedSchema() {
					t.Fatalf("interrupted baseline state = %+v, want tables without marker", state)
				}
				started := time.Now()
				if err := initializeServiceDatabases(ctx, probe, []SchemaDatabase{database}, map[string]ServiceDatabaseState{databaseName: state}, applier, randomReferenceSuffix, serviceBaselineTimeout); err != nil {
					t.Fatal(err)
				}
				if elapsed := time.Since(started); elapsed > serviceBaselineTimeout/2 {
					t.Fatalf("completing %s took %s, over half of its %s per-database bound", source, elapsed, serviceBaselineTimeout)
				} else {
					t.Logf("completed %s (%s) in %s (bound %s)", source, shape, elapsed.Round(time.Second), serviceBaselineTimeout)
				}
				if state := probe.state(database); !state.Initialized {
					t.Fatalf("completed database state = %+v, want initialized", state)
				}
				requireBaselineFloor(t, probe, databaseName)
				probe.requireNoBaselineReferences()
				ybRequireAllIndexesValid(t, name, databaseName)

				complete := probe.catalog(databaseName, source)
				probe.apply(databaseName, rewrite(embeddedBaselineForTest(t, source)))
				requireCatalogsEqual(t, "schema/"+source+".sql applied again on YugabyteDB", complete, probe.catalog(databaseName, source))
			})
		}
	}
}
