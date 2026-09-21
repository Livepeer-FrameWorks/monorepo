//go:build schema_verify

package provisioner

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	pkgdatabase "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

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
	chHarnessImage := infrastructureHarnessImage(t, "clickhouse")
	cfg, err := filepath.Abs("../../../infrastructure/clickhouse/config.xml")
	if err != nil {
		t.Fatalf("resolve config.xml: %v", err)
	}
	rmContainer(t, name)
	// CLICKHOUSE_SKIP_USER_SETUP=1: without it (and with no CLICKHOUSE_USER/PASSWORD) recent images disable network
	// access for the `default` user, so the harness never answers queries and the gate times out. This is an
	// isolated throwaway container, so skipping user setup is safe.
	if _, err := docker(t, "", "run", "-d", "--name", name,
		"-e", "CLICKHOUSE_SKIP_USER_SETUP=1",
		"-v", cfg+":/etc/clickhouse-server/config.d/zz-keeper.xml:ro",
		chHarnessImage); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	// Register cleanup NOW — before readiness — so a readiness Fatalf below cannot leak the container + its volume
	// (the caller installs its defer only after this returns). A caller that also defers rmContainer double-removes
	// harmlessly.
	t.Cleanup(func() { rmContainer(t, name) })
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
		"SELECT name FROM system.tables WHERE database = 'periscope' AND name NOT LIKE '.inner%' AND name NOT IN ('_migrations', '_schema_baseline') ORDER BY name")
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

func TestClickHouseServiceCapabilitiesExecute(t *testing.T) {
	requireDocker(t)
	name := uniqueContainerName("fw-sv-ch-capabilities")
	chStart(t, name)
	baseline, err := dbsql.Content.ReadFile("clickhouse/periscope.sql")
	if err != nil {
		t.Fatalf("read ClickHouse baseline: %v", err)
	}
	chApply(t, name, string(baseline))

	for _, service := range pkgdatabase.CapabilityServices() {
		for _, capability := range pkgdatabase.CapabilitiesFor(service, pkgdatabase.EngineClickHouse) {
			if output, queryErr := docker(t, "", "exec", name, "clickhouse-client", "--database", "periscope", "-q", capability.Probe); queryErr != nil {
				t.Fatalf("%s capability %q failed: %v\n%s", service, capability.Name, queryErr, output)
			}
		}
	}
	if _, err := docker(t, "", "exec", name, "clickhouse-client", "--database", "periscope", "-q", "SELECT hallucinated_column FROM periscope.api_requests LIMIT 0"); err == nil {
		t.Fatal("a deliberately broken ClickHouse capability probe must fail")
	}
}

