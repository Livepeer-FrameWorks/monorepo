//go:build schema_verify

package datamigrations

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbqueries "github.com/Livepeer-FrameWorks/monorepo/pkg/database/queries"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerch"
)

func TestViewerServingAttributionBackfillReplaysAndPreservesConflicts_RealClickHouse(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	db := dockerch.StartCurrent(t, root, "fw-viewer-attribution-backfill").SQL
	ctx := t.Context()
	const tenant = "5eed0005-7a11-4e5e-8001-5eed00000021"
	const other = "5eed0005-7a11-4e5e-8001-5eed00000022"
	const stream = "5eed0005-7a11-4e5e-8001-5eed00000023"
	at := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	if _, err := db.ExecContext(ctx, "DROP TABLE periscope.viewer_lifecycle_serving_attribution_mv"); err != nil {
		t.Fatal(err)
	}
	insert := func(owner, session, cluster string, timestamp time.Time) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO periscope.viewer_connection_events
  (event_id,timestamp,tenant_id,stream_id,internal_name,session_id,node_id,cluster_id,event_type)
  VALUES(generateUUIDv4(),?,?,?,?,?,?,?,'connect')`, timestamp, owner, stream, "retained", session, "edge-backfill", cluster); err != nil {
			t.Fatal(err)
		}
	}
	insert(tenant, "conflicting", "serving-us", at)
	insert(tenant, "conflicting", "serving-eu", at)
	insert(other, "old-other", "serving-other", at)
	migration, err := dbsql.Content.ReadFile("clickhouse/migrations/periscope/v0.3.15/expand/001_viewer_serving_attribution.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range strings.Split(string(migration), ";") {
		if strings.TrimSpace(statement) != "" {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
	}
	// New events arrive through the view while retained data is backfilled.
	insert(other, "live-other", "live-cell", time.Now().UTC())
	if _, err := db.ExecContext(ctx, `INSERT INTO periscope.client_qoe_samples(timestamp,tenant_id,stream_id,session_id,node_id,connection_time)
 VALUES(now(),toUUID(?),toUUID(?),'conflicting','edge-backfill',172800)`, tenant, stream); err != nil {
		t.Fatal(err)
	}
	opts := datamigrate.RunOptions{Scope: datamigrate.ScopeKey{Kind: "tenant", Value: tenant}, BatchSize: 1}
	first, err := backfillViewerServingAttribution(ctx, db, opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.Done || first.Scanned != 1 {
		t.Fatalf("first bounded batch=%+v", first)
	}
	// A crash after the ClickHouse write but before PostgreSQL checkpointing
	// replays the same batch; unique aggregate evidence must remain idempotent.
	if _, err := backfillViewerServingAttribution(ctx, db, opts); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, dbqueries.ActiveViewerReservations)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for rows.Next() {
		var owner, cluster string
		var minutes, egress, unresolvedAge float64
		if err := rows.Scan(&owner, &cluster, &minutes, &egress, &unresolvedAge); err != nil {
			t.Fatal(err)
		}
		if owner == tenant {
			found = true
			if cluster != "" {
				t.Fatalf("partial backfill hid retained conflict: %s", cluster)
			}
		}
	}
	rowErr := rows.Err()
	_ = rows.Close()
	if rowErr != nil {
		t.Fatal(rowErr)
	}
	if !found {
		t.Fatal("active lifecycle disappeared during backfill")
	}
	opts.Checkpoint = first.Checkpoint
	for {
		progress, err := backfillViewerServingAttribution(ctx, db, opts)
		if err != nil {
			t.Fatal(err)
		}
		opts.Checkpoint = progress.Checkpoint
		if progress.Done {
			break
		}
	}
	if err := verifyViewerServingAttribution(ctx, db); err == nil {
		t.Fatal("another tenant's missing history was falsely verified")
	}
	opts = datamigrate.RunOptions{Scope: datamigrate.ScopeKey{Kind: "tenant", Value: other}, BatchSize: 1}
	for {
		progress, err := backfillViewerServingAttribution(ctx, db, opts)
		if err != nil {
			t.Fatal(err)
		}
		opts.Checkpoint = progress.Checkpoint
		if progress.Done {
			break
		}
	}
	if err := verifyViewerServingAttribution(ctx, db); err != nil {
		t.Fatal(err)
	}
	var count uint64
	if err := db.QueryRowContext(ctx, `SELECT length(groupUniqArrayMerge(serving_clusters)) FROM periscope.viewer_lifecycle_serving_attribution WHERE tenant_id=toUUID(?)`, tenant).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("replay lost or invented conflict evidence: %d", count)
	}
	if _, err := db.ExecContext(context.Background(), "TRUNCATE TABLE periscope.viewer_connection_events"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT length(groupUniqArrayMerge(serving_clusters)) FROM periscope.viewer_lifecycle_serving_attribution WHERE tenant_id=toUUID(?)`, tenant).Scan(&count); err != nil || count != 2 {
		t.Fatalf("raw retention erased durable identity: count=%d err=%v", count, err)
	}
}
