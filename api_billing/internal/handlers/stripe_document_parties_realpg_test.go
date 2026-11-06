//go:build schema_verify

package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

func TestStripeIssuedPartiesRequireAcceptedIdentity_RealPG(t *testing.T) {
	db, service, _ := newStripeReassessmentService(t)
	for _, name := range []string{"accepted_brand_alias", "buyer_country", "buyer_name", "buyer_street", "buyer_vat_missing", "buyer_address_missing", "supplier_footer_missing", "supplier_country"} {
		t.Run(name, func(t *testing.T) {
			tenant := seedPrepaidLedgerTenant(t, db)
			point := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
			raw, profile := historicalStripeReverseCharge(t, db, tenant, "in_party_"+name, point)
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			payload["footer"] = profile.SupplierIdentity + "\n" + profile.Wording
			payload["account_name"] = "FrameWorks Streaming"
			switch name {
			case "buyer_country":
				payload["customer_address"].(map[string]any)["country"] = "NL"
			case "buyer_name":
				payload["customer_name"] = "Different Legal Buyer"
			case "buyer_street":
				payload["customer_address"].(map[string]any)["line1"] = "Changed Street2"
			case "buyer_vat_missing":
				payload["customer_tax_ids"] = []any{}
			case "buyer_address_missing":
				delete(payload, "customer_address")
			case "supplier_footer_missing":
				delete(payload, "footer")
			case "supplier_country":
				payload["account_country"] = "DE"
			}
			hash := sha256.Sum256([]byte(profile.VATNumber))
			if _, err := db.Exec(`INSERT INTO purser.vat_validation_evidence(tenant_id,country_code,vat_number_hash,vat_number_masked,valid,checked_at,request_date,expires_at,raw_response) VALUES($1::uuid,'DE',$2,'DE*****6789',true,$3,$4,$5,'{}')`, tenant, hex.EncodeToString(hash[:]), point.Add(-time.Hour), point.Format(time.DateOnly), point.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if err = service.importStripeTaxDocument(t.Context(), "provider_invoice", raw); err != nil {
				t.Fatal(err)
			}
			var status string
			if err = db.QueryRow(`SELECT tax_status FROM purser.provider_billing_documents WHERE tenant_id=$1::uuid AND provider_document_id=$2`, tenant, "in_party_"+name).Scan(&status); err != nil {
				t.Fatal(err)
			}
			want := "legacy_unknown"
			if name == "accepted_brand_alias" {
				want = "ready"
			}
			if status != want {
				t.Fatalf("issued party binding %s: got %s want %s", name, status, want)
			}
		})
	}
}
