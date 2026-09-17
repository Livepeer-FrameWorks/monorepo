package provisioner

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

type fakeServiceDatabaseProbe struct {
	tables string
	rows   map[string]bool
}

func (f fakeServiceDatabaseProbe) databaseNames(context.Context) (map[string]struct{}, error) {
	return nil, nil
}

func (f fakeServiceDatabaseProbe) schemaHasTables(context.Context, string, string) (bool, error) {
	return true, nil
}

func (f fakeServiceDatabaseProbe) scalarText(_ context.Context, _ string, query string) (string, error) {
	if query != initializationTablesQuery {
		panic("unexpected text query " + query)
	}
	return f.tables, nil
}

func (f fakeServiceDatabaseProbe) scalarBool(_ context.Context, _ string, query string) (bool, error) {
	for table, hasRows := range f.rows {
		if query == tableHasRowsQuery(table) {
			return hasRows, nil
		}
	}
	panic("row count probed for an absent table: " + query)
}

func (f fakeServiceDatabaseProbe) textRows(context.Context, string, string) ([]string, error) {
	panic("unexpected catalog read")
}

func (f fakeServiceDatabaseProbe) exec(context.Context, string, string) error {
	panic("unexpected statement")
}

func (fakeServiceDatabaseProbe) maintenanceDatabase() string { return "postgres" }

func (fakeServiceDatabaseProbe) yugabyte() bool { return false }

func TestProbeDatabaseInitialized(t *testing.T) {
	cases := []struct {
		name  string
		probe fakeServiceDatabaseProbe
		want  bool
	}{
		{"baseline marker row", fakeServiceDatabaseProbe{tables: "_schema_baseline", rows: map[string]bool{"_schema_baseline": true}}, true},
		{"ledger rows without marker", fakeServiceDatabaseProbe{tables: "_migrations,_schema_baseline", rows: map[string]bool{"_migrations": true, "_schema_baseline": false}}, true},
		{"empty marker and ledger", fakeServiceDatabaseProbe{tables: "_migrations,_schema_baseline", rows: map[string]bool{"_migrations": false, "_schema_baseline": false}}, false},
		{"neither table", fakeServiceDatabaseProbe{tables: ""}, false},
	}
	if query := tableHasRowsQuery("_schema_baseline"); !strings.Contains(query, "floor <> '"+baselineVerificationPendingFloor+"'") {
		t.Fatalf("marker probe %q must ignore the verification-pending marker", query)
	}
	for _, tc := range cases {
		got, err := probeDatabaseInitialized(context.Background(), tc.probe, "lookout")
		if err != nil || got != tc.want {
			t.Fatalf("%s: initialized = %v, %v; want %v", tc.name, got, err, tc.want)
		}
	}
}

func TestServiceDatabasesWithBaselineSelectsServiceDatabases(t *testing.T) {
	got, err := ServiceDatabasesWithBaseline([]SchemaDatabase{
		{Name: "lookout"},
		{Name: "chatwoot", Owner: "chatwoot"},
		{Name: "foghorn_eu", Owner: "foghorn_eu", SourceName: "foghorn", Schema: "foghorn"},
		{Name: "  "},
		{Name: "purser", Owner: "billing", RuntimeRole: "billing_app"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []SchemaDatabase{
		{Name: "foghorn_eu", Owner: "foghorn_eu", RuntimeRole: "foghorn_eu_runtime", SourceName: "foghorn", Schema: "foghorn"},
		{Name: "lookout", Owner: "lookout", RuntimeRole: "lookout_runtime", SourceName: "lookout", Schema: "lookout"},
		{Name: "purser", Owner: "billing", RuntimeRole: "billing_app", SourceName: "purser", Schema: "purser"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("service databases = %+v, want %+v", got, want)
	}
}

func TestPlanServiceDatabaseBootstrap(t *testing.T) {
	databases := []SchemaDatabase{
		{Name: "commodore", Schema: "commodore"},
		{Name: "lookout", Schema: "lookout"},
		{Name: "skipper", Schema: "skipper"},
	}
	t.Run("missing and empty databases need bootstrap; populated ones never do", func(t *testing.T) {
		pending, err := PlanServiceDatabaseBootstrap(databases, map[string]ServiceDatabaseState{
			"commodore": {Exists: true, HasTables: true, Initialized: true},
			"lookout":   {},
			"skipper":   {Exists: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := schemaDatabaseNamesForTest(pending); !reflect.DeepEqual(got, []string{"lookout", "skipper"}) {
			t.Fatalf("pending = %v, want lookout (absent) and skipper (no tables)", got)
		}
	})
	t.Run("a fully provisioned cluster needs nothing", func(t *testing.T) {
		pending, err := PlanServiceDatabaseBootstrap(databases, map[string]ServiceDatabaseState{
			"commodore": {Exists: true, HasTables: true, Initialized: true},
			"lookout":   {Exists: true, HasTables: true, Initialized: true},
			"skipper":   {Exists: true, HasTables: true, Initialized: true},
		})
		if err != nil || len(pending) != 0 {
			t.Fatalf("pending = %v, err = %v", pending, err)
		}
	})
	t.Run("tables without baseline marker or ledger rows are pending completion", func(t *testing.T) {
		states := map[string]ServiceDatabaseState{
			"commodore": {Exists: true, HasTables: true, Initialized: true},
			"lookout":   {Exists: true, HasTables: true},
			"skipper":   {Exists: true, HasTables: true, Initialized: true},
		}
		pending, err := PlanServiceDatabaseBootstrap(databases, states)
		if err != nil {
			t.Fatal(err)
		}
		if got := schemaDatabaseNamesForTest(pending); !reflect.DeepEqual(got, []string{"lookout"}) {
			t.Fatalf("pending = %v, want lookout", got)
		}
		if !states["lookout"].HasUnverifiedSchema() || states["commodore"].HasUnverifiedSchema() || (ServiceDatabaseState{Exists: true}).HasUnverifiedSchema() {
			t.Fatal("only a database with tables and no marker or ledger rows has an unverified schema")
		}
	})
	t.Run("an unprobed database fails closed", func(t *testing.T) {
		_, err := PlanServiceDatabaseBootstrap(databases, map[string]ServiceDatabaseState{
			"commodore": {Exists: true, HasTables: true, Initialized: true},
		})
		if err == nil || !strings.Contains(err.Error(), "lookout") {
			t.Fatalf("err = %v, want refusal naming the unprobed database", err)
		}
	})
}

func TestServiceDatabaseProbeParsing(t *testing.T) {
	names := parseDatabaseNameLines("postgres\ncommodore\n\n  lookout  \n")
	if _, ok := names["lookout"]; !ok || len(names) != 3 {
		t.Fatalf("names = %v", names)
	}
	if got, err := parsePsqlBoolScalar("t\n"); err != nil || !got {
		t.Fatalf("t = %v, %v", got, err)
	}
	if got, err := parsePsqlBoolScalar("f"); err != nil || got {
		t.Fatalf("f = %v, %v", got, err)
	}
	if _, err := parsePsqlBoolScalar("ERROR"); err == nil {
		t.Fatal("unparseable probe output must fail closed")
	}
	if query := schemaHasTablesQuery("lookout"); !strings.Contains(query, "table_schema = 'lookout'") || !strings.Contains(query, "BASE TABLE") {
		t.Fatalf("schema probe = %q", query)
	}
}

func schemaDatabaseNamesForTest(databases []SchemaDatabase) []string {
	out := make([]string, 0, len(databases))
	for _, database := range databases {
		out = append(out, database.Name)
	}
	return out
}
