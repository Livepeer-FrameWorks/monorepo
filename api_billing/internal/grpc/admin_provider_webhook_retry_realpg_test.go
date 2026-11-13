//go:build schema_verify

package grpc

import (
	"context"
	"sync"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestProviderWebhookRetryIsAuditedTenantScopedAndFenced_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	s := &PurserServer{db: db}
	tenant, webhook, actor := uuid.NewString(), uuid.NewString(), uuid.NewString()
	ctx := context.WithValue(t.Context(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, actor)
	ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.provider_webhook_inbox(id,tenant_id,provider,provider_payment_id,event_key,raw_payload,status,last_error_code)
 VALUES($1::uuid,$2::uuid,'stripe','pi_reviewed',$1::uuid::text,'{}','blocked','evidence_conflict')`, webhook, tenant); err != nil {
		t.Fatal(err)
	}
	request := &purserpb.AdminRetryProviderWebhookRequest{TenantId: tenant, WebhookId: webhook, Reason: "provider precision repair deployed"}
	preview, err := s.AdminRetryProviderWebhook(ctx, request)
	if err != nil || !preview.GetEligible() || preview.GetRetryRequested() {
		t.Fatalf("preview changed work or hid blocked state: %v %v", preview, err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.provider_webhook_retry_audit WHERE tenant_id=$1`, tenant).Scan(&count); err != nil || count != 0 {
		t.Fatalf("preview wrote audit: %d %v", count, err)
	}
	wrongTenant := &purserpb.AdminRetryProviderWebhookRequest{TenantId: uuid.NewString(), WebhookId: webhook}
	if _, err := s.AdminRetryProviderWebhook(ctx, wrongTenant); status.Code(err) != codes.NotFound {
		t.Fatalf("cross tenant lookup: %v", err)
	}
	request.Confirmed = true
	request.ExpectedState = "stale"
	if _, err := s.AdminRetryProviderWebhook(ctx, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("stale confirmation accepted: %v", err)
	}
	request.ExpectedState = preview.GetExpectedState()
	results := make(chan *purserpb.AdminRetryProviderWebhookResponse, 2)
	errors := make(chan error, 2)
	start := make(chan struct{})
	var confirmations sync.WaitGroup
	for i := 0; i < 2; i++ {
		confirmations.Add(1)
		go func() {
			defer confirmations.Done()
			<-start
			result, err := s.AdminRetryProviderWebhook(ctx, request)
			results <- result
			errors <- err
		}()
	}
	close(start)
	confirmations.Wait()
	first, second := <-results, <-results
	if err, otherErr := <-errors, <-errors; err != nil || otherErr != nil || !first.GetRetryRequested() || first.GetAuditId() == "" || second.GetAuditId() != first.GetAuditId() {
		t.Fatalf("concurrent confirmations not idempotent: %v %v %v %v", first, second, err, otherErr)
	}
	replay, err := s.AdminRetryProviderWebhook(ctx, request)
	if err != nil || replay.GetAuditId() != first.GetAuditId() || !replay.GetRetryRequested() {
		t.Fatalf("confirmation replay not idempotent: %v %v", replay, err)
	}
	var state, recordedActor, reason string
	if err := db.QueryRowContext(ctx, `SELECT status FROM purser.provider_webhook_inbox WHERE tenant_id=$1 AND id=$2`, tenant, webhook).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("callback not queued: %s %v", state, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT actor_id::text,reason FROM purser.provider_webhook_retry_audit WHERE tenant_id=$1 AND webhook_id=$2`, tenant, webhook).Scan(&recordedActor, &reason); err != nil || recordedActor != actor || reason != request.Reason {
		t.Fatalf("operator evidence lost: %s %s %v", recordedActor, reason, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.provider_webhook_retry_audit WHERE tenant_id=$1 AND webhook_id=$2`, tenant, webhook).Scan(&count); err != nil || count != 1 {
		t.Fatalf("concurrent confirmations wrote duplicate audit: %d %v", count, err)
	}
	for _, mutation := range []string{`UPDATE purser.provider_webhook_retry_audit SET reason='changed' WHERE tenant_id=$1 AND webhook_id=$2`, `DELETE FROM purser.provider_webhook_retry_audit WHERE tenant_id=$1 AND webhook_id=$2`} {
		if _, err := db.ExecContext(ctx, mutation, tenant, webhook); err == nil {
			t.Fatal("retry audit allowed update or delete")
		}
	}
	mollie := uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.provider_webhook_inbox(id,tenant_id,provider,provider_payment_id,event_key,raw_payload,status,last_error_code) VALUES($1::uuid,$2::uuid,'mollie','tr_conflict',$1::uuid::text,'{}','blocked','evidence_conflict')`, mollie, tenant); err != nil {
		t.Fatal(err)
	}
	blocked := &purserpb.AdminRetryProviderWebhookRequest{TenantId: tenant, WebhookId: mollie, Reason: "review conflict"}
	blockedPreview, err := s.AdminRetryProviderWebhook(ctx, blocked)
	if err != nil || blockedPreview.GetEligible() {
		t.Fatalf("Mollie conflict eligible for no-op replay: %v %v", blockedPreview, err)
	}
	blocked.Confirmed, blocked.ExpectedState = true, blockedPreview.GetExpectedState()
	if _, err := s.AdminRetryProviderWebhook(ctx, blocked); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("Mollie conflict incorrectly unblocked: %v", err)
	}
	ordinary := context.WithValue(ctx, ctxkeys.KeyPlatformOperator, false)
	if _, err := s.AdminRetryProviderWebhook(ordinary, request); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("nonoperator retried provider callback: %v", err)
	}
}

func TestProviderWebhookRetryIsAuditedTenantScopedAndFenced_RealYugabyte(t *testing.T) {
	TestProviderWebhookRetryIsAuditedTenantScopedAndFenced_RealPG(t)
}
