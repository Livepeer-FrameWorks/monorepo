//go:build schema_verify

package grpc

import (
	"context"
	"errors"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/sirupsen/logrus"
)

func TestDurableOutboxRepositories_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx := context.Background()
	server := &CommodoreServer{db: db, logger: logrus.New()}
	tenantID := "10000000-0000-4000-8000-000000000001"
	userID := "20000000-0000-4000-8000-000000000002"
	resourceID := "30000000-0000-4000-8000-000000000003"

	serviceID, err := server.EnqueueServiceEventTx(ctx, db, &ipcpb.ServiceEvent{
		EventType: "stream_created", TenantId: tenantID, UserId: userID,
		ResourceType: "stream", ResourceId: resourceID,
	})
	if err != nil || serviceID == "" {
		t.Fatalf("enqueue service event id = %q, err = %v", serviceID, err)
	}
	serviceBatch, err := server.claimCommodoreServiceOutboxBatch(ctx)
	if err != nil || len(serviceBatch) != 1 || serviceBatch[0].id != serviceID || serviceBatch[0].attempts != 0 {
		t.Fatalf("first service-event claim = %#v, err = %v", serviceBatch, err)
	}
	serviceBatch, err = server.claimCommodoreServiceOutboxBatch(ctx)
	if err != nil || len(serviceBatch) != 0 {
		t.Fatalf("leased service-event re-claim = %#v, err = %v", serviceBatch, err)
	}
	server.recordCommodoreServiceOutboxFailure(ctx, serviceID, 1, errors.New("decklog unavailable"))
	serviceBatch, err = server.claimCommodoreServiceOutboxBatch(ctx)
	if err != nil || len(serviceBatch) != 1 || serviceBatch[0].attempts != 1 {
		t.Fatalf("released service-event claim = %#v, err = %v", serviceBatch, err)
	}
	server.markCommodoreServiceOutboxCompleted(ctx, serviceID)
	serviceBatch, err = server.claimCommodoreServiceOutboxBatch(ctx)
	if err != nil || len(serviceBatch) != 0 {
		t.Fatalf("completed service-event claim = %#v, err = %v", serviceBatch, err)
	}
	var serviceAttempts int
	var serviceCompleted bool
	if err := db.QueryRowContext(ctx, `
		SELECT attempts, completed_at IS NOT NULL
		FROM commodore.service_event_outbox WHERE id = $1::uuid
	`, serviceID).Scan(&serviceAttempts, &serviceCompleted); err != nil {
		t.Fatal(err)
	}
	if serviceAttempts != 1 || !serviceCompleted {
		t.Fatalf("service-event terminal state = attempts %d completed %t", serviceAttempts, serviceCompleted)
	}

	invalidationID, err := server.enqueueInvalidationOutbox(ctx, db, tenantID, "policy_change", []string{"stream-b", "stream-a"})
	if err != nil || invalidationID == "" {
		t.Fatalf("enqueue invalidation id = %q, err = %v", invalidationID, err)
	}
	invalidationBatch, err := server.claimInvalidationOutboxBatch(ctx)
	if err != nil || len(invalidationBatch) != 1 || invalidationBatch[0].id != invalidationID || invalidationBatch[0].tenantID != tenantID {
		t.Fatalf("first invalidation claim = %#v, err = %v", invalidationBatch, err)
	}
	if len(invalidationBatch[0].internalNames) != 2 || invalidationBatch[0].internalNames[0] != "stream-b" {
		t.Fatalf("invalidation names = %#v", invalidationBatch[0].internalNames)
	}
	invalidationBatch, err = server.claimInvalidationOutboxBatch(ctx)
	if err != nil || len(invalidationBatch) != 0 {
		t.Fatalf("leased invalidation re-claim = %#v, err = %v", invalidationBatch, err)
	}
	server.recordInvalidationOutboxFailure(ctx, invalidationID, 0, []string{"edge-a"}, errors.New("edge unavailable"))
	var attempts int
	var pending, failedClustersPersisted bool
	if err := db.QueryRowContext(ctx, `
		SELECT attempts, status = 'pending', last_failed_clusters = '["edge-a"]'::jsonb
		FROM commodore.playback_policy_invalidation_outbox WHERE id = $1::uuid
	`, invalidationID).Scan(&attempts, &pending, &failedClustersPersisted); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || !pending || !failedClustersPersisted {
		t.Fatalf("failed invalidation state = attempts %d pending %t clusters %t", attempts, pending, failedClustersPersisted)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE commodore.playback_policy_invalidation_outbox
		SET next_attempt_at = NOW() - INTERVAL '1 second'
		WHERE id = $1::uuid
	`, invalidationID); err != nil {
		t.Fatal(err)
	}
	invalidationBatch, err = server.claimInvalidationOutboxBatch(ctx)
	if err != nil || len(invalidationBatch) != 1 || invalidationBatch[0].attempts != 1 {
		t.Fatalf("retry invalidation claim = %#v, err = %v", invalidationBatch, err)
	}
	server.markInvalidationOutboxCompleted(ctx, invalidationID)
	invalidationBatch, err = server.claimInvalidationOutboxBatch(ctx)
	if err != nil || len(invalidationBatch) != 0 {
		t.Fatalf("completed invalidation claim = %#v, err = %v", invalidationBatch, err)
	}
}
