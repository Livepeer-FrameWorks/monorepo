//go:build schema_verify

package grpc

import (
	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrepaidPaymentGrace_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	tier, tenant := uuid.NewString(), uuid.NewString()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,currency,is_default_prepaid)VALUES($1,'payg','PAYG',0,0,'EUR',true)`, tier)
	exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model)VALUES($1,$2,'active','prepaid')`, tenant, tier)
	exec(`INSERT INTO purser.prepaid_balances(tenant_id,currency,balance_cents)VALUES($1,'EUR',100)`, tenant)
	// Compile the cold fixture's distributed catalog plan before exercising the unchanged admission budget.
	if _, err := purserdb.New(db).GetTenantAdmissionStatus(t.Context(), purserdb.GetTenantAdmissionStatusParams{TenantID: tenant, Currency: "EUR"}); err != nil {
		t.Fatal(err)
	}
	server := &PurserServer{db: db, logger: logging.NewLogger()}
	read := func() *purserpb.GetTenantAdmissionStatusResponse {
		t.Helper()
		v, err := server.GetTenantAdmissionStatus(t.Context(), &purserpb.GetTenantAdmissionStatusRequest{TenantId: tenant})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	exec(`UPDATE purser.prepaid_balances SET balance_cents=0 WHERE tenant_id=$1`, tenant)
	if v := read(); !v.IsBalanceNegative || v.GetPaymentGrace().GetActive() {
		t.Fatalf("never-funded wallet borrowed grace: %+v", v)
	}
	exec(`INSERT INTO purser.balance_transactions(tenant_id,amount_cents,balance_after_cents,transaction_type,description)VALUES($1,912,912,'topup','settled payment')`, tenant)
	exec(`UPDATE purser.prepaid_balances SET balance_cents=912 WHERE tenant_id=$1`, tenant)
	exec(`UPDATE purser.prepaid_balances SET balance_cents=-1 WHERE tenant_id=$1`, tenant)
	q := purserdb.New(db)
	notices, err := q.ListPrepaidPaymentNotices(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var notice *purserdb.ListPrepaidPaymentNoticesRow
	for i := range notices {
		if notices[i].TenantID == tenant {
			notice = &notices[i]
		}
	}
	if notice == nil || notice.Kind != "grace" {
		t.Fatalf("grace notification was not durable: %+v", notices)
	}
	var claims atomic.Int64
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := q.ClaimPrepaidPaymentNotice(t.Context(), purserdb.ClaimPrepaidPaymentNoticeParams{TenantID: tenant, Kind: notice.Kind, GraceEndsAt: notice.GraceEndsAt, FreeRecoveryUntil: notice.FreeRecoveryUntil})
			if err != nil {
				t.Error(err)
			}
			claims.Add(n)
		}()
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("two workers could send the same notice: claims=%d", claims.Load())
	}
	if _, err := q.FinishPrepaidPaymentNotice(t.Context(), purserdb.FinishPrepaidPaymentNoticeParams{TenantID: tenant, Kind: notice.Kind, GraceEndsAt: notice.GraceEndsAt, FreeRecoveryUntil: notice.FreeRecoveryUntil, Sent: true}); err != nil {
		t.Fatal(err)
	}
	first := read()
	if first.IsBalanceNegative || !first.GetPaymentGrace().GetActive() || first.GetPaymentGrace().GetRemainingCreditCents() != 911 || first.GetValidUntil() == nil {
		t.Fatalf("funded wallet lost grace: %+v", first)
	}
	if first.ValidUntil.AsTime().After(time.Now().Add(61 * time.Second)) {
		t.Fatal("signed grace could survive disconnection beyond its short renewal window")
	}
	if until := first.PaymentGrace.EndsAt.AsTime(); until.Before(time.Now().Add(71*time.Hour)) || until.After(time.Now().Add(73*time.Hour)) {
		t.Fatalf("wrong persisted deadline: %s", until)
	}
	exec(`UPDATE purser.prepaid_balances SET balance_cents=1 WHERE tenant_id=$1`, tenant)
	exec(`UPDATE purser.prepaid_balances SET balance_cents=-1 WHERE tenant_id=$1`, tenant)
	if v := read(); !v.GetPaymentGrace().GetEndsAt().AsTime().Equal(first.PaymentGrace.EndsAt.AsTime()) {
		t.Fatal("token funding renewed grace")
	}
	exec(`UPDATE purser.prepaid_balances SET balance_cents=-912 WHERE tenant_id=$1`, tenant)
	if v := read(); !v.IsBalanceNegative || v.GetPaymentGrace().GetActive() {
		t.Fatalf("grace spending cap failed: %+v", v)
	}
	exec(`UPDATE purser.prepaid_balances SET balance_cents=-1,grace_ends_at=NOW()-INTERVAL '1 second',grace_started_at=NOW()-INTERVAL '73 hours' WHERE tenant_id=$1`, tenant)
	if v := read(); !v.IsBalanceNegative {
		t.Fatal("expired grace still admitted")
	}
	exec(`UPDATE purser.prepaid_balances SET balance_cents=100 WHERE tenant_id=$1`, tenant)
	exec(`UPDATE purser.prepaid_balances SET grace_recovered_at=NOW()-INTERVAL '31 days' WHERE tenant_id=$1`, tenant)
	// The recovery timestamp represents continuously funded time, not a new deposit.
	exec(`UPDATE purser.prepaid_balances SET balance_cents=0 WHERE tenant_id=$1`, tenant)
	if v := read(); v.IsBalanceNegative || !v.GetPaymentGrace().GetActive() || !v.PaymentGrace.EndsAt.AsTime().After(first.PaymentGrace.EndsAt.AsTime()) {
		t.Fatalf("sustained recovery failed to rearm: %+v", v)
	}
}

func TestPrepaidPaymentGrace_RealYugabyte(t *testing.T) { TestPrepaidPaymentGrace_RealPG(t) }
