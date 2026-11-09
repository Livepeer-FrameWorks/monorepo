//go:build schema_verify

package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	mollieapi "github.com/VictorAvelar/mollie-api-go/v4/mollie"
)

func TestMollieDoesNotPromoteEvidenceAfterPayment_RealPG(t *testing.T) {
	db, _, _ := newStripeReassessmentService(t)
	tenant := seedPrepaidLedgerTenant(t, db)
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET billing_name='Buyer',billing_email='buyer@example.test',billing_customer_type='business',tax_id='DE123456789',billing_address='{"street":"Main1","city":"Berlin","postal_code":"10115","country":"DE"}' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1::uuid,'customer')`, tenant); err != nil {
		t.Fatal(err)
	}
	point := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	hash := sha256.Sum256([]byte("DE123456789"))
	// The only live database proof was obtained after the payment. The
	// accepted provider profile also predates that payment with expired proof.
	if _, err := db.Exec(`INSERT INTO purser.vat_validation_evidence(tenant_id,country_code,vat_number_hash,vat_number_masked,valid,checked_at,request_date,expires_at,raw_response) VALUES($1,'DE',$2,'DE*****6789',true,NOW()-INTERVAL '1 minute',CURRENT_DATE,NOW()+INTERVAL '1 day','{}')`, tenant, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
	supplier := ConfiguredDocumentSupplier()
	profile := &billingstripe.TaxProfile{Name: "Buyer", Email: "buyer@example.test", Street: "Main1", City: "Berlin", PostalCode: "10115", Country: "DE", VATNumber: "DE123456789", ReverseCharge: true, Wording: "VAT reverse charged / btw verlegd", SupplierCountry: "NL", SupplierIdentity: billingstripe.SupplierTaxIdentity(supplier.Name, supplier.Address, supplier.VATNumber, supplier.RegistrationNumber)}
	proof := CryptoTaxAssessment{CustomerType: "business", PolicyVersion: frozenCryptoTaxPolicy, SupplierCountry: "NL", Supplier: supplier, Decision: vatDecision{Country: "DE", ReverseCharge: true, VIESValidated: true, Source: "VIES validated cross-border EU business", Treatment: "eu_b2b_reverse_charge", Wording: profile.Wording}}
	var err error
	proof.AssessedAt = point.Add(-48 * time.Hour)
	proof.VATValidation, err = json.Marshal(map[string]any{"tenant_id": tenant, "country_code": "DE", "vat_number_hash": hex.EncodeToString(hash[:]), "valid": true, "checked_at": point.Add(-48 * time.Hour), "expires_at": point.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	profile.Evidence, err = json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err = purserdb.New(db).StoreProviderTaxProfile(t.Context(), purserdb.StoreProviderTaxProfileParams{TenantID: tenant, ProfileSha256: profile.Fingerprint(), Profile: frozen}); err != nil {
		t.Fatal(err)
	}
	payment := &mollieapi.Payment{ID: "paid-before-proof", CustomerID: "customer", SubscriptionID: "subscription", Status: "paid", PaidAt: &point, Amount: &mollieapi.Amount{Currency: "EUR", Value: "100.00"}, Metadata: map[string]any{"tenant_id": tenant, "frameworks_tax_profile": profile.Fingerprint(), "frameworks_net_minor": "10000"}}
	if err = (&Service{db: db, logger: logging.NewLogger()}).issueMollieNativeTaxInvoice(t.Context(), tenant, payment); err != nil {
		t.Fatal(err)
	}
	var reviewed bool
	var gross int64
	if err = db.QueryRow(`SELECT COALESCE((tax_decision->>'review_required')::boolean,false),document_gross_minor FROM purser.billing_invoices WHERE tenant_id=$1::uuid`, tenant).Scan(&reviewed, &gross); err != nil {
		t.Fatal(err)
	}
	if !reviewed || gross != 10000 {
		t.Fatalf("later VIES evidence verified an earlier payment or changed accepted money: review=%v gross=%d", reviewed, gross)
	}
}
