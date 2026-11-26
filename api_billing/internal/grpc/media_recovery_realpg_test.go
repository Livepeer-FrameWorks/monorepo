//go:build schema_verify

package grpc

import (
	"context"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sync"
	"testing"
)

func TestMediaRecoveryExpiry_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	free, paid, tenant := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,currency)VALUES($1,'free','Free',1,0,'EUR'),($2,'payg','PAYG',0,0,'EUR')`, free, paid)
	exec(`INSERT INTO purser.tier_entitlements(tier_id,key,value)VALUES($1,'storage_limit_gb','10'),($1,'recording_retention_days','30')`, free)
	exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model)VALUES($1,$2,'active','postpaid')`, tenant, free)
	exec(`INSERT INTO purser.prepaid_balances(tenant_id,currency,balance_cents,free_recovery_started_at,free_recovery_media_cutoff_at,free_recovery_until)VALUES($1,'EUR',-100,NOW()-INTERVAL '31 days',NOW()-INTERVAL '31 days',NOW()-INTERVAL '1 day')`, tenant)
	server := &PurserServer{db: db}
	ctx := context.WithValue(t.Context(), ctxkeys.KeyAuthType, "service")
	read := func() *purserpb.MediaRecoveryStatus {
		t.Helper()
		v, err := server.GetMediaRecoveryStatus(ctx, &purserpb.GetMediaRecoveryStatusRequest{TenantId: tenant})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if _, err := server.GetMediaRecoveryStatus(t.Context(), &purserpb.GetMediaRecoveryStatusRequest{TenantId: tenant}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("user credentials could expire media")
	}
	if read().Eligible {
		t.Fatal("unnotified customer lost media")
	}
	exec(`UPDATE purser.prepaid_balances SET recovery_notice_at=NOW()-INTERVAL '9 days',recovery_reminder_at=NOW()-INTERVAL '1 day' WHERE tenant_id=$1`, tenant)
	if read().Eligible {
		t.Fatal("late reminder shortened the recovery window")
	}
	exec(`UPDATE purser.prepaid_balances SET recovery_reminder_at=NOW()-INTERVAL '8 days' WHERE tenant_id=$1`, tenant)
	preview := read()
	if !preview.Eligible || preview.CommittedCutoffAt != nil {
		t.Fatalf("eligible preview changed media: %+v", preview)
	}
	request := &purserpb.CommitMediaRecoveryExpiryRequest{TenantId: tenant, ExpectedCutoffAt: preview.CutoffAt, ExpectedRecoveryUntil: preview.RecoveryUntil}
	exec(`UPDATE purser.prepaid_balances SET balance_cents=100 WHERE tenant_id=$1`, tenant)
	if _, err := server.CommitMediaRecoveryExpiry(ctx, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("funding before expiry lost protection: %v", err)
	}
	exec(`UPDATE purser.prepaid_balances SET balance_cents=-100 WHERE tenant_id=$1`, tenant)
	exec(`INSERT INTO purser.subscription_entitlement_overrides(subscription_id,key,value)SELECT id,'recording_retention_days','0' FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant)
	if read().Eligible {
		t.Fatal("uncapped operator retention was shortened")
	}
	exec(`DELETE FROM purser.subscription_entitlement_overrides WHERE subscription_id IN(SELECT id FROM purser.tenant_subscriptions WHERE tenant_id=$1) AND key='recording_retention_days'`, tenant)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := server.CommitMediaRecoveryExpiry(ctx, request)
			if err != nil || v.GetCommittedCutoffAt() == nil {
				t.Errorf("expiry receipt failed: %+v %v", v, err)
			}
		}()
	}
	wg.Wait()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM purser.media_recovery_expiries WHERE tenant_id=$1`, tenant).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate expiry receipts: %d %v", count, err)
	}
	exec(`UPDATE purser.tenant_subscriptions SET tier_id=$2,billing_model='prepaid' WHERE tenant_id=$1`, tenant, paid)
	balance, err := server.GetPrepaidBalance(t.Context(), &purserpb.GetPrepaidBalanceRequest{TenantId: tenant})
	if err != nil || balance.GetPaymentGrace().GetMediaRecoveryUntil() != nil {
		t.Fatalf("paid service showed a Free recovery warning: %+v %v", balance, err)
	}
	if read().CommittedCutoffAt == nil {
		t.Fatal("new funding revived media already committed as expired")
	}
	if _, err := db.Exec(`UPDATE purser.media_recovery_expiries SET committed_at=NOW() WHERE tenant_id=$1`, tenant); err == nil {
		t.Fatal("expiry receipt was mutable")
	}
}
func TestMediaRecoveryExpiry_RealYugabyte(t *testing.T) { TestMediaRecoveryExpiry_RealPG(t) }
