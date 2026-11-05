//go:build schema_verify

package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"github.com/google/uuid"
)

func TestVATQuoteRetainsTheDecisionEvidence_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	tenant, tier := uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers(id,tier_name,display_name) VALUES($1,$2,'VAT proof test')`, tier, "vat-"+tier); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions(id,tenant_id,tier_id,billing_customer_type,billing_name,billing_email,billing_address,tax_id) VALUES($1,$2,$3,'business','German Buyer','buyer@example.test','{"street":"Main 1","city":"Berlin","postal_code":"10115","country":"DE"}','DE123456789')`, uuid.NewString(), tenant, tier); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("DE123456789"))
	for _, proof := range []struct{ date, checked, expires, marker string }{
		{"CURRENT_DATE-1", "NOW()-INTERVAL '10 minutes'", "NOW()+INTERVAL '2 days'", "older"},
		{"CURRENT_DATE", "NOW()-INTERVAL '1 minute'", "NOW()+INTERVAL '1 day'", "latest"},
	} {
		query := `INSERT INTO purser.vat_validation_evidence(tenant_id,country_code,vat_number_hash,vat_number_masked,valid,request_date,checked_at,expires_at,raw_response) VALUES($1,'DE',$2,'DE*****6789',true,` + proof.date + `,` + proof.checked + `,` + proof.expires + `,$3::jsonb)`
		raw, _ := json.Marshal(map[string]string{"marker": proof.marker})
		if _, err := db.ExecContext(ctx, query, tenant, hex.EncodeToString(digest[:]), raw); err != nil {
			t.Fatal(err)
		}
	}
	appconfigtest.Set(t, "VIES_ENDPOINT", "http://127.0.0.1:1")
	requirement, err := (&X402Handler{db: db, supplierCountry: "NL"}).GetCryptoDocumentRequirement(ctx, tenant, 500, "")
	if err != nil {
		t.Fatal(err)
	}
	if requirement.Profile.TaxAssessment == nil || !strings.Contains(string(requirement.Profile.TaxAssessment.VATValidation), `"latest"`) {
		t.Fatalf("quote retained evidence other than the latest validation used for its decision: %+v", requirement.Profile.TaxAssessment)
	}
}
