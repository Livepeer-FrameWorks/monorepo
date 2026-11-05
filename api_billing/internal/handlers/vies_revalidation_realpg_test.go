//go:build schema_verify

package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"frameworks/api_billing/internal/database/purserdb"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
)

func TestVIESGraceExpiryRevalidationAndPermanentFaultBarrier_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	var mode atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch mode.Load() {
		case 1:
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><soap:Fault><faultcode>soap:Server</faultcode><faultstring>VAT_BLOCKED</faultstring></soap:Fault></soap:Body></soap:Envelope>`))
		case 2:
			_, _ = w.Write([]byte(`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><checkVatResponse xmlns="urn:ec.europa.eu:taxud:vies:services:checkVat:types"><countryCode>DE</countryCode><vatNumber>123456789</vatNumber><requestDate>2026-11-05</requestDate><valid>true</valid><name>Buyer</name></checkVatResponse></soap:Body></soap:Envelope>`))
		default:
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	appconfigtest.Set(t, "VIES_ENDPOINT", server.URL)
	h := &X402Handler{db: db}
	digest := sha256.Sum256([]byte("DE123456789"))
	hash := hex.EncodeToString(digest[:])
	seed := func() string {
		tenant := seedPrepaidLedgerTenant(t, db)
		_, err := db.ExecContext(ctx, `INSERT INTO purser.vat_validation_evidence(tenant_id,country_code,vat_number_hash,vat_number_masked,valid,request_date,checked_at,expires_at,raw_response) VALUES($1,'DE',$2,'DE*****6789',true,CURRENT_DATE-8,NOW()-INTERVAL '170 hours',NOW()-INTERVAL '146 hours','{"original":true}')`, tenant, hash)
		if err != nil {
			t.Fatal(err)
		}
		return tenant
	}
	tenant := seed()
	proof, err := h.validateVIESVATEvidence(ctx, tenant, "DE", "DE123456789")
	if err != nil || !proof.Valid || proof.Mode != "outage_grace" {
		t.Fatalf("proof within 168h after expiry rejected: %+v, %v", proof, err)
	}
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden, NL", "SUPPLIER_VAT_NUMBER": "NL123456789B01", "SUPPLIER_REGISTRATION_NUMBER": "12345678", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	if _, err = db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET billing_customer_type='business',billing_name='Buyer GmbH',billing_email='buyer@example.test',billing_address='{"street":"Main1","city":"Berlin","postal_code":"10115","country":"DE"}',tax_id='DE123456789' WHERE tenant_id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	stripeProfile, err := AssessStripeTaxProfile(ctx, db, tenant, "")
	if err != nil || !stripeProfile.ReverseCharge || !strings.Contains(string(stripeProfile.Evidence), `"vat_validation_mode":"outage_grace"`) {
		t.Fatalf("Stripe caller lost retained outage proof: %+v %v", stripeProfile, err)
	}
	storeRate(t, db, "USD", time.Now().UTC(), "1.25")
	quotedHandler := &X402Handler{db: db, supplierCountry: "NL", supplierName: "DDVTech B.V.", supplierAddress: "Leiden, NL", supplierVAT: "NL123456789B01", supplierRegistration: "12345678", topupUSDCents: 1000}
	quotes, _, err := quotedHandler.CreatePaymentQuotes(ctx, tenant, "mcp://grace-test", "0x0000000000000000000000000000000000000002", []NetworkConfig{Networks["base"]}, "")
	if err != nil || len(quotes) != 1 {
		t.Fatalf("retained grace quote: %d %v", len(quotes), err)
	}
	nonce, hashTx := uuid.NewString(), "0x"+strings.Repeat("e", 64)
	if _, err = db.ExecContext(ctx, `INSERT INTO purser.x402_nonces(id,network,payer_address,nonce,tx_hash,tenant_id,amount_cents,status,quote_id) VALUES($1,'base','0x0000000000000000000000000000000000000003',$2,$3,$4,$5,'pending',$6)`, nonce, uuid.NewString(), hashTx, tenant, quotes[0].CreditAmountCents, quotes[0].ID); err != nil {
		t.Fatal(err)
	}
	if balance, err := quotedHandler.confirmAndCreditSettlement(ctx, tenant, quotes[0].CreditAmountCents, nonce, hashTx, 10, 21000); err != nil || balance != 800 {
		t.Fatalf("grace receipt lost frozen credit/document: %d %v", balance, err)
	}
	auditParams := purserdb.FinancialAuditPageParams{TenantID: tenant, Source: "invoices", FromAt: time.Now().Add(-time.Hour), ThroughAt: time.Now().Add(time.Minute)}
	beforeRows, err := purserdb.New(db).FinancialAuditPage(ctx, auditParams)
	if err != nil || len(beforeRows) != 1 {
		t.Fatalf("grace invoice audit: %+v %v", beforeRows, err)
	}
	var beforeData map[string]json.RawMessage
	if err = json.Unmarshal([]byte(beforeRows[0].Payload), &beforeData); err != nil {
		t.Fatal(err)
	}
	var pendingReview struct {
		ReviewRequired bool `json:"review_required"`
	}
	if err = json.Unmarshal(beforeData["tax_decision"], &pendingReview); err != nil {
		t.Fatal(err)
	}
	if !pendingReview.ReviewRequired {
		t.Fatalf("grace invoice lost review marker: %s", beforeData["tax_decision"])
	}
	var retained json.RawMessage
	if err = db.QueryRowContext(ctx, `SELECT evidence_json->'retained_validation' FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1 AND kind='vies_revalidation'`, tenant).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if string(retained) != string(proof.Snapshot) {
		t.Fatalf("queue did not retain original proof: %s != %s", retained, proof.Snapshot)
	}
	if _, err = db.ExecContext(ctx, `UPDATE purser.crypto_accounting_anomalies SET last_seen_at=NOW()-INTERVAL '6 minutes' WHERE tenant_id=$1 AND kind='vies_revalidation'`, tenant); err != nil {
		t.Fatal(err)
	}
	mode.Store(2)
	if err = h.revalidateTenantVAT(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	var status, outcome string
	var stillRetained json.RawMessage
	if err = db.QueryRowContext(ctx, `SELECT status,evidence_json->>'revalidation_outcome',evidence_json->'retained_validation' FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1 AND kind='vies_revalidation'`, tenant).Scan(&status, &outcome, &stillRetained); err != nil {
		t.Fatal(err)
	}
	if status != "resolved" || outcome != "valid" || string(stillRetained) != string(retained) {
		t.Fatalf("revalidation rewrote proof or failed: %s %s %s", status, outcome, stillRetained)
	}
	afterRows, err := purserdb.New(db).FinancialAuditPage(ctx, auditParams)
	if err != nil || len(afterRows) != 1 {
		t.Fatalf("revalidated invoice audit: %+v %v", afterRows, err)
	}
	var afterData map[string]json.RawMessage
	if err = json.Unmarshal([]byte(afterRows[0].Payload), &afterData); err != nil {
		t.Fatal(err)
	}
	if string(afterData["vies_revalidation"]) != `"valid"` || string(afterData["tax_decision"]) != string(beforeData["tax_decision"]) {
		t.Fatalf("audit lost valid followup or rewrote issued tax facts: %s", afterRows[0].Payload)
	}
	mode.Store(1)
	blocked := seed()
	if proof, err = h.validateVIESVATEvidence(ctx, blocked, "DE", "DE123456789"); err == nil || proof.Valid {
		t.Fatal("permanent fault accepted old valid proof")
	}
	mode.Store(0)
	if proof, err = h.validateVIESVATEvidence(ctx, blocked, "DE", "DE123456789"); err == nil || proof.Valid {
		t.Fatalf("later outage resurrected permanently rejected proof: %+v %v", proof, err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE purser.vat_validation_evidence SET expires_at=NOW()-INTERVAL '169 hours',checked_at=NOW()-INTERVAL '193 hours' WHERE tenant_id=$1`, blocked); err != nil {
		t.Fatal(err)
	}
	if proof, err = h.validateVIESVATEvidence(ctx, blocked, "DE", "DE123456789"); err == nil || proof.Valid {
		t.Fatalf("old proof used outside grace: %+v", proof)
	}
}
