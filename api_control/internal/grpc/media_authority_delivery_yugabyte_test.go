//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
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
