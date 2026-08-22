//go:build schema_verify

package grpc

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/sirupsen/logrus"
)

func TestPullSourceEventRepository_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	server := &CommodoreServer{db: db, logger: logrus.New()}
	ctx := context.Background()
	const (
		tenantID = "10000000-0000-4000-8000-000000000021"
		otherID  = "10000000-0000-4000-8000-000000000022"
		userID   = "20000000-0000-4000-8000-000000000021"
		streamID = "30000000-0000-4000-8000-000000000021"
	)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.streams
		    (id, tenant_id, user_id, stream_key, playback_id, internal_name, title, ingest_mode)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'pull-event-key', 'pull-event-playback', 'pull+event', 'Pull event', 'pull')
	`, streamID, tenantID, userID); err != nil {
		t.Fatalf("seed pull stream: %v", err)
	}

	request := &commodorepb.RecordPullSourceEventRequest{
		TenantId: tenantID, StreamId: streamID, InternalName: "pull+event", EventKind: "resolved", Detail: "media-a",
	}
	if _, err := server.RecordPullSourceEvent(serviceCtx(), request); err != nil {
		t.Fatalf("record first resolution: %v", err)
	}
	var clusterID, claimID string
	if err := db.QueryRowContext(ctx, `
		SELECT active_ingest_cluster_id, active_ingest_claim_id
		FROM commodore.streams WHERE id = $1::uuid AND tenant_id = $2::uuid
	`, streamID, tenantID).Scan(&clusterID, &claimID); err != nil {
		t.Fatal(err)
	}
	if clusterID != "media-a" || claimID != pullClaimToken(streamID) {
		t.Fatalf("resolved placement=(%q,%q), want media-a and pull claim", clusterID, claimID)
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE commodore.streams
		SET active_ingest_cluster_id = 'media-b', active_ingest_claim_id = 'push:fresh', active_ingest_cluster_updated_at = NOW()
		WHERE id = $1::uuid AND tenant_id = $2::uuid
	`, streamID, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.RecordPullSourceEvent(serviceCtx(), request); err != nil {
		t.Fatalf("record contended resolution: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT active_ingest_cluster_id, active_ingest_claim_id
		FROM commodore.streams WHERE id = $1::uuid AND tenant_id = $2::uuid
	`, streamID, tenantID).Scan(&clusterID, &claimID); err != nil {
		t.Fatal(err)
	}
	if clusterID != "media-b" || claimID != "push:fresh" {
		t.Fatalf("stale pull resolution stole fresh placement: (%q,%q)", clusterID, claimID)
	}

	userCtx := context.WithValue(context.Background(), ctxkeys.KeyUserID, userID)
	userCtx = context.WithValue(userCtx, ctxkeys.KeyTenantID, tenantID)
	byStream, err := server.ListPullSourceEvents(userCtx, &commodorepb.ListPullSourceEventsRequest{StreamId: streamID, Limit: 1})
	if err != nil || len(byStream.GetEvents()) != 1 || byStream.GetEvents()[0].GetDetail() != "media-a" {
		t.Fatalf("list by stream=%#v err=%v", byStream, err)
	}
	byName, err := server.ListPullSourceEvents(userCtx, &commodorepb.ListPullSourceEventsRequest{InternalName: "pull+event"})
	if err != nil || len(byName.GetEvents()) != 2 {
		t.Fatalf("list by internal name=%#v err=%v", byName, err)
	}
	for _, event := range byName.GetEvents() {
		if event.GetStreamId() != streamID || event.GetCreatedAt() == nil {
			t.Fatalf("incomplete event projection: %#v", event)
		}
	}

	otherCtx := context.WithValue(context.Background(), ctxkeys.KeyUserID, userID)
	otherCtx = context.WithValue(otherCtx, ctxkeys.KeyTenantID, otherID)
	other, err := server.ListPullSourceEvents(otherCtx, &commodorepb.ListPullSourceEventsRequest{InternalName: "pull+event"})
	if err != nil || len(other.GetEvents()) != 0 {
		t.Fatalf("cross-tenant list=%#v err=%v, want empty", other, err)
	}
}
