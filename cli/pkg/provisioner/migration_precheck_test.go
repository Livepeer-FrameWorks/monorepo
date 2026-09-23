package provisioner

import (
	"strings"
	"testing"
	"testing/fstest"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

func TestMigrationPrecheckSelectAcceptsOnlyOneReadOnlySelect(t *testing.T) {
	accepted := map[string]string{
		"trailing semicolon and comments": "-- rows\nSELECT a FROM s.t WHERE b IS NOT NULL ORDER BY a; -- done\n",
		"no semicolon":                    "SELECT a, count(*) FROM s.t GROUP BY a HAVING count(*) > 1",
		"string with keywords":            "SELECT 'insert into; for update' AS note FROM s.t",
		"substring for length":            "SELECT substring(a for 3) FROM s.t",
	}
	for name, src := range accepted {
		t.Run(name, func(t *testing.T) {
			selectSQL, err := migrationPrecheckSelect(src)
			if err != nil {
				t.Fatalf("migrationPrecheckSelect: %v", err)
			}
			if !strings.HasPrefix(selectSQL, "SELECT") || strings.HasSuffix(selectSQL, ";") {
				t.Fatalf("select = %q, want the statement without comments or its semicolon", selectSQL)
			}
		})
	}
	rejected := map[string]string{
		"two statements":  "SELECT 1; SELECT 2;",
		"empty":           "-- nothing\n",
		"update":          "UPDATE s.t SET a = 1",
		"cte":             "WITH x AS (DELETE FROM s.t RETURNING a) SELECT a FROM x",
		"select into":     "SELECT a INTO s.copy FROM s.t",
		"row lock":        "SELECT a FROM s.t FOR UPDATE",
		"key share lock":  "SELECT a FROM s.t FOR KEY SHARE",
		"no key lock":     "SELECT a FROM s.t FOR NO KEY UPDATE",
		"dollar body":     "SELECT $$x$$ FROM s.t",
		"trailing delete": "SELECT 1; DELETE FROM s.t",
	}
	for name, src := range rejected {
		t.Run(name, func(t *testing.T) {
			if _, err := migrationPrecheckSelect(src); err == nil {
				t.Fatalf("migrationPrecheckSelect accepted %q", src)
			}
		})
	}
}

func TestMigrationPrecheckReportQueryCountsBeforeLimit(t *testing.T) {
	query := migrationPrecheckReportQuery("SELECT a FROM s.t ORDER BY a")
	for _, want := range []string{
		"count(*) OVER () AS precheck_total",
		"row_to_json(precheck)::text AS precheck_row",
		"FROM (\nSELECT a FROM s.t ORDER BY a\n) AS precheck",
		"LIMIT 50",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("report query lacks %q:\n%s", want, query)
		}
	}
}

func TestValidateMigrationPrechecksRejectsOrphansFloorItemsAndNonSelects(t *testing.T) {
	fsys := fstest.MapFS{
		"prechecks/quartermaster/v0.3.0/expand/005_ok.notx.sql":      {Data: []byte("SELECT 1 FROM quartermaster.t")},
		"prechecks/quartermaster/v0.3.0/expand/006_missing.notx.sql": {Data: []byte("SELECT 1 FROM quartermaster.t")},
		"prechecks/quartermaster/v0.3.0/expand/007_write.sql":        {Data: []byte("DELETE FROM quartermaster.t")},
		"prechecks/quartermaster/v0.2.1/expand/001_old.sql":          {Data: []byte("SELECT 1")},
		"prechecks/quartermaster/v0.3.0/expand/README.md":            {Data: []byte("notes")},
	}
	migrations := []Migration{
		{Database: "quartermaster", Version: "v0.3.0", Phase: "expand", Path: "migrations/quartermaster/v0.3.0/expand/005_ok.notx.sql"},
		{Database: "quartermaster", Version: "v0.3.0", Phase: "expand", Path: "migrations/quartermaster/v0.3.0/expand/007_write.sql"},
		{Database: "quartermaster", Version: "v0.2.1", Phase: "expand", Path: "migrations/quartermaster/v0.2.1/expand/001_old.sql"},
	}
	issues := validateMigrationPrechecks(fsys, migrations)
	got := map[string]string{}
	for _, issue := range issues {
		got[issue.Path] += issue.Message + "\n"
	}
	want := map[string]string{
		"prechecks/quartermaster/v0.3.0/expand/006_missing.notx.sql": "precheck names no migration item",
		"prechecks/quartermaster/v0.3.0/expand/007_write.sql":        "single SELECT",
		"prechecks/quartermaster/v0.2.1/expand/001_old.sql":          "below the schema floor",
		"prechecks/quartermaster/v0.3.0/expand/README.md":            "must be .sql",
	}
	if len(got) != len(want) {
		t.Fatalf("issues = %#v, want one per path in %v", issues, want)
	}
	for path, message := range want {
		if !strings.Contains(got[path], message) {
			t.Fatalf("issues for %s = %q, want %q", path, got[path], message)
		}
	}
}

func TestEmbeddedMigrationPrechecksNameOfferedItems(t *testing.T) {
	migrations, err := discoverAllPostgresMigrationsForValidation()
	if err != nil {
		t.Fatal(err)
	}
	if issues := validateMigrationPrechecks(dbsql.Content, migrations); len(issues) > 0 {
		t.Fatalf("embedded prechecks: %v", (&MigrationValidationError{Issues: issues}).Error())
	}
}

// TestQuartermasterIdentityIndexItemsCarryTheirPrechecks proves the packaging both roles receive: the two unique
// fingerprint index items carry their precheck, keyed by migration file, and no other item carries one.
func TestQuartermasterIdentityIndexItemsCarryTheirPrechecks(t *testing.T) {
	want := map[string]string{
		"005_node_identity_unique_indexes.notx.sql":    "fingerprint_machine_sha256",
		"006_node_identity_macs_unique_index.notx.sql": "fingerprint_macs_sha256",
	}
	for _, engine := range []SQLEngine{SQLEnginePostgres, SQLEngineYugabyte} {
		t.Run(string(engine), func(t *testing.T) {
			items, err := BuildMigrationItemsForEngine([]SchemaDatabase{{Name: "quartermaster"}}, "expand", "v0.3.0", engine)
			if err != nil {
				t.Fatal(err)
			}
			found := 0
			for _, item := range items {
				filename, _ := item["filename"].(string)
				query, _ := item["precheck_query"].(string)
				path, _ := item["precheck_path"].(string)
				column, ok := want[filename]
				if !ok || item["version"] != "v0.3.0" {
					if query != "" || path != "" {
						t.Fatalf("%s/%s carries precheck %q", item["version"], filename, path)
					}
					continue
				}
				found++
				if path != "prechecks/quartermaster/v0.3.0/expand/"+filename {
					t.Fatalf("%s precheck path = %q", filename, path)
				}
				for _, fragment := range []string{"precheck_total", "precheck_row", "f.node_id", "n.cluster_id", "f.last_seen", "btrim(d." + column + ") <> ''", "HAVING count(*) > 1", "LIMIT 50"} {
					if !strings.Contains(query, fragment) {
						t.Fatalf("%s precheck lacks %q:\n%s", filename, fragment, query)
					}
				}
			}
			if found != len(want) {
				t.Fatalf("found %d precheck items, want %d", found, len(want))
			}
		})
	}
}
