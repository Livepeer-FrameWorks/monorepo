//go:build schema_verify

package grpc

import (
	"context"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestBillingDiagnosisTenantIsolationAndSecretOmission_RealPG(t *testing.T) {
	db := startPurserTransitionRealPG(t)
	tier, tenant, other := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name) VALUES ($1,'diagnostic','Diagnostic')`, tier); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{tenant, other} {
		if _, err := db.Exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,stripe_customer_id) VALUES ($1,$2,'cus_diagnostic')`, id, tier); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(tenant_id,provider,purpose,currency,idempotency_key,provider_request) VALUES ($1,'stripe','tenant_subscription_checkout','EUR',$2,convert_to('secret-provider-request','UTF8'))`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxkeys.KeyAuthType, "jwt"), ctxkeys.KeyUserID, "operator"), ctxkeys.KeyPlatformOperator, true)
	out, err := (&PurserServer{db: db}).DiagnoseBilling(ctx, &purserpb.DiagnoseBillingRequest{TenantId: tenant})
	if err != nil {
		t.Fatal(err)
	}
	if out.GetTenantId() != tenant || out.GetIntentCount() != 1 || len(out.GetIntents()) != 1 || out.GetIntents()[0].GetSubmissionState() != "unknown" {
		t.Fatalf("wrong tenant/state: %v", out)
	}
	body, err := protojson.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "secret-provider-request") || strings.Contains(string(body), other) {
		t.Fatalf("diagnosis leaked another tenant or a provider request: %s", body)
	}
}
