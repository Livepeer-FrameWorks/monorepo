//go:build schema_verify

package provisioner

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// precheckEngine runs SQL scripts through the engine's own client, one statement at a time in autocommit mode unless
// the script opens a transaction, as the migration roles send them.
type precheckEngine struct {
	engine SQLEngine
	run    func(t *testing.T, database, script string) (string, error)
}

type precheckRow struct {
	NodeID        string  `json:"node_id"`
	ClusterID     *string `json:"cluster_id"`
	LastSeen      *string `json:"last_seen"`
	FingerprintID string  `json:"fingerprint_id"`
}

// precheckOutcome is what one pending item did: the rows its precheck refused it with, or nil when it applied.
type precheckOutcome struct {
	total int
	rows  []precheckRow
}

// applyPrecheckedItem runs one migration item the way roles/{postgres,yugabyte}/tasks/migrate_item.yml does: the
// precheck in a READ ONLY transaction, a refusal before any statement when it returns rows, and otherwise the item's
// statements, each in autocommit for a .notx.sql item.
func applyPrecheckedItem(t *testing.T, engine precheckEngine, database string, item map[string]any) *precheckOutcome {
	t.Helper()
	if query, _ := item["precheck_query"].(string); query != "" {
		out, err := engine.run(t, database, "BEGIN;\nSET TRANSACTION READ ONLY;\n"+query+";\nCOMMIT;\n")
		if err != nil {
			t.Fatalf("precheck %s: %v", item["precheck_path"], err)
		}
		outcome := &precheckOutcome{}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if line == "" {
				continue
			}
			total, row, ok := strings.Cut(line, "\t")
			if !ok {
				t.Fatalf("precheck %s printed %q, want total<TAB>row", item["precheck_path"], line)
			}
			n, err := strconv.Atoi(total)
			if err != nil {
				t.Fatalf("precheck total %q: %v", total, err)
			}
			outcome.total = n
			var parsed precheckRow
			if err := json.Unmarshal([]byte(row), &parsed); err != nil {
				t.Fatalf("precheck row %q: %v", row, err)
			}
			outcome.rows = append(outcome.rows, parsed)
		}
		if len(outcome.rows) > 0 {
			t.Logf("%s/%s refused: %d row(s): %s", item["version"], item["filename"], outcome.total, out)
			return outcome
		}
	}
	statements, _ := item["statements"].([]string)
	if item["transactional"] == true {
		t.Fatalf("%s is transactional; this harness applies .notx.sql items", item["filename"])
	}
	if out, err := engine.run(t, database, strings.Join(statements, ";\n")+";\n"); err != nil {
		t.Fatalf("apply %s: %v\n%s", item["filename"], err, out)
	}
	return nil
}

func precheckItems(t *testing.T, engine SQLEngine) map[string]map[string]any {
	t.Helper()
	items, err := BuildMigrationItemsForEngine([]SchemaDatabase{{Name: "quartermaster"}}, "expand", "v0.3.0", engine)
	if err != nil {
		t.Fatal(err)
	}
	byFile := map[string]map[string]any{}
	for _, item := range items {
		if item["version"] == "v0.3.0" {
			byFile[item["filename"].(string)] = item
		}
	}
	return byFile
}

func precheckIndexState(t *testing.T, engine precheckEngine, database, index string) string {
	t.Helper()
	out, err := engine.run(t, database, fmt.Sprintf(`SELECT coalesce((SELECT CASE WHEN i.indisvalid THEN 'valid' ELSE 'invalid' END
  FROM pg_index i
  JOIN pg_class c ON c.oid = i.indexrelid
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'quartermaster' AND c.relname = '%s'), 'absent');`, index))
	if err != nil {
		t.Fatalf("inspect index %s: %v", index, err)
	}
	return strings.TrimSpace(out)
}

