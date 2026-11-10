//go:build schema_verify

package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	billingtax "frameworks/api_billing/internal/tax"
	"github.com/google/uuid"
)

func TestLegacyCryptoRepairDoesNotPromoteLaterVIESProof_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	tenant := seedPrepaidLedgerTenant(t, db)
	point := time.Now().UTC().Add(-48 * time.Hour)
	vat := "ATU12345678"
	hash := sha256.Sum256([]byte(vat))
	if _, err := db.Exec(`INSERT INTO purser.vat_validation_evidence(tenant_id,country_code,vat_number_hash,vat_number_masked,valid,request_date,checked_at,expires_at,raw_response) VALUES($1,'AT',$2,'AT*****5678',true,CURRENT_DATE,NOW(),NOW()+INTERVAL '1 day','{}')`, tenant, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
	h := &X402Handler{db: db, supplierCountry: "NL"}
	profile := CryptoBillingProfile{CustomerType: "business", VATNumber: vat, Address: json.RawMessage(`{"country":"AT"}`)}
	d, err := h.legacyCryptoVATDecision(t.Context(), tenant, profile, point.Add(-time.Hour), point)
	var review *billingtax.ReviewError
	if !errors.As(err, &review) || review.Code != "vat_evidence_missing_at_tax_point" || d.ReverseCharge {
		t.Fatalf("later validation promoted a historical payment: decision=%+v err=%v", d, err)
	}
	if _, err := db.Exec(`UPDATE purser.vat_validation_evidence SET checked_at=$2,expires_at=$3 WHERE tenant_id=$1::uuid`, tenant, point.Add(-time.Hour), point.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	d, err = h.legacyCryptoVATDecision(t.Context(), tenant, profile, point.Add(-2*time.Hour), point)
	if err != nil || !d.ReverseCharge {
		t.Fatalf("retained proof valid at actual payment point was lost: decision=%+v err=%v", d, err)
	}
	profile.Name, profile.Email = "Original Buyer", "buyer@example.at"
	profile.Address = json.RawMessage(`{"street":"Main 1","city":"Vienna","postal_code":"1010","country":"AT"}`)
	frozen, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	quote, hashID := uuid.NewString(), "0x"+uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.x402_payment_quotes(id,tenant_id,resource,resource_class,network,asset,pay_to,amount_atomic,credit_amount_cents,requirements_json,status,expires_at,tax_document_kind,tax_profile_snapshot,created_at,original_amount_cents,original_currency,eur_amount_cents,fx_units_per_eur,fx_source,fx_reference_date) VALUES($1,$2,'mcp://historical','mcp','base','asset','payee',1000000,1000,'{}','confirmed',$3,'full',$4,$5,1000,'EUR',1000,1,'identity',$5::timestamptz::date)`, quote, tenant, point.Add(time.Hour), frozen, point.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.x402_nonces(network,payer_address,nonce,tx_hash,tenant_id,amount_cents,status,quote_id,confirmed_at) VALUES('base','payer',$1,$2,$3,1000,'confirmed',$4,$5)`, uuid.NewString(), hashID, tenant, quote, point); err != nil {
		t.Fatal(err)
	}
	h.supplierName, h.supplierAddress, h.supplierVAT, h.supplierRegistration = "DDVTech B.V.", "Leiden NL", "NL853319145B01", "59104694"
	if _, err := h.generateCryptoTopupInvoice(t.Context(), tenant, 1000, "x402_payment", hashID, "", "base"); err != nil {
		t.Fatalf("historical confirmation could not be documented: %v", err)
	}
	var taxPoint, serviceDate time.Time
	var gross, net, tax int64
	if err := db.QueryRow(`SELECT (tax_assessment->>'tax_point')::timestamptz,service_date,gross_amount_cents,net_amount_cents,vat_amount_cents FROM purser.crypto_invoices WHERE tenant_id=$1::uuid AND reference_id=$2`, tenant, hashID).Scan(&taxPoint, &serviceDate, &gross, &net, &tax); err != nil || taxPoint.Sub(point).Abs() > time.Microsecond || serviceDate.Format(time.DateOnly) != point.Format(time.DateOnly) || gross != 1000 || net != 1000 || tax != 0 {
		t.Fatalf("repair changed retained money or payment tax point: taxPoint=%s serviceDate=%s gross/net/VAT=%d/%d/%d err=%v", taxPoint, serviceDate, gross, net, tax, err)
	}
}
