package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestCatalogRetainsPostgresWhenConnectionsAreRebound(t *testing.T) {
	source := `package p
import database "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
func mixed(ch database.ClickHouseConn, db *sql.DB, openClickHouse func() (database.ClickHouseConn, error)) {
 db.QueryContext(ctx, "SELECT id FROM periscope.metering_events")
 ch.QueryContext(ctx, "SELECT groupUniqArrayMerge(clusters) FROM periscope.viewer_lifecycle_serving_attribution")
 warehouse, err := openClickHouse()
 warehouse.QueryContext(ctx, "SELECT toUUID(?) FROM periscope.viewer_connection_events", tenant)
 {
  warehouse := db
  warehouse.QueryContext(ctx, "SELECT id FROM periscope.processed_metering_events")
 }
 ch = db
 ch.QueryContext(ctx, "SELECT id FROM periscope.auth_event_outbox")
 shadow := func(openClickHouse func() (*sql.DB, error)) {
  pg, err := openClickHouse()
  pg.QueryContext(ctx, "SELECT id FROM periscope.processed_domain_events")
 }
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	b := &catalogBuilder{constCache: map[string]map[string]ast.Expr{"": {}}, fset: fset}
	var queries []query
	b.scanParsed(file, "", func(q query) { queries = append(queries, q) })
	for _, table := range []string{"metering_events", "processed_metering_events", "auth_event_outbox", "processed_domain_events"} {
		found := false
		for _, q := range queries {
			found = found || strings.HasSuffix(q.SQL, "periscope."+table)
		}
		if !found {
			t.Errorf("PostgreSQL query disappeared after shadowing/rebinding a ClickHouse connection: %s", table)
		}
	}
}

func TestCatalogSeparatesClickHouseConnections(t *testing.T) {
	source := `package p
import database "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
func mixed(ch database.ClickHouseConn, db *sql.DB, openClickHouse func() (database.ClickHouseConn, error)) {
 db.QueryContext(ctx, "SELECT id FROM periscope.metering_events")
 ch.QueryContext(ctx, "SELECT groupUniqArrayMerge(clusters) FROM periscope.viewer_lifecycle_serving_attribution")
 warehouse, err := openClickHouse()
 warehouse.QueryContext(ctx, "SELECT toUUID(?) FROM periscope.viewer_connection_events", tenant)
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	b := &catalogBuilder{constCache: map[string]map[string]ast.Expr{"": {}}, fset: fset}
	var queries []query
	b.scanParsed(file, "", func(q query) { queries = append(queries, q) })
	if len(queries) != 1 || queries[0].SQL != "SELECT id FROM periscope.metering_events" {
		t.Fatalf("Yugabyte catalog must retain PostgreSQL SQL and exclude typed ClickHouse calls: %+v", queries)
	}
}

func TestServingAttributionQueriesUseClickHouseContracts(t *testing.T) {
	b, err := newCatalogBuilder("../..")
	if err != nil {
		t.Fatal(err)
	}
	var queries []query
	if err := b.scanFile("../../api_analytics_query/internal/datamigrations/viewer_serving_attribution.go", func(q query) {
		queries = append(queries, q)
	}); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 0 {
		t.Fatalf("ClickHouse backfill must not run against Yugabyte: %+v", queries)
	}
}
