//go:build schema_verify

package grpc

import (
	"context"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestArtifactCreationIntentRepository_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx := context.Background()
	server := &CommodoreServer{db: db, logger: logrus.New()}
	const (
		tenantID = "10000000-0000-4000-8000-000000000001"
		userID   = "20000000-0000-4000-8000-000000000001"
		streamID = "30000000-0000-4000-8000-000000000001"
	)

	firstRequest := "40000000-0000-4000-8000-000000000001"
	replayedRequest := "40000000-0000-4000-8000-000000000002"
	persisted, err := upsertCreationIntent(ctx, db, tenantID, creationIntentKindVOD, "vod-intent-1", firstRequest, "media-1", nil)
	if err != nil || persisted != firstRequest {
		t.Fatalf("first upsert = %q, err = %v", persisted, err)
	}
	persisted, err = upsertCreationIntent(ctx, db, tenantID, creationIntentKindVOD, "vod-intent-1", replayedRequest, "media-2", nil)
	if err != nil || persisted != firstRequest {
		t.Fatalf("replayed upsert = %q, err = %v", persisted, err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE commodore.artifact_creation_intents
		SET updated_at = NOW() - INTERVAL '1 hour', created_at = NOW() - INTERVAL '1 hour'
		WHERE tenant_id = $1::uuid AND kind = 'vod' AND artifact_hash = 'vod-intent-1'
	`, tenantID); err != nil {
		t.Fatal(err)
	}
	server.sweepCreationIntentsOnce(ctx)
	var attempts int
	var leaseToken string
	if err := db.QueryRowContext(ctx, `
		SELECT attempts, lease_token::text
		FROM commodore.artifact_creation_intents
		WHERE tenant_id = $1::uuid AND kind = 'vod' AND artifact_hash = 'vod-intent-1'
	`, tenantID).Scan(&attempts, &leaseToken); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || leaseToken == "" {
		t.Fatalf("claimed unresolved intent = attempts %d lease %q", attempts, leaseToken)
	}
	row := creationIntentRow{
		tenantID: tenantID, kind: creationIntentKindVOD, artifactHash: "vod-intent-1",
		requestID: firstRequest, originClusterID: "media-1", leaseToken: leaseToken,
	}
	if err := server.commitCreationIntent(ctx, row, leaseToken, nil); err != nil {
		t.Fatalf("commit claimed intent: %v", err)
	}
	var statusValue string
	var ackPending bool
	if err := db.QueryRowContext(ctx, `
		SELECT status, command_ack_pending
		FROM commodore.artifact_creation_intents
		WHERE tenant_id = $1::uuid AND kind = 'vod' AND artifact_hash = 'vod-intent-1'
	`, tenantID).Scan(&statusValue, &ackPending); err != nil {
		t.Fatal(err)
	}
	if statusValue != "committed" || !ackPending {
		t.Fatalf("committed intent = status %q ack_pending %t", statusValue, ackPending)
	}

	server.drainCreationCommandAcks(ctx)
	var ackAttempts int
	var ackLeaseCleared bool
	if err := db.QueryRowContext(ctx, `
		SELECT command_ack_attempts, command_ack_lease_token IS NULL
		FROM commodore.artifact_creation_intents
		WHERE tenant_id = $1::uuid AND kind = 'vod' AND artifact_hash = 'vod-intent-1'
	`, tenantID).Scan(&ackAttempts, &ackLeaseCleared); err != nil {
		t.Fatal(err)
	}
	if ackAttempts != 1 || !ackLeaseCleared {
		t.Fatalf("backed-off ack = attempts %d lease_cleared %t", ackAttempts, ackLeaseCleared)
	}
	clearToken := "50000000-0000-4000-8000-000000000001"
	if _, err := db.ExecContext(ctx, `
		UPDATE commodore.artifact_creation_intents
		SET command_ack_lease_token = $1::uuid, command_ack_leased_until = NOW() + INTERVAL '1 minute'
		WHERE tenant_id = $2::uuid AND kind = 'vod' AND artifact_hash = 'vod-intent-1'
	`, clearToken, tenantID); err != nil {
		t.Fatal(err)
	}
	server.clearAckObligation(ctx, ackPendingRow{
		tenantID: tenantID, kind: creationIntentKindVOD, artifactHash: "vod-intent-1", leaseToken: clearToken,
	})
	if err := db.QueryRowContext(ctx, `
		SELECT command_ack_pending FROM commodore.artifact_creation_intents
		WHERE tenant_id = $1::uuid AND kind = 'vod' AND artifact_hash = 'vod-intent-1'
	`, tenantID).Scan(&ackPending); err != nil {
		t.Fatal(err)
	}
	if ackPending {
		t.Fatal("terminal ack obligation was not cleared")
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.vod_assets
		    (tenant_id, user_id, vod_hash, internal_name, playback_id, filename)
		VALUES ($1::uuid, $2::uuid, 'vod-abort-1', 'vod-abort-internal', 'vod-abort-playback', 'abort.mp4')
	`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	abortRequest := "40000000-0000-4000-8000-000000000003"
	if _, err := upsertCreationIntent(ctx, db, tenantID, creationIntentKindVOD, "vod-abort-1", abortRequest, "media-1", nil); err != nil {
		t.Fatal(err)
	}
	abortRow := creationIntentRow{tenantID: tenantID, kind: creationIntentKindVOD, artifactHash: "vod-abort-1", requestID: abortRequest}
	if err := server.abortCreationIntent(ctx, abortRow, "", "rejected", true); err != nil {
		t.Fatalf("abort VOD intent: %v", err)
	}
	var vodRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commodore.vod_assets WHERE tenant_id = $1::uuid AND vod_hash = 'vod-abort-1'`, tenantID).Scan(&vodRows); err != nil {
		t.Fatal(err)
	}
	if vodRows != 0 {
		t.Fatalf("aborted VOD business rows = %d", vodRows)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.streams
		    (id, tenant_id, user_id, stream_key, playback_id, internal_name, title)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'clip-parent-key', 'clip-parent-playback', 'clip-parent-internal', 'parent')
	`, streamID, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	clipRequest := "40000000-0000-4000-8000-000000000004"
	if _, err := upsertCreationIntent(ctx, db, tenantID, creationIntentKindClip, "clip-intent-1", clipRequest, "media-1", map[string]string{"kind": "clip"}); err != nil {
		t.Fatal(err)
	}
	clipRow := creationIntentRow{tenantID: tenantID, kind: creationIntentKindClip, artifactHash: "clip-intent-1", requestID: clipRequest, originClusterID: "media-1"}
	clipPayload := clipCreationPayload{
		ClipID: "60000000-0000-4000-8000-000000000001", UserID: userID, StreamID: streamID,
		InternalName: "clip-converged-internal", PlaybackID: "clip-converged-playback",
		Title: "clip", Description: "description", ClipMode: "absolute", RequestedParams: "{}",
	}
	if err := server.commitCreationIntent(ctx, clipRow, "", clipCatalogRowMutator(clipRow, clipPayload, 1000, 2000)); err != nil {
		t.Fatalf("commit converged clip: %v", err)
	}
	var clipRows int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM commodore.clips
		WHERE tenant_id = $1::uuid AND clip_hash = 'clip-intent-1' AND start_time = 1000 AND duration = 2000
	`, tenantID).Scan(&clipRows); err != nil {
		t.Fatal(err)
	}
	if clipRows != 1 {
		t.Fatalf("converged clip rows = %d", clipRows)
	}
}
