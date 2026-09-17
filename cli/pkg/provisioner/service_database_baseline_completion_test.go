package provisioner

import (
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testReferenceSuffix = "0123456789abcdef"

type completionProbe struct {
	existing   map[string]struct{}
	catalogs   map[string]map[string]string
	floors     map[string]string
	statements []string
	// contextErrs holds ctx.Err() at the time of each recorded statement.
	contextErrs []error
}

func (p *completionProbe) databaseNames(context.Context) (map[string]struct{}, error) {
	return p.existing, nil
}

func (p *completionProbe) schemaHasTables(context.Context, string, string) (bool, error) {
	panic("unexpected table probe")
}

func (p *completionProbe) scalarText(_ context.Context, database, query string) (string, error) {
	switch {
	case query == baselineMarkerQuery:
		return p.floors[database], nil
	case strings.HasPrefix(query, "SELECT format('CREATE DATABASE"):
		return `CREATE DATABASE lookout__baseline_check_` + testReferenceSuffix + ` TEMPLATE template0 ENCODING 'UTF8'`, nil
	}
	panic("unexpected text query " + query)
}

func (p *completionProbe) scalarBool(_ context.Context, _ string, query string) (bool, error) {
	if strings.Contains(query, "floor = '"+baselineVerificationPendingFloor+"'") {
		return true, nil
	}
	panic("unexpected bool query " + query)
}

func (p *completionProbe) textRows(_ context.Context, database, query string) ([]string, error) {
	if !strings.Contains(query, "encode(convert_to(object") {
		panic("unexpected rows query " + query)
	}
	var rows []string
	for object, definition := range p.catalogs[database] {
		rows = append(rows, hex.EncodeToString([]byte(object+"\t"+definition)))
	}
	return rows, nil
}

func (p *completionProbe) exec(ctx context.Context, database, statement string) error {
	p.statements = append(p.statements, database+": "+statement)
	p.contextErrs = append(p.contextErrs, ctx.Err())
	return ctx.Err()
}

func (*completionProbe) maintenanceDatabase() string { return "postgres" }

func (*completionProbe) yugabyte() bool { return false }

func (p *completionProbe) indexOf(t *testing.T, prefix string) int {
	t.Helper()
	for i, statement := range p.statements {
		if strings.HasPrefix(statement, prefix) {
			return i
		}
	}
	return -1
}

func fixedReferenceSuffix() (string, error) { return testReferenceSuffix, nil }

func lookoutCatalog() map[string]string {
	return map[string]string{
		"schema lookout":                  "present",
		"table lookout.incidents":         "kind=r persistence=p row_security=false force_row_security=false",
		"column lookout.incidents.id":     "uuid NOT NULL",
		"index lookout.incidents_pkey":    "CREATE UNIQUE INDEX incidents_pkey ON lookout.incidents USING btree (id) valid=true ready=true",
		"constraint lookout.incidents.pk": "p PRIMARY KEY (id)",
	}
}

type recordedApply struct {
	calls [][]SchemaDatabase
	err   error
}

func (r *recordedApply) apply(_ context.Context, databases []SchemaDatabase) error {
	r.calls = append(r.calls, append([]SchemaDatabase(nil), databases...))
	return r.err
}

var completionDatabases = []SchemaDatabase{
	{Name: "commodore", Owner: "commodore", RuntimeRole: "commodore_runtime", SourceName: "commodore", Schema: "commodore"},
	{Name: "lookout", Owner: "lookout", RuntimeRole: "lookout_runtime", SourceName: "lookout", Schema: "lookout"},
}

func TestInitializeServiceDatabasesAppliesDirectlyWithoutUnverifiedSchemas(t *testing.T) {
	probe := &completionProbe{}
	applied := &recordedApply{}
	err := initializeServiceDatabases(context.Background(), probe, completionDatabases, map[string]ServiceDatabaseState{
		"commodore": {},
		"lookout":   {Exists: true},
	}, applied.apply, fixedReferenceSuffix, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]SchemaDatabase{completionDatabases[:1], completionDatabases[1:]}
	if !reflect.DeepEqual(applied.calls, want) || len(probe.statements) != 0 {
		t.Fatalf("apply calls = %+v statements = %v; absent and empty databases are applied one at a time and need no reference", applied.calls, probe.statements)
	}
}

func TestInitializeServiceDatabasesCompletesAndVerifiesUnverifiedSchema(t *testing.T) {
	reference := "lookout__baseline_check_" + testReferenceSuffix
	stale := "lookout__baseline_check_aaaaaaaaaaaaaaaa"
	probe := &completionProbe{
		existing: map[string]struct{}{"lookout": {}, stale: {}, "lookout_archive": {}, "commodore__baseline_check_bbbbbbbbbbbbbbbb": {}},
		catalogs: map[string]map[string]string{"lookout": lookoutCatalog(), reference: lookoutCatalog()},
		floors:   map[string]string{reference: "v0.3.0"},
	}
	applied := &recordedApply{}
	err := initializeServiceDatabases(context.Background(), probe, completionDatabases, map[string]ServiceDatabaseState{
		"commodore": {},
		"lookout":   {Exists: true, HasTables: true},
	}, applied.apply, fixedReferenceSuffix, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	wantTargets := [][]SchemaDatabase{
		completionDatabases[:1],
		{
			{Name: "lookout", Owner: "lookout", RuntimeRole: "lookout_runtime", SourceName: "lookout", Schema: "lookout", ReapplyBaseline: true},
			{Name: reference, Owner: "lookout", RuntimeRole: "lookout_runtime", SourceName: "lookout", Schema: "lookout", ReapplyBaseline: true},
		},
	}
	if !reflect.DeepEqual(applied.calls, wantTargets) {
		t.Fatalf("apply targets = %+v, want %+v", applied.calls, wantTargets)
	}

	dropStale := probe.indexOf(t, `postgres: DROP DATABASE IF EXISTS "`+stale+`"`)
	markTable := probe.indexOf(t, "lookout: CREATE TABLE IF NOT EXISTS public._schema_baseline")
	markPending := probe.indexOf(t, "lookout: INSERT INTO public._schema_baseline (floor) SELECT '"+baselineVerificationPendingFloor+"'")
	create := probe.indexOf(t, "postgres: CREATE DATABASE lookout__baseline_check_"+testReferenceSuffix)
	dropReference := probe.indexOf(t, `postgres: DROP DATABASE IF EXISTS "`+reference+`"`)
	finalize := probe.indexOf(t, "lookout: UPDATE public._schema_baseline SET floor = 'v0.3.0'")
	if dropStale < 0 || markTable < 0 || markPending < 0 || create < 0 || dropReference < 0 || finalize < 0 ||
		dropStale >= markPending || markTable >= markPending || markPending >= create || create >= dropReference || dropReference >= finalize {
		t.Fatalf("statements out of order (stale drop -> pending marker -> reference -> drop reference -> finalize):\n%s", strings.Join(probe.statements, "\n"))
	}
	for _, statement := range probe.statements {
		if strings.Contains(statement, "lookout_archive") || strings.Contains(statement, "commodore__baseline_check_") {
			t.Fatalf("dropped a database that is not a reference for lookout: %s", statement)
		}
	}
}

func TestInitializeServiceDatabasesRefusesDivergentSchema(t *testing.T) {
	reference := "lookout__baseline_check_" + testReferenceSuffix
	actual := lookoutCatalog()
	actual["column lookout.incidents.extra"] = "text"
	actual["column lookout.incidents.id"] = "bigint NOT NULL"
	probe := &completionProbe{
		existing: map[string]struct{}{"lookout": {}},
		catalogs: map[string]map[string]string{"lookout": actual, reference: lookoutCatalog()},
		floors:   map[string]string{reference: "v0.3.0"},
	}
	applied := &recordedApply{}
	err := initializeServiceDatabases(context.Background(), probe, completionDatabases[1:], map[string]ServiceDatabaseState{
		"lookout": {Exists: true, HasTables: true},
	}, applied.apply, fixedReferenceSuffix, time.Hour)
	if err == nil {
		t.Fatal("divergent schema was accepted")
	}
	for _, want := range []string{
		`service database "lookout"`,
		"schema/lookout.sql",
		"2 difference(s)",
		"  column lookout.incidents.extra\n    expected: (absent)\n    actual:   text",
		"  column lookout.incidents.id\n    expected: uuid NOT NULL\n    actual:   bigint NOT NULL",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal lacks %q:\n%s", want, err)
		}
	}
	if probe.indexOf(t, "lookout: UPDATE public._schema_baseline") >= 0 {
		t.Fatal("a divergent database must keep its verification-pending marker")
	}
	if probe.indexOf(t, `postgres: DROP DATABASE IF EXISTS "`+reference+`"`) < 0 {
		t.Fatalf("reference database was not dropped:\n%s", strings.Join(probe.statements, "\n"))
	}
}

func TestInitializeServiceDatabasesDropsReferenceWhenApplyFails(t *testing.T) {
	reference := "lookout__baseline_check_" + testReferenceSuffix
	probe := &completionProbe{existing: map[string]struct{}{"lookout": {}}}
	applyErr := errors.New("ansible failed")
	applied := &recordedApply{err: applyErr}
	err := initializeServiceDatabases(context.Background(), probe, completionDatabases[1:], map[string]ServiceDatabaseState{
		"lookout": {Exists: true, HasTables: true},
	}, applied.apply, fixedReferenceSuffix, time.Hour)
	if !errors.Is(err, applyErr) {
		t.Fatalf("err = %v, want apply failure", err)
	}
	last := probe.statements[len(probe.statements)-1]
	if last != `postgres: DROP DATABASE IF EXISTS "`+reference+`"` || probe.indexOf(t, "lookout: UPDATE") >= 0 {
		t.Fatalf("statements = %v; a failed apply must drop the reference and leave the database unverified", probe.statements)
	}
}

func TestInitializeServiceDatabasesRefusesReferenceWithoutMarker(t *testing.T) {
	reference := "lookout__baseline_check_" + testReferenceSuffix
	probe := &completionProbe{
		existing: map[string]struct{}{"lookout": {}},
		catalogs: map[string]map[string]string{"lookout": lookoutCatalog(), reference: lookoutCatalog()},
	}
	err := initializeServiceDatabases(context.Background(), probe, completionDatabases[1:], map[string]ServiceDatabaseState{
		"lookout": {Exists: true, HasTables: true},
	}, (&recordedApply{}).apply, fixedReferenceSuffix, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "wrote no _schema_baseline marker") || probe.indexOf(t, "lookout: UPDATE") >= 0 {
		t.Fatalf("err = %v statements = %v", err, probe.statements)
	}
}

func TestInitializeServiceDatabasesTimedOutCompletionFailsClosed(t *testing.T) {
	reference := "lookout__baseline_check_" + testReferenceSuffix
	probe := &completionProbe{existing: map[string]struct{}{"lookout": {}}}
	const bound = 50 * time.Millisecond
	var applyCalls int
	var applyDeadline, applyStarted time.Time
	blocking := func(ctx context.Context, _ []SchemaDatabase) error {
		applyCalls++
		applyStarted = time.Now()
		applyDeadline, _ = ctx.Deadline()
		<-ctx.Done()
		return errors.New("ansible killed")
	}
	pending := []SchemaDatabase{completionDatabases[1], completionDatabases[0]}
	err := initializeServiceDatabases(context.Background(), probe, pending, map[string]ServiceDatabaseState{
		"lookout":   {Exists: true, HasTables: true},
		"commodore": {},
	}, blocking, fixedReferenceSuffix, bound)
	if err == nil || !strings.Contains(err.Error(), "lookout: baseline completion exceeded its 50ms per-database bound") {
		t.Fatalf("err = %v, want the per-database bound named", err)
	}
	if applyDeadline.IsZero() || applyDeadline.Sub(applyStarted) > bound {
		t.Fatalf("apply deadline = %v started = %v; the completion must run within its own bound", applyDeadline, applyStarted)
	}
	dropped := probe.indexOf(t, `postgres: DROP DATABASE IF EXISTS "`+reference+`"`)
	if dropped < 0 || probe.contextErrs[dropped] != nil {
		t.Fatalf("statements = %v context errors = %v; the reference must be dropped with a live context after the timeout", probe.statements, probe.contextErrs)
	}
	if probe.indexOf(t, "lookout: INSERT INTO public._schema_baseline (floor) SELECT '"+baselineVerificationPendingFloor+"'") < 0 || probe.indexOf(t, "lookout: UPDATE") >= 0 {
		t.Fatalf("statements = %v; a timed-out completion must keep the verification-pending marker", probe.statements)
	}
	if applyCalls != 1 {
		t.Fatalf("apply calls = %d; a timed-out completion stops before later databases", applyCalls)
	}
}

func TestInitializeServiceDatabasesBoundsEachDatabaseSeparately(t *testing.T) {
	const bound = time.Hour
	var remaining []time.Duration
	slow := func(ctx context.Context, _ []SchemaDatabase) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("baseline apply ran without a deadline")
		}
		remaining = append(remaining, time.Until(deadline))
		time.Sleep(100 * time.Millisecond)
		return nil
	}
	parent, cancel := context.WithTimeout(context.Background(), 2*bound)
	defer cancel()
	err := initializeServiceDatabases(parent, nil, completionDatabases, map[string]ServiceDatabaseState{
		"commodore": {},
		"lookout":   {},
	}, slow, fixedReferenceSuffix, bound)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 2 || remaining[1] < bound-50*time.Millisecond {
		t.Fatalf("remaining time per apply = %v; each database must start with its full bound", remaining)
	}
}

