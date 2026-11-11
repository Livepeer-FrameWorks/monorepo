//go:build schema_verify

package periscopequerydb

import (
	"context"
	"testing"
	"time"

	dbqueries "github.com/Livepeer-FrameWorks/monorepo/pkg/database/queries"
)

func TestRetainedViewerLifecycleStaysReservedAfterUpgrade_RealClickHouse(t *testing.T) {
	db := startContractClickHouse(t)
	ctx := t.Context()
	const tenant = "5eed0005-7a11-4e5e-8001-5eed00000001"
	const stream = "5eed0005-7a11-4e5e-8001-5eed00000002"
	const node, cluster = "upgrade-edge", "upgrade-cluster"
	now := time.Now().UTC().Truncate(time.Second)
	first, ended, resumed := now.Add(-40*time.Minute), now.Add(-30*time.Minute), now.Add(-10*time.Minute)
	// Retained raw connects were materialized by the old view, which had no
	// last_connected_at output. Do not let today's view repair the fixture.
	if _, err := db.ExecContext(ctx, "DROP TABLE periscope.viewer_sessions_connect_mv"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		session     string
		last        any
		disconnect  time.Time
		updated     time.Time
		wantOpen    uint8
		wantConnect time.Time
	}{
		{"retained-resumed", nil, ended, resumed, 1, resumed},
		{"retained-closed", nil, now.Add(-5 * time.Minute), now.Add(-5 * time.Minute), 0, first},
		{"current-explicit", resumed, ended, now, 1, resumed},
	}
	for _, tc := range cases {
		for _, at := range []time.Time{first, resumed} {
			if _, err := db.ExecContext(ctx, `INSERT INTO periscope.viewer_connection_events
				(event_id,timestamp,tenant_id,stream_id,internal_name,session_id,node_id,cluster_id,event_type)
				VALUES(generateUUIDv4(),?,?,?,?,?,?,?,'connect')`, at, tenant, stream, "upgrade-stream", tc.session, node, cluster); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO periscope.viewer_sessions_current
			(tenant_id,stream_id,internal_name,session_id,node_id,cluster_id,connected_at,disconnected_at,last_connected_at,last_updated)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, tenant, stream, "upgrade-stream", tc.session, node, cluster, first, tc.disconnect, tc.last, tc.updated); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO periscope.viewer_sessions_final
			(tenant_id,stream_id,node_id,session_id,source_event_id,source_started_at_ms,source_ended_at_ms,projection_version_ms)
			VALUES(?,?,?,?,?,?,?,?)`, tenant, stream, node, tc.session, tc.session+"-old", first.UnixMilli(), ended.UnixMilli(), now.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range cases {
		t.Run(tc.session, func(t *testing.T) {
			var connected time.Time
			var open uint8
			query := "WITH " + dbqueries.ViewerLifecyclesCTE("tenant_id=toUUID(?) AND session_id=?") +
				" SELECT lifecycle_connected_at,lifecycle_open FROM viewer_lifecycles"
			if err := db.QueryRowContext(ctx, query, tenant, tc.session).Scan(&connected, &open); err != nil {
				t.Fatal(err)
			}
			if open != tc.wantOpen || !connected.Equal(tc.wantConnect) {
				t.Fatalf("retained lifecycle open=%d connected=%s, want open=%d connected=%s", open, connected, tc.wantOpen, tc.wantConnect)
			}
		})
	}
	rows, err := ActiveViewerReservations.Query(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	found := false
	for rows.Next() {
		var owner, servingCluster string
		var minutes, egress, unresolvedAge float64
		if err := rows.Scan(&owner, &servingCluster, &minutes, &egress, &unresolvedAge); err != nil {
			t.Fatal(err)
		}
		if owner == tenant {
			found = true
			if servingCluster != cluster || minutes < 20 || minutes > 21 {
				t.Fatalf("retained live holds: cluster=%s minutes=%.2f, want two resumed ten-minute lifecycles", servingCluster, minutes)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("retained resumed viewer lost its active reservation after upgrade")
	}
}

func TestLongLivedViewerRetainsTrustedServingCluster_RealClickHouse(t *testing.T) {
	db := startContractClickHouse(t)
	ctx := t.Context()
	const tenant = "5eed0005-7a11-4e5e-8001-5eed00000011"
	const stream = "5eed0005-7a11-4e5e-8001-5eed00000012"
	connected := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	if _, err := db.ExecContext(ctx, `INSERT INTO periscope.viewer_connection_events
 (event_id,timestamp,tenant_id,stream_id,internal_name,session_id,node_id,cluster_id,event_type)
 VALUES(generateUUIDv4(),?,?,?,?,?,?,?,'connect')`, connected, tenant, stream, "long-live", "long-session", "edge-long", "serving-us"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO periscope.client_qoe_samples
 (timestamp,tenant_id,stream_id,session_id,node_id,connection_time,bytes_downloaded)
 VALUES(now(),?,?,?,?,?,?)`, tenant, stream, "long-session", "edge-long", float64(48*3600), uint64(1024)); err != nil {
		t.Fatal(err)
	}
	assertAttribution := func(want string) {
		t.Helper()
		rows, err := ActiveViewerReservations.Query(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		found := false
		for rows.Next() {
			var owner, cluster string
			var minutes, egress, unresolvedAge float64
			if err := rows.Scan(&owner, &cluster, &minutes, &egress, &unresolvedAge); err != nil {
				t.Fatal(err)
			}
			if owner == tenant {
				found = true
				if want == "" && unresolvedAge < 47*3600 {
					t.Fatalf("oldest unresolved lifecycle age=%v, want at least 47 hours", unresolvedAge)
				}
				if cluster != want {
					t.Fatalf("old active viewer serving cluster=%q want=%q", cluster, want)
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatal("old active viewer disappeared")
		}
	}
	assertAttribution("serving-us")
	// Conflicting trusted evidence for the same lifecycle must remain explicit.
	if _, err := db.ExecContext(ctx, `INSERT INTO periscope.viewer_connection_events
 (event_id,timestamp,tenant_id,stream_id,internal_name,session_id,node_id,cluster_id,event_type)
 VALUES(generateUUIDv4(),?,?,?,?,?,?,?,'connect')`, connected, tenant, stream, "long-live", "long-session", "edge-long", "conflicting-eu"); err != nil {
		t.Fatal(err)
	}
	assertAttribution("")
	// Raw TTL/removal cannot erase previously materialized conflict evidence.
	if _, err := db.ExecContext(ctx, "TRUNCATE TABLE periscope.viewer_connection_events"); err != nil {
		t.Fatal(err)
	}
	assertAttribution("")
}
