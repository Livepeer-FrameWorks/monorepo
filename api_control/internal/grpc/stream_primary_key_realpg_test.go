//go:build schema_verify

package grpc

import (
	"database/sql"
	"testing"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestStreamKeysListCurrentPrimaryAfterRotation_RealPG(t *testing.T) {
	testStreamKeysListCurrentPrimaryAfterRotation(t, startCommodoreRealPG(t))
}

func TestStreamKeysListCurrentPrimaryAfterRotation_RealYugabyte(t *testing.T) {
	testStreamKeysListCurrentPrimaryAfterRotation(t, startPlacementDeliveryYugabyte(t, "stream_primary_key"))
}

func testStreamKeysListCurrentPrimaryAfterRotation(t *testing.T, db *sql.DB) {
	t.Helper()
	tenant, user := uuid.NewString(), uuid.NewString()
	ctx := ctxAs(user, tenant, "owner")
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,is_active) VALUES($1,$2,true)`, user, tenant); err != nil {
		t.Fatal(err)
	}
	var stream, oldKey, playback, internal string
	if err := db.QueryRowContext(ctx, `SELECT * FROM commodore.create_user_stream($1,$2,'Primary key regression')`, tenant, user).Scan(&stream, &oldKey, &playback, &internal); err != nil {
		t.Fatal(err)
	}
	server := &CommodoreServer{db: db, logger: logrus.New(), tokenHasher: testTokenHasher(t)}
	rotated, err := server.RefreshStreamKey(ctx, &commodorepb.RefreshStreamKeyRequest{StreamId: stream})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.GetStreamKey() == oldKey || rotated.GetStreamKey() == "" {
		t.Fatal("rotation did not replace the primary key")
	}
	listed, err := server.ListStreamKeys(ctx, &commodorepb.ListStreamKeysRequest{StreamId: stream})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.GetStreamKeys()) != 1 || listed.GetStreamKeys()[0].GetKeyValue() != rotated.GetStreamKey() || !listed.GetStreamKeys()[0].GetIsActive() || listed.GetPagination().GetTotalCount() != 1 {
		t.Fatal("legacy key listing does not project exactly the current active primary key after rotation")
	}
	first := int32(1)
	for _, page := range []*commonpb.CursorPaginationRequest{
		{First: first, After: listed.GetPagination().EndCursor},
		{Last: first, Before: listed.GetPagination().StartCursor},
	} {
		next, err := server.ListStreamKeys(ctx, &commodorepb.ListStreamKeysRequest{StreamId: stream, Pagination: page})
		if err != nil || len(next.GetStreamKeys()) != 0 || next.GetPagination().GetTotalCount() != 1 {
			t.Fatalf("single key pagination repeated a credential: rows=%d error=%v", len(next.GetStreamKeys()), err)
		}
	}
	for _, foreign := range []struct{ user, tenant string }{{user, uuid.NewString()}, {uuid.NewString(), tenant}} {
		if _, err := server.ListStreamKeys(ctxAs(foreign.user, foreign.tenant, "owner"), &commodorepb.ListStreamKeysRequest{StreamId: stream}); status.Code(err) != codes.NotFound {
			t.Fatalf("key listing escaped its owner/tenant: %v", err)
		}
	}
	if _, err := server.CreateStreamKey(ctx, &commodorepb.CreateStreamKeyRequest{StreamId: stream}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("additional key creation must direct callers to rotation: %v", err)
	}
	if _, err := server.DeactivateStreamKey(ctx, &commodorepb.DeactivateStreamKeyRequest{StreamId: stream, KeyId: listed.GetStreamKeys()[0].GetId()}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("primary key deletion must direct callers to rotation: %v", err)
	}
}
