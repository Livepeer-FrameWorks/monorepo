//go:build schema_verify

package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestProviderWebhookInboxRepository_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	store := &providerWebhookInboxStore{db: db}
	service := &Service{db: db}

	ok, message, status := service.enqueueProviderWebhook(ctx, "stripe", "evt-contract", map[string]string{
		"Stripe-Signature": "sig", "Authorization": "must-not-persist",
	}, []byte(`{"id":"evt-contract"}`))
	if !ok || message != "" || status != 200 {
		t.Fatalf("enqueue = (%v, %q, %d)", ok, message, status)
	}
	if ok, _, status := service.enqueueProviderWebhook(ctx, "stripe", "evt-contract", nil, []byte(`{}`)); !ok || status != 200 {
		t.Fatalf("duplicate enqueue = (%v, %d)", ok, status)
	}

	claims, err := store.ClaimBatch(ctx, 10, time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claims) != 1 || claims[0].Payload.Provider != "stripe" || claims[0].Payload.Headers["Authorization"] != "" {
		t.Fatalf("claims = %#v", claims)
	}
	if _, err := uuid.Parse(claims[0].ID); err != nil {
		t.Fatalf("claim id is not UUID: %v", err)
	}
	if _, err := uuid.Parse(claims[0].LeaseToken); err != nil {
		t.Fatalf("lease token is not UUID: %v", err)
	}

	if err := store.MarkCompletedToken(ctx, claims[0].ID, uuid.NewString()); err == nil {
		t.Fatal("stale completion lease unexpectedly succeeded")
	}
	if err := store.RecordFailureToken(ctx, claims[0].ID, 0, nil, errors.New("retry me"), 0, claims[0].LeaseToken); err != nil {
		t.Fatalf("record failure: %v", err)
	}

	retry, err := store.ClaimBatch(ctx, 10, 0)
	if err != nil {
		t.Fatalf("retry claim: %v", err)
	}
	if len(retry) != 1 || retry[0].Attempts != 1 {
		t.Fatalf("retry claims = %#v", retry)
	}
	if err := store.MarkCompletedToken(ctx, retry[0].ID, retry[0].LeaseToken); err != nil {
		t.Fatalf("complete: %v", err)
	}

	var statusValue string
	var attempts int
	if err := db.QueryRowContext(ctx, `SELECT status, attempts FROM purser.provider_webhook_inbox WHERE id = $1`, retry[0].ID).Scan(&statusValue, &attempts); err != nil {
		t.Fatal(err)
	}
	if statusValue != "processed" || attempts != 1 {
		t.Fatalf("final row = status %q attempts %d", statusValue, attempts)
	}
}