func TestInitializeServiceDatabasesReportsCallerCancellationUnwrapped(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancelled := func(context.Context, []SchemaDatabase) error {
		cancel()
		return context.Canceled
	}
	err := initializeServiceDatabases(parent, nil, completionDatabases[:1], map[string]ServiceDatabaseState{"commodore": {}}, cancelled, fixedReferenceSuffix, time.Hour)
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "per-database bound") {
		t.Fatalf("err = %v; a cancelled caller is not a per-database timeout", err)
	}
}

func TestParseServiceSchemaCatalog(t *testing.T) {
	row := func(s string) string { return hex.EncodeToString([]byte(s)) }
	catalog, err := parseServiceSchemaCatalog([]string{row("table s.t\tkind=r"), row("column s.t.c\ttext | with pipe")})
	if err != nil || catalog["column s.t.c"] != "text | with pipe" || len(catalog) != 2 {
		t.Fatalf("catalog = %v, err = %v", catalog, err)
	}
	for name, rows := range map[string][]string{
		"not hex":      {"zz"},
		"no separator": {row("table s.t")},
		"duplicate":    {row("table s.t\tkind=r"), row("table s.t\tkind=p")},
	} {
		if _, err := parseServiceSchemaCatalog(rows); err == nil {
			t.Errorf("%s: malformed catalog accepted", name)
		}
	}
	if query := serviceSchemaCatalogQuery("lookout"); !strings.Contains(query, "nspname = 'lookout'") || strings.Contains(query, "{{schema}}") {
		t.Fatalf("catalog query not bound to the schema:\n%s", query)
	}
}

