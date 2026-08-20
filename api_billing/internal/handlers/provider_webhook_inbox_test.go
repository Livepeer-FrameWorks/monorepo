package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestProviderWebhookInboxClaimUsesTokenFence(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.provider_webhook_inbox[\s\S]+FOR UPDATE SKIP LOCKED`).
		WithArgs(int64(120000), 10).
		WillReturnRows(sqlmock.NewRows([]string{"id", "provider", "headers", "raw_payload", "attempts"}).
			AddRow("inbox-1", "stripe", []byte(`{"Stripe-Signature":"sig"}`), []byte(`{"id":"evt_1"}`), 2))
	mock.ExpectExec(`UPDATE purser\.provider_webhook_inbox[\s\S]+status = 'processing'`).
		WithArgs("inbox-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	claims, err := (&providerWebhookInboxStore{db: db}).ClaimBatch(context.Background(), 10, 2*time.Minute)
	if err != nil {
		t.Fatalf("ClaimBatch: %v", err)
	}
	if len(claims) != 1 || claims[0].ID != "inbox-1" || claims[0].LeaseToken == "" || claims[0].Attempts != 2 {
		t.Fatalf("claims = %+v", claims)
	}
	if claims[0].Payload.Provider != "stripe" || claims[0].Payload.Headers["Stripe-Signature"] != "sig" {
		t.Fatalf("payload = %+v", claims[0].Payload)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestProviderWebhookInboxCompletionRequiresLease(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`UPDATE purser\.provider_webhook_inbox[\s\S]+lease_token::text = \$2`).
		WithArgs("inbox-1", "lease-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	if err := (&providerWebhookInboxStore{db: db}).MarkCompletedToken(context.Background(), "inbox-1", "lease-1"); err == nil {
		t.Fatal("expected stale lease error")
	}
}

func TestProviderWebhookHeadersRetainsOnlyReconciliationHeaders(t *testing.T) {
	got := providerWebhookHeaders(map[string]string{
		"stripe-signature": "sig",
		"Content-Type":     "application/json",
		"Authorization":    "must-not-persist",
		"Cookie":           "must-not-persist",
	})
	if len(got) != 2 || got["Stripe-Signature"] != "sig" || got["Content-Type"] != "application/json" {
		t.Fatalf("providerWebhookHeaders = %#v", got)
	}
}
