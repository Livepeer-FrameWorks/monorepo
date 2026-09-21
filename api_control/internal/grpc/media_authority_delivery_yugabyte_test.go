//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

func startPlacementDeliveryYugabyte(t *testing.T, prefix string) *sql.DB {
	t.Helper()
	db, ok := dockerpg.OpenSharedYugabyteDatabase(t, prefix)
	if !ok {
		t.Skip("requires make verify-placement-yugabyte-db")
	}
	baseline, err := dbsql.Content.ReadFile("schema/commodore.sql")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, string(baseline)); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMediaAuthorityQueueIndexesUseRangeSharding_RealYugabyte(t *testing.T) {
	db := startPlacementDeliveryYugabyte(t, "authority_queue_indexes")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, name := range []string{
		"idx_media_authority_deliveries_due_v2",
		"idx_media_authority_versions_expiry_v2",
		"idx_media_authority_refresh_inbox_due_v2",
		"idx_media_authority_refresh_inbox_completed_v2",
		"idx_media_authority_refresh_obligations_due",
		"idx_media_authority_refresh_obligations_expiry",
		"idx_media_authority_use_recent",
		"idx_media_authority_deliveries_short_lease_due",
		"idx_media_authority_deliveries_cell_due",
		"idx_media_authority_deliveries_rejected",
		"idx_media_authority_targets_retired",
	} {
		var definition string
		if err := db.QueryRowContext(ctx, `SELECT pg_get_indexdef(to_regclass('commodore.' || $1))`, name).Scan(&definition); err != nil {
			t.Fatalf("read %s definition: %v", name, err)
		}
		if strings.Contains(definition, " HASH") {
			t.Fatalf("%s is hash-sharded instead of range-sharded: %s", name, definition)
		}
	}
}
