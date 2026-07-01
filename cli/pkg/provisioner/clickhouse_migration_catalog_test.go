package provisioner

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// TestClickHouseMigrationCatalogCoverage is the safety net for the hand-curated
// PeriscopeMigrationCatalog: it re-derives every object from the embedded
// periscope.sql and fails if the catalog drifts (a table/MV/view added or removed
// without updating the catalog, or an MV misclassified as insert-trigger vs
// refreshable). This guarantees the migrator can never silently mishandle a new
// object.
func TestClickHouseMigrationCatalogCoverage(t *testing.T) {
	data, err := dbsql.Content.ReadFile("clickhouse/periscope.sql")
	if err != nil {
		t.Fatalf("read embedded periscope.sql: %v", err)
	}
	sql := string(data)

	tableRe := regexp.MustCompile(`CREATE TABLE IF NOT EXISTS ([a-z0-9_]+)`)
	mvRe := regexp.MustCompile(`CREATE MATERIALIZED VIEW IF NOT EXISTS ([a-z0-9_]+)`)
	viewRe := regexp.MustCompile(`CREATE VIEW IF NOT EXISTS ([a-z0-9_]+)`)

	var wantTables, wantViews, wantInsertMVs, wantRefreshMVs []string
	for _, m := range tableRe.FindAllStringSubmatch(sql, -1) {
		// Infra tables (leading underscore, e.g. _schema_baseline) are node-local
		// identity/bookkeeping, not analytics data — intentionally NOT in the
		// cross-host migration catalog and never copied between nodes.
		if strings.HasPrefix(m[1], "_") {
			continue
		}
		wantTables = append(wantTables, m[1])
	}
	for _, m := range viewRe.FindAllStringSubmatch(sql, -1) {
		wantViews = append(wantViews, m[1])
	}
	// Classify each MV by whether its statement body (up to the next CREATE) is a
	// refreshable (REFRESH EVERY) view — authoritative, derived from the DDL itself.
	mvIdx := mvRe.FindAllStringSubmatchIndex(sql, -1)
	for i, loc := range mvIdx {
		name := sql[loc[2]:loc[3]]
		bodyEnd := len(sql)
		if i+1 < len(mvIdx) {
			bodyEnd = mvIdx[i+1][0]
		} else if next := strings.Index(sql[loc[1]:], "CREATE "); next >= 0 {
			bodyEnd = loc[1] + next
		}
		if strings.Contains(sql[loc[0]:bodyEnd], "REFRESH EVERY") {
			wantRefreshMVs = append(wantRefreshMVs, name)
		} else {
			wantInsertMVs = append(wantInsertMVs, name)
		}
	}

	assertSetEqual(t, "Tables", PeriscopeMigrationCatalog.Tables, wantTables)
	assertSetEqual(t, "InsertTriggerMVs", PeriscopeMigrationCatalog.InsertTriggerMVs, wantInsertMVs)
	assertSetEqual(t, "RefreshableMVs", PeriscopeMigrationCatalog.RefreshableMVs, wantRefreshMVs)
	assertSetEqual(t, "Views", PeriscopeMigrationCatalog.Views, wantViews)
}

func assertSetEqual(t *testing.T, label string, got, want []string) {
	t.Helper()
	g := slices.Clone(got)
	w := slices.Clone(want)
	slices.Sort(g)
	slices.Sort(w)
	if slices.Equal(g, w) {
		return
	}
	var missing, extra []string // in schema but not catalog / in catalog but not schema
	for _, n := range w {
		if !slices.Contains(g, n) {
			missing = append(missing, n)
		}
	}
	for _, n := range g {
		if !slices.Contains(w, n) {
			extra = append(extra, n)
		}
	}
	t.Errorf("%s: catalog drift vs periscope.sql\n  missing from catalog: %v\n  stale in catalog: %v", label, missing, extra)
}
