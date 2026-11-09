//go:build schema_verify

package grpc

import (
	"testing"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
)

func TestBillingDiagnosisRetainsOrphanRecovery_RealPG(t *testing.T) {
	db := startPurserTransitionRealPG(t)
	tenant := uuid.NewString()
	var inbox string
	if err := db.QueryRow(`INSERT INTO purser.provider_webhook_inbox(provider,event_key,headers,raw_payload) VALUES('stripe',$1,jsonb_build_object('tenant_id',$2::text),$3) RETURNING id::text`, uuid.NewString(), tenant, []byte(`{}`)).Scan(&inbox); err != nil {
		t.Fatal(err)
	}
	out, err := (&PurserServer{db: db}).DiagnoseBilling(grantNoticeOperatorCtx(), &purserpb.DiagnoseBillingRequest{TenantId: tenant})
	if err != nil {
		t.Fatalf("missing subscription must retain the independent recovery evidence: %v", err)
	}
	if out.GetTenantId() != tenant || out.GetStatus() != "missing" || len(out.GetPendingWebhooks()) != 1 || out.GetPendingWebhooks()[0].GetId() != inbox {
		t.Fatalf("partial billing diagnosis lost tenant/subscription/recovery state: %+v", out)
	}
}
