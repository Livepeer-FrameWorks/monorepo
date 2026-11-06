//go:build schema_verify

package handlers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/google/uuid"
)

func TestStripeBusinessEvidenceAtTaxPoint_RealPG(t *testing.T) {
	db, service, _ := newStripeReassessmentService(t)
	for _, name := range []string{"valid", "expired", "renewed_before_point", "revoked_before", "revoked_after"} {
		t.Run(name, func(t *testing.T) {
			tenant := seedPrepaidLedgerTenant(t, db)
			point := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
			raw, profile := historicalStripeReverseCharge(t, db, tenant, "in_business_"+name, point)
			acceptedHash := strings.Repeat("a", 64)
			created, expires := point.Add(-3*time.Hour), point.Add(12*time.Hour)
			if name == "expired" || name == "renewed_before_point" {
				expires = point.Add(-time.Hour)
			}
			var snapshot []byte
			if err := db.QueryRow(`INSERT INTO purser.business_establishment_reviews(tenant_id,revision,profile_sha256,country,reference,reviewed_by,created_at,valid_until) VALUES($1::uuid,1,$2,'US','Accepted full legal profile including state',$3::uuid,$4,$5) RETURNING to_jsonb(business_establishment_reviews)`, tenant, acceptedHash, uuid.NewString(), created, expires).Scan(&snapshot); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(name, "revoked_") {
				at := point.Add(-30 * time.Minute)
				if name == "revoked_after" {
					at = point.Add(time.Hour)
				}
				if _, err := db.Exec(`INSERT INTO purser.business_establishment_reviews(tenant_id,revision,is_revocation,reference,reviewed_by,created_at,valid_until) VALUES($1::uuid,2,true,'Business verification withdrawn',$2::uuid,$3,$3)`, tenant, uuid.NewString(), at); err != nil {
					t.Fatal(err)
				}
			}
			if name == "renewed_before_point" {
				if _, err := db.Exec(`INSERT INTO purser.business_establishment_reviews(tenant_id,revision,profile_sha256,country,reference,reviewed_by,created_at,valid_until) VALUES($1::uuid,2,$2,'US','Renewed same full legal profile',$3::uuid,$4,$5)`, tenant, acceptedHash, uuid.NewString(), point.Add(-30*time.Minute), point.Add(12*time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			profile.Country, profile.City, profile.PostalCode, profile.VATNumber, profile.ReverseCharge = "US", "Austin", "78701", "", false
			profile.Wording = "Outside scope of Dutch VAT"
			var proof CryptoTaxAssessment
			if err := json.Unmarshal(profile.Evidence, &proof); err != nil {
				t.Fatal(err)
			}
			from, until := point.Add(-24*time.Hour), point.Add(30*24*time.Hour)
			proof.AssessedAt = point.Add(-2 * time.Hour)
			proof.CustomerType = "business"
			proof.Decision = vatDecision{Country: "US", RateBPS: 0, Source: "Accepted jurisdiction policy", Treatment: "non_eu_b2b", PolicyReference: "policy-valid-at-issue", PolicyValidFrom: &from, PolicyValidUntil: &until, BusinessEstablishment: snapshot}
			proof.VATValidation = nil
			proof.VATValidationMode = ""
			profile.Evidence, _ = json.Marshal(proof)
			profile.ProfileSHA = profile.Fingerprint()
			frozen, err := json.Marshal(profile)
			if err != nil {
				t.Fatal(err)
			}
			if err = purserdb.New(db).StoreProviderTaxProfile(t.Context(), purserdb.StoreProviderTaxProfileParams{TenantID: tenant, ProfileSha256: profile.ProfileSHA, Profile: frozen}); err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err = json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			payload["footer"] = profile.SupplierIdentity + "\n" + profile.Wording
			payload["customer_address"] = map[string]string{"line1": profile.Street, "city": profile.City, "postal_code": profile.PostalCode, "country": profile.Country, "state": "TX"}
			payload["customer_tax_ids"] = []any{}
			payload["metadata"] = map[string]string{"frameworks_tax_profile": profile.ProfileSHA}
			raw, err = json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if err = service.importStripeTaxDocument(t.Context(), "provider_invoice", raw); err != nil {
				t.Fatal(err)
			}
			var status string
			if err = db.QueryRow(`SELECT tax_status FROM purser.provider_billing_documents WHERE tenant_id=$1::uuid AND provider_document_id=$2`, tenant, "in_business_"+name).Scan(&status); err != nil {
				t.Fatal(err)
			}
			want := "ready"
			if name == "expired" || name == "revoked_before" {
				want = "legacy_unknown"
			}
			if status != want {
				t.Fatalf("business proof %s at issue: got %s want %s", name, status, want)
			}
		})
	}
}
