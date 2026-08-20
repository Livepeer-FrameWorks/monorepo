//go:build schema_verify

package provisioner

import (
	"fmt"
	"strings"
	"testing"

	dbqueries "github.com/Livepeer-FrameWorks/monorepo/pkg/database/queries"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// TestClickHouseDemoSeedAndMeteringQueries applies the complete demo fixture to
// the current schema, checks its session/cluster invariants, and executes the
// exact active-reservation statement used by periscope-metering.
func TestClickHouseDemoSeedAndMeteringQueries(t *testing.T) {
	requireDocker(t)

	schemaSQL, err := dbsql.Content.ReadFile("clickhouse/periscope.sql")
	if err != nil {
		t.Fatalf("read ClickHouse baseline: %v", err)
	}
	seedSQL, err := dbsql.Content.ReadFile("seeds/demo/clickhouse_demo_data.sql")
	if err != nil {
		t.Fatalf("read ClickHouse demo seed: %v", err)
	}

	const name = "fw-demo-seed-ch"
	chStart(t, name)
	chApply(t, name, string(schemaSQL))
	chApply(t, name, string(seedSQL))

	assertCHScalar(t, name, `
		SELECT count()
		FROM periscope.viewer_connection_events
		WHERE cluster_id = ''
	`, "0", "viewer connection events without serving-cluster attribution")
	assertCHScalar(t, name, `
		SELECT count()
		FROM periscope.viewer_sessions_current FINAL
		WHERE connected_at IS NOT NULL
		  AND (disconnected_at IS NULL OR disconnected_at = toDateTime(0))
	`, "0", "unpaired demo viewer sessions")

	const tenantID = "11111111-1111-4111-8111-111111111111"
	const streamID = "22222222-2222-4222-8222-222222222222"
	chApply(t, name, fmt.Sprintf(`
		INSERT INTO periscope.viewer_connection_events (
			event_id, timestamp, tenant_id, stream_id, internal_name, session_id,
			connection_addr, connector, node_id, cluster_id, origin_cluster_id,
			country_code, city, latitude, longitude, event_type,
			session_duration, bytes_transferred
		) VALUES (
			generateUUIDv4(), now(), '%s', '%s', 'contract-stream', 'contract-session',
			'127.0.0.1', 'HLS', 'edge-contract', 'serving-contract', 'origin-contract',
			'US', 'Test', 0, 0, 'connect', 0, 0
		)
	`, tenantID, streamID))

	out, err := docker(t, "", "exec", name, "clickhouse-client", "-q", dbqueries.ActiveViewerReservations)
	if err != nil {
		t.Fatalf("execute active-viewer reservation query: %v\noutput: %s", err, out)
	}
	fields := strings.Split(strings.TrimSpace(out), "\t")
	if len(fields) != 4 || fields[0] != tenantID || fields[1] != "serving-contract" {
		t.Fatalf("reservation row = %q, want tenant %s served by serving-contract", out, tenantID)
	}
}

func assertCHScalar(t *testing.T, name, query, want, description string) {
	t.Helper()
	out, err := docker(t, "", "exec", name, "clickhouse-client", "-q", query)
	if err != nil {
		t.Fatalf("query %s: %v\noutput: %s", description, err, out)
	}
	if got := strings.TrimSpace(out); got != want {
		t.Fatalf("%s = %s, want %s", description, got, want)
	}
}
