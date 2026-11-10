//go:build schema_verify

package handlers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	billingtax "frameworks/api_billing/internal/tax"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

func TestCryptoTaxDocuments_RealPG(t *testing.T) { //nolint:funlen // The table is the compliance matrix exercised against one real schema.
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	tierID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_tiers (id, tier_name, display_name, is_default_prepaid)
		VALUES ($1, $2, 'Tax test tier', true)
	`, tierID, "tax-test-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}

	type taxCase struct {
		legacyVIESOutage bool
		legacyGrace      bool
		revoked          bool
		business         bool
		freeze           bool
		name             string
		amountCents      int64
		legalName        string
		email            string
		company          string
		country          string
		vatNumber        string
		ipCountry        string
		wantKind         string
		wantEvidence     string
		wantConflict     bool
		wantTaxStatus    string
		wantError        bool
	}
	tests := []taxCase{
		{name: "frozen Greek business accepts GR prefix", freeze: true, amountCents: 500, legalName: "Original Buyer", email: "buyer@example.gr", company: "Original AE", country: "GR", vatNumber: "GR123456789", ipCountry: "GR", wantKind: "full", wantEvidence: "complete", wantTaxStatus: "reverse_charge"},
		{name: "legacy Austrian VAT claim survives VIES outage", legacyVIESOutage: true, amountCents: 500, legalName: "Austrian Buyer", email: "buyer@example.at", company: "Buyer GmbH", country: "AT", vatNumber: "ATU12345678", ipCountry: "AT", wantKind: "full", wantEvidence: "complete", wantTaxStatus: "reverse_charge"},
		{name: "legacy expired proof requires historical review", legacyGrace: true, amountCents: 500, legalName: "Grace Buyer", email: "buyer@example.at", company: "Buyer GmbH", country: "AT", vatNumber: "ATU12345678", ipCountry: "AT", wantKind: "full"},
		{name: "legacy German consumer", amountCents: 500, country: "DE", wantKind: "simplified", wantEvidence: "single_source", wantTaxStatus: "standard_vat"},
		{name: "legacy US consumer", amountCents: 500, country: "US", wantKind: "simplified", wantEvidence: "single_source", wantTaxStatus: "legacy_review"},
		{name: "frozen Dutch consumer identity", freeze: true, amountCents: 500, legalName: "Original Buyer", email: "buyer@example.nl", country: "NL", ipCountry: "NL", wantKind: "simplified", wantEvidence: "complete", wantTaxStatus: "standard_vat"},
		{name: "frozen German consumer policy", freeze: true, amountCents: 500, legalName: "Original Buyer", email: "buyer@example.de", country: "DE", ipCountry: "DE", wantKind: "simplified", wantEvidence: "complete", wantTaxStatus: "standard_vat"},
		{name: "frozen German business survives VIES outage", freeze: true, amountCents: 500, legalName: "Original Buyer", email: "buyer@example.de", company: "Original GmbH", country: "DE", vatNumber: "DE123456789", ipCountry: "DE", wantKind: "full", wantEvidence: "complete", wantTaxStatus: "reverse_charge"},
		{name: "frozen reviewed US business policy", business: true, freeze: true, amountCents: 500, legalName: "Original Buyer", email: "buyer@example.us", company: "Original Inc", country: "US", ipCountry: "US", wantKind: "full", wantEvidence: "complete", wantTaxStatus: "reviewed_foreign_tax"},
		{name: "received US payment retains document after revocation", revoked: true, business: true, freeze: true, amountCents: 500, legalName: "Original Buyer", email: "buyer@example.us", company: "Original Inc", country: "US", ipCountry: "US", wantKind: "full", wantEvidence: "complete", wantTaxStatus: "reviewed_foreign_tax"},
		{name: "anonymous no IP", amountCents: 500, wantKind: "simplified", wantEvidence: "missing", wantTaxStatus: "standard_vat"},
		{name: "IP only", amountCents: 500, ipCountry: "NL", wantKind: "simplified", wantEvidence: "single_source", wantTaxStatus: "standard_vat"},
		{name: "billing only", amountCents: 500, country: "NL", wantKind: "simplified", wantEvidence: "single_source", wantTaxStatus: "standard_vat"},
		{name: "matching", amountCents: 500, country: "NL", ipCountry: "NL", wantKind: "simplified", wantEvidence: "complete", wantTaxStatus: "standard_vat"},
		{name: "conflicting", amountCents: 500, country: "NL", ipCountry: "DE", wantKind: "simplified", wantEvidence: "conflict", wantConflict: true, wantTaxStatus: "standard_vat"},
		{name: "exactly EUR 100 remains simplified", amountCents: 10_000, wantKind: "simplified", wantEvidence: "missing", wantTaxStatus: "standard_vat"},
		{name: "EUR 100.01 needs full identity", amountCents: 10_001, wantKind: "full", wantError: true},
		{name: "domestic VIES", amountCents: 500, legalName: "Dutch Customer", email: "billing@example.nl", company: "Customer BV", country: "NL", vatNumber: "NL123456789B01", ipCountry: "NL", wantKind: "full", wantEvidence: "complete", wantTaxStatus: "standard_vat"},
		{name: "cross-border VIES", amountCents: 500, legalName: "German Customer", email: "billing@example.de", company: "Customer GmbH", country: "DE", vatNumber: "DE123456789", ipCountry: "DE", wantKind: "full", wantEvidence: "complete", wantTaxStatus: "reverse_charge"},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tenantID := uuid.NewString()
			var address any
			if test.country != "" {
				address = []byte(fmt.Sprintf(`{"street":"Main 1","city":"City","postal_code":"1000AA","country":%q}`, test.country))
			}
			if _, err := db.ExecContext(ctx, `
				INSERT INTO purser.tenant_subscriptions (
					id, tenant_id, tier_id, billing_model, billing_email, billing_name,
					billing_company, billing_address, tax_id
				) VALUES ($1, $2, $3, 'prepaid', NULLIF($4, ''), NULLIF($5, ''),
				          NULLIF($6, ''), $7::jsonb, NULLIF($8, ''))
			`, uuid.NewString(), tenantID, tierID, test.email, test.legalName, test.company, address, test.vatNumber); err != nil {
				t.Fatal(err)
			}

			if test.vatNumber != "" && billingtax.IsEU(test.country) {
				canonical, err := billingtax.CanonicalVATNumber(test.country, test.vatNumber)
				if err != nil {
					t.Fatal(err)
				}
				country := billingtax.VIESCountryCode(test.country)
				number := strings.TrimPrefix(canonical, country)
				digest := sha256.Sum256([]byte(country + number))
				if _, err := db.ExecContext(ctx, `
					INSERT INTO purser.vat_validation_evidence (
						tenant_id, country_code, vat_number_hash, vat_number_masked,
						valid, request_date, expires_at, raw_response
					) VALUES ($1, $2, $3, $4, true, CURRENT_DATE, NOW() + INTERVAL '1 hour', '{}')
				`, tenantID, country, hex.EncodeToString(digest[:]), country+"****"); err != nil {
					t.Fatal(err)
				}
			}

			handler := &X402Handler{
				db: db, supplierName: "FrameWorks B.V.", supplierAddress: "Amsterdam, NL",
				supplierVAT: "NL000000000B01", supplierRegistration: "12345678", supplierCountry: "NL",
				countryFromIP: func(string) string { return test.ipCountry },
			}
			profile := CryptoBillingProfile{
				Email: test.email, Name: test.legalName, Company: test.company,
				VATNumber: test.vatNumber,
			}
			if address != nil {
				profile.Address = append(json.RawMessage(nil), address.([]byte)...)
			}
			if test.freeze {
				profile.CustomerType = "consumer"
				if test.vatNumber != "" || test.business {
					profile.CustomerType = "business"
				}
				if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET billing_customer_type=$2 WHERE tenant_id=$1`, tenantID, profile.CustomerType); err != nil {
					t.Fatal(err)
				}
				if test.country == "DE" && test.vatNumber == "" {
				}
				requirement, err := handler.GetCryptoDocumentRequirement(ctx, tenantID, test.amountCents, "203.0.113.1")
				if err != nil {
					t.Fatal(err)
				}
				if requirement.Profile.TaxAssessment == nil {
					t.Fatal("payable quote has no tax assessment")
				}
				profile = requirement.Profile
				profile.TaxAssessment.PolicyVersion = billing.FrozenInclusiveCryptoTopupTaxPolicy
			}
			profileJSON, err := json.Marshal(profile)
			if err != nil {
				t.Fatal(err)
			}
			quoteID := uuid.NewString()
			observedAt := time.Date(2026, 8, 20, 9, index, 0, 0, time.UTC)
			if test.legacyGrace {
				observedAt = time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Microsecond)
			}
			if _, err := db.ExecContext(ctx, `
				INSERT INTO purser.x402_payment_quotes (
					id, tenant_id, resource, resource_class, network, asset, pay_to,
					amount_atomic, credit_amount_cents,
					requirements_json, status, expires_at, tax_document_kind,
					tax_profile_snapshot, created_at,
					original_amount_cents, original_currency, eur_amount_cents,
					fx_units_per_eur, fx_source, fx_reference_date
				) VALUES ($1, $2, 'mcp://test', 'mcp', 'eip155:8453',
				          '0x0000000000000000000000000000000000000001',
				          '0x0000000000000000000000000000000000000002',
				          5000000, $3::bigint, '{}', 'confirmed', NOW() + INTERVAL '1 hour',
				          $4, $5::jsonb, $6,
				          ROUND($3::bigint * 1.14)::bigint, 'USD', $3::bigint, 1.14, 'ecb', DATE '2026-08-19')
			`, quoteID, tenantID, test.amountCents, test.wantKind, profileJSON, observedAt); err != nil {
				t.Fatal(err)
			}
			if test.legacyGrace {
				if _, err = db.ExecContext(ctx, `UPDATE purser.vat_validation_evidence SET checked_at=NOW()-INTERVAL '25 hours',expires_at=NOW()-INTERVAL '1 hour' WHERE tenant_id=$1`, tenantID); err != nil {
					t.Fatal(err)
				}
				appconfigtest.Set(t, "VIES_ENDPOINT", "http://127.0.0.1:1")
			}
			if test.legacyVIESOutage {
				if _, err := db.ExecContext(ctx, `UPDATE purser.vat_validation_evidence SET request_date=$2::date,checked_at=$2::timestamptz-INTERVAL '1 hour',expires_at=$2::timestamptz+INTERVAL '23 hours' WHERE tenant_id=$1`, tenantID, observedAt); err != nil {
					t.Fatal(err)
				}
				appconfigtest.Set(t, "VIES_ENDPOINT", "http://127.0.0.1:1")
			}
			txHash := fmt.Sprintf("0x%064x", index+1)
			confirmedAt := time.Now().UTC()
			if test.legacyVIESOutage {
				confirmedAt = observedAt.Add(time.Minute)
			}
			if _, err := db.ExecContext(ctx, `
				INSERT INTO purser.x402_nonces (
					network, payer_address, nonce, tx_hash, tenant_id, amount_cents,
					status, quote_id, confirmed_at
				) VALUES ('base', '0x0000000000000000000000000000000000000003', $1, $2, $3, $4, 'confirmed', $5, $6)
			`, fmt.Sprintf("0x%x", index+1), txHash, tenantID, test.amountCents, quoteID, confirmedAt); err != nil {
				t.Fatal(err)
			}
			if test.freeze {
				if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET billing_name='Changed after quote' WHERE tenant_id=$1`, tenantID); err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(ctx, `DELETE FROM purser.vat_validation_evidence WHERE tenant_id=$1`, tenantID); err != nil {
					t.Fatal(err)
				}
				appconfigtest.Set(t, "VIES_ENDPOINT", "http://127.0.0.1:1")
				handler.supplierName = "Changed Seller"
				handler.supplierRegistration = "CHANGED"
				handler.supplierCountry = "BE"
				handler.countryFromIP = func(string) string { return "BE" }
			}
			invoiceNumber, err := handler.generateCryptoTopupInvoice(ctx, tenantID, test.amountCents, "x402_payment", txHash, "203.0.113.1", "base")
			if test.legacyGrace {
				var review *billingtax.ReviewError
				if !errors.As(err, &review) || review.Code != "vat_evidence_missing_at_tax_point" || invoiceNumber != "" {
					t.Fatalf("unproven historical payment became a reverse-charge invoice: invoice=%s err=%v", invoiceNumber, err)
				}
				var retained int64
				if readErr := db.QueryRowContext(ctx, `SELECT amount_cents FROM purser.x402_nonces WHERE tenant_id=$1::uuid AND tx_hash=$2 AND status='confirmed'`, tenantID, txHash).Scan(&retained); readErr != nil || retained != test.amountCents {
					t.Fatalf("review changed received money: amount=%d err=%v", retained, readErr)
				}
				return
			}
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), "complete profile") {
					t.Fatalf("generateCryptoTopupInvoice() error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if invoiceNumber == "" {
				t.Fatal("invoice number is empty")
			}

			table := "simplified_invoices"
			if test.wantKind == "full" {
				table = "crypto_invoices"
			}
			var evidence, taxStatus, fxRate, fxSource, service, registration, currency, referenceDate string
			var conflict bool
			var fxObserved time.Time
			var quantity, vatRateBps int
			var serviceDate time.Time
			var grossCents, amountEURCents, netEURCents, vatEURCents, netCents, vatCents int64
			query := fmt.Sprintf(`
				SELECT evidence_status, evidence_conflict, tax_validation_status,
				       fx_units_per_eur::text, fx_rate_source, fx_rate_observed_at,
				       supplier_registration_number, service_description,
				       service_quantity, service_date, currency, fx_reference_date::text,
				       gross_amount_cents, net_amount_cents, vat_amount_cents,
				       amount_eur_cents, net_eur_cents, vat_eur_cents, vat_rate_bps
				FROM purser.%s WHERE tenant_id = $1 AND reference_id = $2
			`, table)
			if err := db.QueryRowContext(ctx, query, tenantID, txHash).Scan(
				&evidence, &conflict, &taxStatus, &fxRate, &fxSource, &fxObserved,
				&registration, &service, &quantity, &serviceDate, &currency, &referenceDate,
				&grossCents, &netCents, &vatCents, &amountEURCents, &netEURCents, &vatEURCents, &vatRateBps,
			); err != nil {
				t.Fatal(err)
			}
			if evidence != test.wantEvidence || conflict != test.wantConflict || taxStatus != test.wantTaxStatus {
				t.Fatalf("evidence/tax = (%s, %t, %s), want (%s, %t, %s)", evidence, conflict, taxStatus, test.wantEvidence, test.wantConflict, test.wantTaxStatus)
			}
			if fxRate != "1.1400000000" || referenceDate != "2026-08-19" || !fxObserved.Equal(observedAt) || !strings.Contains(fxSource, "locked by x402 quote") {
				t.Fatalf("FX snapshot = (%s, %s, %s, %s), want locked quote values", fxRate, referenceDate, fxSource, fxObserved)
			}
			// The document keeps the USD the payer authorized and states the
			// credited EUR with VAT extracted from the EUR amount.
			wantNetEUR, wantVATEUR := extractVATInclusive(test.amountCents, vatRateBps)
			wantNet, wantVAT := extractVATInclusive(grossCents, vatRateBps)
			if currency != "USD" || grossCents != (test.amountCents*114+50)/100 || netCents != wantNet || vatCents != wantVAT {
				t.Fatalf("original amounts = (%s, %d, %d, %d)", currency, grossCents, netCents, vatCents)
			}
			if amountEURCents != test.amountCents || netEURCents != wantNetEUR || vatEURCents != wantVATEUR {
				t.Fatalf("EUR amounts = (%d, %d, %d), want (%d, %d, %d)", amountEURCents, netEURCents, vatEURCents, test.amountCents, wantNetEUR, wantVATEUR)
			}
			if taxStatus == "standard_vat" && vatEURCents == 0 {
				t.Fatal("standard VAT document states zero EUR VAT")
			}
			if test.freeze {
				var retained []byte
				var customer string
				customerColumn := "customer_name"
				if test.wantKind == "simplified" {
					customerColumn = "customer_snapshot->>'name'"
				}
				if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT tax_assessment,%s FROM purser.%s WHERE tenant_id=$1 AND reference_id=$2`, customerColumn, table), tenantID, txHash).Scan(&retained, &customer); err != nil {
					t.Fatal(err)
				}
				var assessment struct {
					ReviewRequired bool `json:"review_required"`
				}
				if err := json.Unmarshal(retained, &assessment); err != nil {
					t.Fatal(err)
				}
				if assessment.ReviewRequired {
					t.Fatalf("received payment review=%v, want %v", assessment.ReviewRequired, test.revoked)
				}
				if customer != test.legalName {
					t.Fatalf("issued document replaced the accepted customer: got %q want %q", customer, test.legalName)
				}
				if test.vatNumber != "" && billingtax.IsEU(test.country) && !strings.Contains(string(retained), "vat_validation") {
					t.Fatal("issued document lost the actual VIES validation evidence")
				}
				if !strings.Contains(string(retained), "quoted_assessment") || !strings.Contains(string(retained), "Original Buyer") {
					t.Fatalf("issued document lost frozen assessment evidence: %s", retained)
				}
				if _, err := db.ExecContext(ctx, fmt.Sprintf(`UPDATE purser.%s SET tax_assessment='{"changed":true}' WHERE tenant_id=$1 AND reference_id=$2`, table), tenantID, txHash); err == nil || !strings.Contains(err.Error(), "immutable") {
					t.Fatalf("issued assessment was mutable: %v", err)
				}
				again, err := handler.generateCryptoTopupInvoice(ctx, tenantID, test.amountCents, "x402_payment", txHash, "", "base")
				if err != nil || again != invoiceNumber {
					t.Fatalf("document replay changed the issued result: %q %v", again, err)
				}
			}
			if registration != "12345678" || service != "FrameWorks prepaid usage credit" || quantity != 1 || serviceDate.IsZero() {
				t.Fatalf("legal/service fields = (%s, %s, %d, %s)", registration, service, quantity, serviceDate)
			}
		})
	}
}

