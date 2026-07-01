//go:build schema_verify

package provisioner

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// Pinned to match docker-compose.yml and the production release manifest.
const chHarnessImage = "clickhouse/clickhouse-server:26.3.10.62"

var (
	// de-Replicate the engine name: ReplicatedXMergeTree -> XMergeTree
	// (ReplicatedMergeTree -> MergeTree). This is the one deliberate tolerance:
	// the HA baseline is Replicated*, replayed historical contract migrations
	// recreate the same tables as plain — folding them is the whole point.
	reDeReplicate = regexp.MustCompile(`\bReplicated([A-Za-z]*)MergeTree\b`)
	// Strip the two leading string-literal engine args (zk path + replica name)
	// that the server injects for a Replicated table but that a plain table lacks.
	// Fires only when an engine call starts with two quoted strings — i.e. a
	// previously-Replicated engine; plain engines (version col, tuple) are untouched.
	reStripPathReplica  = regexp.MustCompile(`MergeTree\('[^']*',\s*'[^']*'\s*,?\s*`)
	reEmptyEngineParens = regexp.MustCompile(`MergeTree\(\s*\)`)
	reUUIDClause        = regexp.MustCompile(`UUID '[^']*'`)
)

// normalizeCHCreate canonicalizes a SHOW CREATE statement so the Replicated
// baseline and a plain-engine replay compare equal MODULO the engine divergence,
// while preserving everything semantic (ORDER BY / PARTITION BY / TTL / SETTINGS /
// version columns / TABLE-vs-VIEW kind).
func normalizeCHCreate(ddl string) string {
	ddl = reUUIDClause.ReplaceAllString(ddl, "")
	ddl = reDeReplicate.ReplaceAllString(ddl, "${1}MergeTree")
	// After de-Replicating, drop the leading path+replica literals, then any empty
	// parens left behind (de-Replicated ReplicatedMergeTree('p','r') -> MergeTree()).
	ddl = reStripPathReplica.ReplaceAllString(ddl, "MergeTree(")
	ddl = reEmptyEngineParens.ReplaceAllString(ddl, "MergeTree")
	return collapseWS(ddl)
}

// chStart launches a throwaway ClickHouse container with the dev keeper-enabled
// config (so Replicated* engines resolve) and waits until it answers queries.
func chStart(t *testing.T, name string) {
	t.Helper()
	cfg, err := filepath.Abs("../../../infrastructure/clickhouse/config.xml")
	if err != nil {
		t.Fatalf("resolve config.xml: %v", err)
	}
	rmContainer(t, name)
	if _, err := docker(t, "", "run", "-d", "--name", name,
		"-v", cfg+":/etc/clickhouse-server/config.d/zz-keeper.xml:ro",
		chHarnessImage); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		if out, err := docker(t, "", "exec", name, "clickhouse-client", "-q", "SELECT 1"); err == nil && strings.TrimSpace(out) == "1" {
			return
		}
		if time.Now().After(deadline) {
			logs, _ := docker(t, "", "logs", "--tail", "40", name)
			t.Fatalf("%s did not become ready in time:\n%s", name, logs)
		}
		time.Sleep(time.Second)
	}
}

// chApply runs a multi-statement SQL blob against the container.
func chApply(t *testing.T, name, sql string) {
	t.Helper()
	if out, err := docker(t, sql, "exec", "-i", name, "clickhouse-client", "--multiquery"); err != nil {
		t.Fatalf("apply SQL to %s: %v\noutput: %s", name, err, out)
	}
}

// chIntrospect returns name -> normalized SHOW CREATE for every periscope object.
func chIntrospect(t *testing.T, name string) map[string]string {
	t.Helper()
	out, err := docker(t, "", "exec", name, "clickhouse-client", "-q",
		"SELECT name FROM system.tables WHERE database = 'periscope' AND name NOT LIKE '.inner%' ORDER BY name")
	if err != nil {
		t.Fatalf("list periscope tables in %s: %v", name, err)
	}
	schema := map[string]string{}
	for _, tbl := range strings.Split(strings.TrimSpace(out), "\n") {
		tbl = strings.TrimSpace(tbl)
		if tbl == "" {
			continue
		}
		ddl, err := docker(t, "", "exec", name, "clickhouse-client", "-q",
			fmt.Sprintf("SHOW CREATE TABLE periscope.`%s`", tbl))
		if err != nil {
			t.Fatalf("SHOW CREATE periscope.%s in %s: %v", tbl, name, err)
		}
		schema[tbl] = normalizeCHCreate(ddl)
	}
	if len(schema) == 0 {
		t.Fatalf("%s: no periscope objects found (apply failed silently?)", name)
	}
	return schema
}

// TestClickHouseBaselineEqualsReplay proves periscope.sql (baseline) is logically
// equal to periscope.sql + every periscope migration replayed on top — modulo the
// Replicated-vs-plain engine divergence that the squash exists to resolve.
func TestClickHouseBaselineEqualsReplay(t *testing.T) {
	requireDocker(t)

	baselineSQL, err := dbsql.Content.ReadFile("clickhouse/periscope.sql")
	if err != nil {
		t.Fatalf("read baseline periscope.sql: %v", err)
	}
	migs, err := discoverMigrationsInFS(dbsql.Content, "clickhouse/migrations", map[string]bool{"periscope": true})
	if err != nil {
		t.Fatalf("discover clickhouse migrations: %v", err)
	}

	const aName, bName = "fw-sv-ch-a", "fw-sv-ch-b"

	// A: baseline only.
	chStart(t, aName)
	defer rmContainer(t, aName)
	chApply(t, aName, string(baselineSQL))
	baseline := chIntrospect(t, aName)

	// B: baseline, then every POST-FLOOR migration in (version, phase, sequence)
	// order. Pre-floor migrations are folded into the baseline and NOT replayed: they
	// are deltas against an OLDER baseline shape (e.g. a table the v0.2.82 contract
	// later swapped to a VIEW), so replaying them on the CURRENT baseline is not a
	// clean operation. This mirrors what a fresh `init` does: baseline, then only
	// post-floor expand/postdeploy/contract.
	chStart(t, bName)
	defer rmContainer(t, bName)
	chApply(t, bName, string(baselineSQL))
	replayedCount := 0
	for _, m := range migs {
		if belowBaselineFloor(m) {
			continue
		}
		replayedCount++
		if out, err := docker(t, m.content, "exec", "-i", bName,
			"clickhouse-client", "--database", "periscope", "--multiquery"); err != nil {
			t.Fatalf("apply migration %s/%s/%s to %s: %v\noutput: %s",
				m.Version, m.Phase, m.Filename, bName, err, out)
		}
	}
	replayed := chIntrospect(t, bName)

	t.Logf("clickhouse periscope: %d baseline objects, %d replayed, %d/%d migrations post-floor (floor=%s)",
		len(baseline), len(replayed), replayedCount, len(migs), schemaMigrationBaselineFloor)
	diffSchemas(t, "clickhouse periscope", baseline, replayed)
}
