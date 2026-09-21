//go:build schema_verify

package provisioner

import (
	"strings"
	"testing"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// TestPostgresServiceDatabaseProbeQueries runs the release-path probe queries
// on a real engine: an empty database, a database created from the current
// baseline, and a database whose service schema was populated by hand.
func TestPostgresServiceDatabaseProbeQueries(t *testing.T) {
	requireDocker(t)
	name := uniqueContainerName("fw-sv-pg-service-db-probe")
	pgStart(t, name)

	query := func(db, sql string) string {
		t.Helper()
		out, err := docker(t, "", "exec", name, "psql", "-U", "postgres", "-d", db, "-X", "-v", "ON_ERROR_STOP=1", "-tAc", sql)
		if err != nil {
			t.Fatalf("%s: %s: %v\n%s", db, sql, err, out)
		}
		return strings.TrimSpace(out)
	}

	pgCreateDB(t, name, "lookout")
	if got := query("lookout", schemaHasTablesQuery("lookout")); got != "f" {
		t.Fatalf("empty database has tables = %q", got)
	}
	if got := query("lookout", initializationTablesQuery); got != "" {
		t.Fatalf("empty database initialization tables = %q", got)
	}

	baseline, err := dbsql.Content.ReadFile("schema/lookout.sql")
	if err != nil {
		t.Fatal(err)
	}
	pgApply(t, name, "lookout", string(baseline))
	if got := query("lookout", schemaHasTablesQuery("lookout")); got != "t" {
		t.Fatalf("baseline database has tables = %q", got)
	}
	tables := query("lookout", initializationTablesQuery)
	if !strings.Contains(tables, "_schema_baseline") {
		t.Fatalf("baseline database initialization tables = %q, want _schema_baseline", tables)
	}
	if got := query("lookout", tableHasRowsQuery("_schema_baseline")); got != "t" {
		t.Fatalf("baseline marker has rows = %q", got)
	}

	pgCreateDB(t, name, "partial")
	pgApply(t, name, "partial", "CREATE SCHEMA lookout; CREATE TABLE lookout.incidents (id integer PRIMARY KEY);")
	if got := query("partial", schemaHasTablesQuery("lookout")); got != "t" {
		t.Fatalf("hand-populated database has tables = %q", got)
	}
	if got := query("partial", initializationTablesQuery); got != "" {
		t.Fatalf("hand-populated database initialization tables = %q, want none", got)
	}
}