func TestCryptoTaxDocumentAnomalyReopensAndResolves_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	tenantID := uuid.NewString()
	const referenceID = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.crypto_accounting_anomalies (
			tenant_id, kind, network, reference_type, reference_id,
			amount_cents, detail, status, resolved_at, resolution_note
		) VALUES ($1, 'tax_document_missing', 'base', 'x402_payment', $2,
		          500, 'earlier failure', 'resolved', NOW(), 'earlier recovery')
	`, tenantID, referenceID); err != nil {
		t.Fatal(err)
	}

	recordCryptoAccountingAnomaly(ctx, db, logging.NewLogger(), tenantID,
		"tax_document_missing", "base", "x402_payment", referenceID,
		500, "document retry failed", map[string]any{"nonce_id": "nonce-1"})
	var state string
	var occurrences int
	var resolvedAt sql.NullTime
	if err := db.QueryRowContext(ctx, `
		SELECT status, occurrences, resolved_at
		FROM purser.crypto_accounting_anomalies
		WHERE kind='tax_document_missing' AND reference_type='x402_payment' AND reference_id=$1
	`, referenceID).Scan(&state, &occurrences, &resolvedAt); err != nil {
		t.Fatal(err)
	}
	if state != "open" || occurrences != 2 || resolvedAt.Valid {
		t.Fatalf("reopened anomaly = (%s, %d, %v), want (open, 2, NULL)", state, occurrences, resolvedAt)
	}

	resolveCryptoAccountingAnomaly(ctx, db, logging.NewLogger(),
		"tax_document_missing", "x402_payment", referenceID, "tax document created")
	var note sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT status, resolved_at, resolution_note
		FROM purser.crypto_accounting_anomalies
		WHERE kind='tax_document_missing' AND reference_type='x402_payment' AND reference_id=$1
	`, referenceID).Scan(&state, &resolvedAt, &note); err != nil {
		t.Fatal(err)
	}
	if state != "resolved" || !resolvedAt.Valid || note.String != "tax document created" {
		t.Fatalf("resolved anomaly = (%s, %v, %q)", state, resolvedAt, note.String)
	}
}