func TestBaselineReferenceNames(t *testing.T) {
	name, err := baselineReferenceName("foghorn_eu", testReferenceSuffix)
	if err != nil || name != "foghorn_eu__baseline_check_"+testReferenceSuffix {
		t.Fatalf("name = %q, err = %v", name, err)
	}
	if name, err := baselineReferenceName(strings.Repeat("a", 30), testReferenceSuffix); err != nil || len(name) != 63 {
		t.Fatalf("63-byte reference name = %q, %v", name, err)
	}
	if _, err := baselineReferenceName(strings.Repeat("a", 31), testReferenceSuffix); err == nil {
		t.Fatal("a reference name longer than 63 bytes would be truncated by the engine")
	}
	stale := staleBaselineReferences("foghorn", map[string]struct{}{
		"foghorn__baseline_check_" + testReferenceSuffix:    {},
		"foghorn_eu__baseline_check_" + testReferenceSuffix: {},
		"foghorn__baseline_check_short":                     {},
		"foghorn":                                           {},
	})
	if !reflect.DeepEqual(stale, []string{"foghorn__baseline_check_" + testReferenceSuffix}) {
		t.Fatalf("stale = %v", stale)
	}
	if suffix, err := randomReferenceSuffix(); err != nil || len(suffix) != 16 {
		t.Fatalf("suffix = %q, err = %v", suffix, err)
	}
}
