//go:build schema_verify

package grpc

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestOperatorCreditReferenceRejectsChangedMoney_RealPG(t *testing.T) {
	db := startPurserTransitionRealPG(t)
	s := &PurserServer{db: db, logger: logging.NewLogger()}
	tenant, ref, kind := uuid.NewString(), uuid.NewString(), "operator_adjustment"
	req := &purserpb.AdjustBalanceRequest{TenantId: tenant, AmountCents: 7900, Description: "operator credit: incident invoice", ReferenceId: &ref, ReferenceType: &kind}
	first, err := s.AdjustBalance(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := s.AdjustBalance(t.Context(), req)
	if err != nil || repeated.GetId() != first.GetId() {
		t.Fatalf("same credit replay=%v %v", repeated, err)
	}
	req.AmountCents = 5000
	if _, err = s.AdjustBalance(t.Context(), req); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("changed money on same credit reference returned %v; want AlreadyExists", err)
	}
	var balance, count int64
	if err = db.QueryRow(`SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id=$1::uuid AND currency='EUR'`, tenant).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM purser.balance_transactions WHERE tenant_id=$1::uuid`, tenant).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if balance != 7900 || count != 1 {
		t.Fatalf("credit duplicated: balance=%d rows=%d", balance, count)
	}
}