func TestReviewedForeignQuotesKeepAcceptedIdentityAfterPolicyChanges_RealPG(t *testing.T) { //nolint:funlen // Real quotes are followed through profile changes and received-payment documentation.
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	now := time.Now().UTC()
	storeRate(t, db, "USD", now, "1.2500")
	for _, customer := range []struct {
		name, kind, vat, documentKind string
		business                      bool
	}{
		{"legacy VAT business", "unspecified", "US-REG-123", "full", true},
		{"personal buyer", "consumer", "", "simplified", false},
	} {
		t.Run(customer.name, func(t *testing.T) {
			appconfigtest.Set(t, "SUPPLIER_COUNTRY", "NL")
			tenant, tier := uuid.NewString(), uuid.NewString()
			address := json.RawMessage(`{"street":"Main 1","city":"Seattle","postal_code":"98101","country":"US"}`)
			if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers(id,tier_name,display_name) VALUES($1,$2,'Foreign quote test')`, tier, "foreign-"+tier); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions(id,tenant_id,tier_id,billing_customer_type,billing_email,billing_name,billing_address,tax_id) VALUES($1,$2,$3,$4,'buyer@example.test','Accepted Buyer',$5::jsonb,NULLIF($6,''))`, uuid.NewString(), tenant, tier, customer.kind, address, customer.vat); err != nil {
				t.Fatal(err)
			}
			h := &X402Handler{db: db, logger: logging.NewLogger(), supplierCountry: "NL", supplierName: "Accepted Supplier", supplierAddress: "Amsterdam, NL", supplierVAT: "NL000000000B01", supplierRegistration: "12345678", topupUSDCents: 1000, countryFromIP: func(string) string { return "US" }}
			quotes, requirement, err := h.CreatePaymentQuotes(ctx, tenant, "mcp://foreign-test", "0x0000000000000000000000000000000000000002", []NetworkConfig{Networks["base"], Networks["arbitrum"]}, "203.0.113.1")
			if err != nil || len(quotes) != 2 || requirement.RequiresCompleteProfile {
				t.Fatalf("reviewed foreign customer could not obtain quotes: count=%d requirement=%+v err=%v", len(quotes), requirement, err)
			}
			if string(quotes[0].TaxProfileSnapshot) != string(quotes[1].TaxProfileSnapshot) || requirement.Profile.TaxAssessment == nil {
				t.Fatal("network options did not retain one accepted assessment")
			}
			if customer.business && requirement.Profile.CustomerType != "business" {
				t.Fatal("legacy VAT business was not classified consistently")
			}
			if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET billing_name='Changed Buyer',billing_address='{"country":"GB"}' WHERE tenant_id=$1`, tenant); err != nil {
				t.Fatal(err)
			}
			h.supplierCountry = "BE"
			h.supplierName = "Changed Supplier"
			txHash := "0x" + strings.ReplaceAll(uuid.NewString(), "-", "") + strings.Repeat("0", 32)
			nonceID := uuid.NewString()
			if _, err := db.ExecContext(ctx, `INSERT INTO purser.x402_nonces(id,network,payer_address,nonce,tx_hash,tenant_id,amount_cents,status,quote_id) VALUES($1,'base','0x0000000000000000000000000000000000000003',$2,$3,$4,$5,'pending',$6)`, nonceID, uuid.NewString(), txHash, tenant, quotes[0].CreditAmountCents, quotes[0].ID); err != nil {
				t.Fatal(err)
			}
			balance, err := h.confirmAndCreditSettlement(ctx, tenant, quotes[0].CreditAmountCents, nonceID, txHash, 10, 21000)
			if err != nil || balance != 800 {
				t.Fatalf("accepted receipt did not atomically credit net and retain its document: balance=%d %v", balance, err)
			}
			number, err := h.generateCryptoTopupInvoice(ctx, tenant, quotes[0].CreditAmountCents, "x402_payment", txHash, "203.0.113.1", "base")
			if err != nil || number == "" {
				t.Fatalf("received payment lost its document after setup changed: %q %v", number, err)
			}
			var invoiceID string
			var retained []byte
			var rate int
			var amount int64
			if err := db.QueryRowContext(ctx, `SELECT tax_decision,(tax_summary->0->>'rate_bps')::integer,gross_eur_cents,id::text FROM purser.billing_invoices WHERE tenant_id=$1 AND source_reference_type='x402_payment' AND source_reference_id=$2`, tenant, txHash).Scan(&retained, &rate, &amount, &invoiceID); err != nil {
				t.Fatal(err)
			}
			var assessment struct {
				ReviewRequired bool                 `json:"review_required"`
				Customer       CryptoBillingProfile `json:"customer"`
				Advance        *AdvanceMoney        `json:"advance_money"`
				DocumentKind   string               `json:"document_kind"`
				Supplier       DocumentSupplier     `json:"supplier"`
			}
			if err := json.Unmarshal(retained, &assessment); err != nil {
				t.Fatal(err)
			}
			if rate != 0 || amount != 800 || assessment.Customer.Name != "Accepted Buyer" || assessment.Supplier.Name != "Accepted Supplier" || assessment.ReviewRequired || assessment.DocumentKind != customer.documentKind || assessment.Advance == nil || assessment.Advance.NetEUR != 800 || assessment.Advance.VATEUR != 0 || assessment.Customer.TaxAssessment == nil || assessment.Customer.TaxAssessment.PolicyVersion != CryptoTopupTaxPolicyRef {
				t.Fatalf("accepted quote evidence or tax treatment changed: rate=%d amount=%d assessment=%+v", rate, amount, assessment)
			}

			var reviewCount int
			if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1 AND kind='tax_assessment_review' AND reference_type='invoice' AND reference_id=$2`, tenant, invoiceID).Scan(&reviewCount); err != nil || reviewCount != 0 {
				t.Fatalf("canonical review has no durable operator exception: count=%d %v", reviewCount, err)
			}
			replay, err := h.confirmAndCreditSettlement(ctx, tenant, quotes[0].CreditAmountCents, nonceID, txHash, 10, 21000)
			if err != nil || replay != 800 {
				t.Fatalf("receipt replay changed accepted credit: %d %v", replay, err)
			}
			var retainedAgain []byte
			if err = db.QueryRowContext(ctx, `SELECT tax_decision FROM purser.billing_invoices WHERE tenant_id=$1 AND id=$2`, tenant, invoiceID).Scan(&retainedAgain); err != nil || string(retainedAgain) != string(retained) {
				t.Fatalf("review or replay rewrote issued facts: %v", err)
			}

		})
	}
}