func requirePrecheckRefusal(t *testing.T, outcome *precheckOutcome, wantTotal int, want map[string]string) {
	t.Helper()
	if outcome == nil {
		t.Fatalf("item applied; want its precheck to refuse it with nodes %v", want)
	}
	if outcome.total != wantTotal || len(outcome.rows) != len(want) {
		t.Fatalf("precheck reported total %d and rows %+v; want %d rows for %v", outcome.total, outcome.rows, wantTotal, want)
	}
	got := map[string]string{}
	for _, row := range outcome.rows {
		cluster := "<none>"
		if row.ClusterID != nil {
			cluster = *row.ClusterID
		}
		if row.LastSeen == nil || *row.LastSeen == "" || row.FingerprintID == "" {
			t.Fatalf("precheck row %+v lacks last seen or fingerprint id", row)
		}
		got[row.NodeID] = cluster
	}
	for node, cluster := range want {
		if got[node] != cluster {
			t.Fatalf("precheck rows %v, want node %s in cluster %s (all: %v)", got, node, cluster, want)
		}
	}
}

// verifyFingerprintPrechecks proves the duplicate-fingerprint prechecks on a quartermaster database that has not yet
// built the v0.3.0 unique fingerprint indexes. Each item is refused, with nothing applied, while rows its index would
// reject remain; it reports every such row and no row the partial index ignores (NULL and blank hashes, and hashes that
// differ only in whitespace); once the duplicates are gone the item applies and its index is valid. 006 is refused
// after 005 applied, so each precheck runs against the state left by the items before it.
func verifyFingerprintPrechecks(t *testing.T, engine precheckEngine, database string) {
	t.Helper()
	if _, err := engine.run(t, database, `
DROP INDEX quartermaster.uq_qm_fingerprints_machine;
DROP INDEX quartermaster.uq_qm_fingerprints_macs;
INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url)
VALUES ('cluster-eu', 'EU', 'edge', 'https://eu.example.test');
INSERT INTO quartermaster.infrastructure_nodes (node_id, cluster_id, node_name, node_type) VALUES
  ('node-a', 'cluster-eu', 'a', 'edge'),
  ('node-b', 'cluster-eu', 'b', 'edge'),
  ('node-c', 'cluster-eu', 'c', 'edge'),
  ('node-d', 'cluster-eu', 'd', 'edge'),
  ('node-e', 'cluster-eu', 'e', 'edge'),
  ('node-f', 'cluster-eu', 'f', 'edge'),
  ('node-g', 'cluster-eu', 'g', 'edge');
INSERT INTO quartermaster.node_fingerprints (node_id, fingerprint_machine_sha256, fingerprint_macs_sha256, last_seen) VALUES
  ('node-a', 'machine-dup', 'macs-a', NOW() - INTERVAL '1 day'),
  ('node-b', 'machine-dup', 'macs-b', NOW()),
  ('node-ghost', 'machine-dup', NULL, NOW() - INTERVAL '30 days'),
  ('node-c', NULL, 'macs-dup', NOW()),
  ('node-d', NULL, 'macs-dup', NOW()),
  ('node-e', '   ', '', NOW()),
  ('node-f', '   ', '', NOW()),
  ('node-g', ' machine-dup', '  ', NOW());
`); err != nil {
		t.Fatalf("seed pre-index fingerprints: %v", err)
	}

	items := precheckItems(t, engine.engine)
	machine := items["005_node_identity_unique_indexes.notx.sql"]
	macs := items["006_node_identity_macs_unique_index.notx.sql"]
	if machine == nil || macs == nil {
		t.Fatalf("v0.3.0 items %v lack the fingerprint index items", items)
	}

	requirePrecheckRefusal(t, applyPrecheckedItem(t, engine, database, machine), 3,
		map[string]string{"node-a": "cluster-eu", "node-b": "cluster-eu", "node-ghost": "<none>"})
	if state := precheckIndexState(t, engine, database, "uq_qm_fingerprints_machine"); state != "absent" {
		t.Fatalf("refused 005 left uq_qm_fingerprints_machine %s", state)
	}

	if _, err := engine.run(t, database, `DELETE FROM quartermaster.node_fingerprints WHERE node_id IN ('node-b', 'node-ghost');`); err != nil {
		t.Fatalf("remove duplicate machine fingerprints: %v", err)
	}
	if outcome := applyPrecheckedItem(t, engine, database, machine); outcome != nil {
		t.Fatalf("005 refused on clean machine hashes: %+v", outcome)
	}
	if state := precheckIndexState(t, engine, database, "uq_qm_fingerprints_machine"); state != "valid" {
		t.Fatalf("applied 005 left uq_qm_fingerprints_machine %s", state)
	}

	requirePrecheckRefusal(t, applyPrecheckedItem(t, engine, database, macs), 2,
		map[string]string{"node-c": "cluster-eu", "node-d": "cluster-eu"})
	if state := precheckIndexState(t, engine, database, "uq_qm_fingerprints_macs"); state != "absent" {
		t.Fatalf("refused 006 left uq_qm_fingerprints_macs %s", state)
	}

	if _, err := engine.run(t, database, `UPDATE quartermaster.node_fingerprints SET fingerprint_macs_sha256 = NULL WHERE node_id = 'node-d';`); err != nil {
		t.Fatalf("unbind duplicate MAC fingerprint: %v", err)
	}
	if outcome := applyPrecheckedItem(t, engine, database, macs); outcome != nil {
		t.Fatalf("006 refused on clean MAC hashes: %+v", outcome)
	}
	if state := precheckIndexState(t, engine, database, "uq_qm_fingerprints_macs"); state != "valid" {
		t.Fatalf("applied 006 left uq_qm_fingerprints_macs %s", state)
	}
}