func TestClickHouseDeliveryRollupContractSeedsAndRetainsDiscoveryThroughScheduledRefreshes(t *testing.T) {
	requireDocker(t)
	name := uniqueContainerName("fw-sv-ch-rollup-seed")
	chStart(t, name)
	baseline, err := dbsql.Content.ReadFile("clickhouse/periscope.sql")
	if err != nil {
		t.Fatalf("read ClickHouse baseline: %v", err)
	}
	chApply(t, name, string(baseline))
	chApply(t, name, `
		INSERT INTO periscope.delivery_usage_5m
		(window_start, tenant_id, cluster_id, stream_id, node_id, delivery_kind,
		 delivery_id, seconds_observed, down_bytes_observed, source_event_id, projection_version_ms)
		SELECT toStartOfFiveMinute(now()),
		       toUUID('a11ce000-0000-4000-8000-000000000001'), 'cluster-a',
		       toUUID('a11ce000-0000-4000-8000-000000000002'), 'node-a',
		       'restream', 'delivery-a', 300, 1073741824, 'event-a', toUnixTimestamp64Milli(now64(3));
	`)

	contractRefresh, err := dbsql.Content.ReadFile("clickhouse/migrations/periscope/v0.3.0/contract/011_refresh_delivery_rollups.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"clickhouse/migrations/periscope/v0.3.0/contract/008_delivery_dashboard_rollups.sql",
		"clickhouse/migrations/periscope/v0.3.0/contract/009_ledger_tombstone_rollups.sql",
		"clickhouse/migrations/periscope/v0.3.0/contract/010_bound_tenant_usage_daily.sql",
	} {
		migration, readErr := dbsql.Content.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		chApply(t, name, string(migration))
	}
	chApply(t, name, string(contractRefresh))
	// A migration runner retries a file that failed before its ledger write. The
	// contract must therefore be self-contained after its first successful
	// attempt. Discovery rows remain available for an overlapping scheduled
	// refresh and expire through their table TTL instead of a racy truncate.
	chApply(t, name, string(contractRefresh))
	if out, queryErr := docker(t, "", "exec", name, "clickhouse-client", "-q", `
		SELECT concat(
		  toString((SELECT count() FROM periscope.rollup_backfill_markers FINAL)), '/',
		  toString((SELECT count() FROM periscope.rollup_backfill_seed_receipts FINAL)), '/',
		  toString((SELECT seconds_observed FROM periscope.tenant_usage_5m
		            WHERE tenant_id = toUUID('a11ce000-0000-4000-8000-000000000001') LIMIT 1)))
	`); queryErr != nil || strings.TrimSpace(out) != "4/6/300" {
		t.Fatalf("contract result = %q, err=%v, want 4/6/300", strings.TrimSpace(out), queryErr)
	}
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

	aName, bName := uniqueContainerName("fw-sv-ch-a"), uniqueContainerName("fw-sv-ch-b")

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

// TestClickHouseTaggedBaselineUpgradeEqualsCurrent proves in-place upgrade
// convergence once the shipped baseline is already Replicated. Plain-engine
// installations are below the v0.3 source floor and are refused by the ledger certificates.
func TestClickHouseTaggedBaselineUpgradeEqualsCurrent(t *testing.T) {
	requireDocker(t)
	fromTag := schemaVerifyFromTag(t)
	taggedSQL := repositoryFileAtTag(t, fromTag, "pkg/database/sql/clickhouse/periscope.sql")
	if !strings.Contains(taggedSQL, "CREATE DATABASE IF NOT EXISTS periscope ENGINE = Replicated(") {
		t.Skipf("%s uses the unsupported legacy plain ClickHouse source", fromTag)
	}

	currentSQL, err := dbsql.Content.ReadFile("clickhouse/periscope.sql")
	if err != nil {
		t.Fatalf("read current periscope.sql: %v", err)
	}
	migrations, err := discoverMigrationsInFS(dbsql.Content, "clickhouse/migrations", map[string]bool{"periscope": true})
	if err != nil {
		t.Fatalf("discover clickhouse migrations: %v", err)
	}
	migrations = migrationsAfterVersion(migrations, fromTag)

	currentName, upgradedName := uniqueContainerName("fw-sv-ch-current"), uniqueContainerName("fw-sv-ch-tag")
	chStart(t, currentName)
	chApply(t, currentName, string(currentSQL))
	current := chIntrospect(t, currentName)

	chStart(t, upgradedName)
	chApply(t, upgradedName, taggedSQL)
	for _, migration := range migrations {
		if out, applyErr := docker(t, migration.content, "exec", "-i", upgradedName,
			"clickhouse-client", "--database", "periscope", "--multiquery"); applyErr != nil {
			t.Fatalf("apply migration %s/%s/%s to %s: %v\noutput: %s",
				migration.Version, migration.Phase, migration.Filename, upgradedName, applyErr, out)
		}
	}
	upgraded := chIntrospect(t, upgradedName)

	t.Logf("clickhouse periscope: upgraded %s Replicated baseline with %d migration(s) to current", fromTag, len(migrations))
	diffSchemas(t, "clickhouse tagged Replicated baseline upgrade vs current baseline", current, upgraded)
}
