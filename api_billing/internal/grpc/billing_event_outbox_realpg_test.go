//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
)

func TestBillingEventOutboxLifecycle_RealPG(t *testing.T) {
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	server := &PurserServer{db: db, logger: logging.NewLogger()}
	tenantID := uuid.NewString()

	id, err := server.EnqueueBillingEventTx(ctx, db,
		"payment_succeeded", tenantID, "user-1", "payment", "payment-1",
		&ipcpb.BillingEvent{},
	)
	if err != nil {
		t.Fatalf("enqueue billing event: %v", err)
	}
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("generated outbox id %q is not a UUID: %v", id, err)
	}

	claimed, err := server.claimBillingOutboxBatch(ctx)
	if err != nil {
		t.Fatalf("claim billing event: %v", err)
	}
	if len(claimed) != 1 || claimed[0].id != id || claimed[0].tenantID != tenantID {
		t.Fatalf("claimed rows = %+v", claimed)
	}
	if claimed[0].attempts != 0 || claimed[0].leaseToken == "" {
		t.Fatalf("initial attempts = %d, want 0", claimed[0].attempts)
	}
	if got := claimed[0].billingJSON; len(got) == 0 {
		t.Fatal("typed JSONB payload was empty")
	}

	claimedAgain, err := server.claimBillingOutboxBatch(ctx)
	if err != nil {
		t.Fatalf("claim leased billing event: %v", err)
	}
	if len(claimedAgain) != 0 {
		t.Fatalf("leased row was claimed twice: %+v", claimedAgain)
	}

	if _, err := db.ExecContext(ctx, `UPDATE purser.billing_event_outbox SET claimed_at = NOW() - INTERVAL '2 minutes' WHERE id = $1`, id); err != nil {
		t.Fatalf("expire first lease: %v", err)
	}
	reclaimedByPeer, err := server.claimBillingOutboxBatch(ctx)
	if err != nil || len(reclaimedByPeer) != 1 {
		t.Fatalf("peer reclaim = %+v, %v", reclaimedByPeer, err)
	}
	if reclaimedByPeer[0].leaseToken == claimed[0].leaseToken {
		t.Fatal("peer reclaim reused the stale lease token")
	}
	if err := server.markBillingOutboxCompletedToken(ctx, id, claimed[0].leaseToken); err == nil {
		t.Fatal("stale worker completed a peer-owned row")
	}
	if err := server.recordBillingOutboxFailureToken(ctx, id, claimed[0].leaseToken, claimed[0].attempts, errors.New("stale failure")); err == nil {
		t.Fatal("stale worker failed a peer-owned row")
	}

	if err := server.recordBillingOutboxFailureToken(ctx, id, reclaimedByPeer[0].leaseToken, reclaimedByPeer[0].attempts, errors.New("decklog unavailable")); err != nil {
		t.Fatalf("record current worker failure: %v", err)
	}
	var attempts int
	var lastError string
	var claimedAt sql.NullTime
	if err := db.QueryRowContext(ctx, `
		SELECT attempts, last_error, claimed_at
		FROM purser.billing_event_outbox
		WHERE id = $1
	`, id).Scan(&attempts, &lastError, &claimedAt); err != nil {
		t.Fatalf("read failed row: %v", err)
	}
	if attempts != 1 || lastError != "decklog unavailable" || claimedAt.Valid {
		t.Fatalf("failed row attempts/error/claim = %d/%q/%v", attempts, lastError, claimedAt)
	}

	reclaimed, err := server.claimBillingOutboxBatch(ctx)
	if err != nil {
		t.Fatalf("reclaim failed billing event: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].id != id || reclaimed[0].attempts != 1 {
		t.Fatalf("reclaimed rows = %+v", reclaimed)
	}
	if err := server.markBillingOutboxCompletedToken(ctx, id, reclaimed[0].leaseToken); err != nil {
		t.Fatalf("complete current lease: %v", err)
	}

	var completedAt sql.NullTime
	var persistedError sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT completed_at, last_error
		FROM purser.billing_event_outbox
		WHERE id = $1
	`, id).Scan(&completedAt, &persistedError); err != nil {
		t.Fatalf("read completed row: %v", err)
	}
	if !completedAt.Valid || persistedError.Valid {
		t.Fatalf("completed/error = %v/%v", completedAt, persistedError)
	}

	completedClaim, err := server.claimBillingOutboxBatch(ctx)
	if err != nil {
		t.Fatalf("claim completed billing event: %v", err)
	}
	if len(completedClaim) != 0 {
		t.Fatalf("completed row remained claimable: %+v", completedClaim)
	}
}