// TestMigrationPrecheckRefusesDuplicateFingerprints_RealPG runs the fingerprint prechecks on PostgreSQL.
func TestMigrationPrecheckRefusesDuplicateFingerprints_RealPG(t *testing.T) {
	requireDocker(t)
	name := uniqueContainerName("fw-sv-pg-precheck")
	pgStart(t, name)
	database := "qm_precheck"
	pgCreateDB(t, name, database)
	baseline, err := dbsql.Content.ReadFile("schema/quartermaster.sql")
	if err != nil {
		t.Fatal(err)
	}
	pgApply(t, name, database, string(baseline))
	verifyFingerprintPrechecks(t, precheckEngine{
		engine: SQLEnginePostgres,
		run: func(t *testing.T, database, script string) (string, error) {
			return dockerWithTimeout(t, 5*time.Minute, script, "exec", "-i", name, "psql", "-U", "postgres", "-d", database,
				"-v", "ON_ERROR_STOP=1", "-q", "-tA", "-F", "\t")
		},
	}, database)
}

// TestMigrationPrecheckRefusesDuplicateFingerprints_RealYugabyte runs the fingerprint prechecks on YugabyteDB, on a
// database in the declared quartermaster layout receiving the layout-rewritten items the yugabyte role applies.
func TestMigrationPrecheckRefusesDuplicateFingerprints_RealYugabyte(t *testing.T) {
	requireDocker(t)
	name := ybStart(t, fmt.Sprintf("fw-sv-yb-precheck-%d", time.Now().UnixNano()))
	database := fmt.Sprintf("qm_precheck_%d", time.Now().UnixNano())
	layout := ybDatabaseLayout(t, "quartermaster")
	ybCreateDatabase(t, name, database, layout)
	t.Cleanup(func() { ybDropDatabase(t, name, database) })
	baseline, err := dbsql.Content.ReadFile("schema/quartermaster.sql")
	if err != nil {
		t.Fatal(err)
	}
	ybApply(t, name, database, ybLayoutSQL(t, layout, string(baseline)))
	verifyFingerprintPrechecks(t, precheckEngine{
		engine: SQLEngineYugabyte,
		run: func(t *testing.T, database, script string) (string, error) {
			return dockerWithTimeout(t, 10*time.Minute, script, "exec", "-i", name, "ysqlsh", "-h", ybSQLHost(name), "-U", "yugabyte", "-d", database,
				"-v", "ON_ERROR_STOP=1", "-q", "-tA", "-F", "\t")
		},
	}, database)
}
