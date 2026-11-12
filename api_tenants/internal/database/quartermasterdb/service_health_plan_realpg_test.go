//go:build schema_verify

package quartermasterdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
)

func TestServiceHealthLookupUsesOrderedIndex_RealPG(t *testing.T) {
	assertServiceHealthLookupUsesOrderedIndex(t, startQuartermasterQueryCatalogRealPG(t), false)
}

func TestServiceHealthLookupUsesOrderedIndex_RealYugabyte(t *testing.T) {
	assertServiceHealthLookupUsesOrderedIndex(t, startQuartermasterQueryCatalogRealYugabyte(t), true)
}

type serviceHealthPlanCapture struct {
	*sql.DB
	query string
	args  []any
}

func (c *serviceHealthPlanCapture) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c.query, c.args = query, args
	return c.DB.QueryContext(ctx, query, args...)
}

func assertServiceHealthLookupUsesOrderedIndex(t *testing.T, db *sql.DB, distributed bool) {
	t.Helper()
	for _, query := range []string{
		`INSERT INTO quartermaster.services(service_id,name,plane,type,protocol)
 SELECT 'health-plan-'||n,'Health plan','control','api','http' FROM generate_series(1,4)n`,
		`INSERT INTO quartermaster.infrastructure_clusters(cluster_id,cluster_name,cluster_type,base_url)
 VALUES('health-plan','Health plan','edge','https://health.example.com')`,
		`INSERT INTO quartermaster.service_instances(instance_id,service_id,cluster_id,port)
 SELECT 'health-plan-'||n,'health-plan-'||(1+n%4),'health-plan',18000 FROM generate_series(1,1600)n`,
		`ANALYZE quartermaster.service_instances`,
	} {
		if _, err := db.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	capture := &serviceHealthPlanCapture{DB: db}
	rows, err := New(capture).ListServiceHealth(t.Context(), "health-plan-1")
	if err != nil || len(rows) != 400 {
		t.Fatalf("service health rows=%d: %v", len(rows), err)
	}
	explain := "EXPLAIN (ANALYZE, FORMAT JSON) "
	if distributed {
		explain = "EXPLAIN (ANALYZE, DIST, FORMAT JSON) "
	}
	var raw []byte
	if err := db.QueryRowContext(t.Context(), explain+capture.query, capture.args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	type node struct {
		Kind      string            `json:"Node Type"`
		Index     string            `json:"Index Name"`
		Relation  string            `json:"Relation Name"`
		Rows      float64           `json:"Actual Rows"`
		TableRows float64           `json:"Storage Table Rows Scanned"`
		IndexRows float64           `json:"Storage Index Rows Scanned"`
		Plans     []json.RawMessage `json:"Plans"`
	}
	var roots []struct{ Plan json.RawMessage }
	if err := json.Unmarshal(raw, &roots); err != nil {
		t.Fatal(err)
	}
	indexed := false
	var inspect func(json.RawMessage)
	inspect = func(data json.RawMessage) {
		var n node
		if err := json.Unmarshal(data, &n); err != nil {
			t.Fatal(err)
		}
		indexed = indexed || n.Index != ""
		if n.Relation == "service_instances" {
			if n.Kind == "Seq Scan" || n.TableRows+n.IndexRows > 800 {
				t.Errorf("health lookup scans unrelated services: %s", raw)
			}
		}
		for _, child := range n.Plans {
			inspect(child)
		}
	}
	for _, root := range roots {
		inspect(root.Plan)
	}
	if !indexed {
		t.Errorf("health lookup scans without an index: %s", raw)
	}
}
