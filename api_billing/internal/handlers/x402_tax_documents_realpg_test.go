//go:build schema_verify

package handlers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

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
		name          string
		amountCents   int64
		legalName     string
		email         string
		company       string
		country       string
		vatNumber     string
		ipCountry     string
		wantKind      string
		wantEvidence  string
		wantConflict  bool
		wantTaxStatus string
		wantError     bool
	}
	tests := []taxCase{
		{name: "anonymous no IP", amountCents: 500, wantKind: "simplified", wantEvidence: "missing", wantTaxStatus: "standard_vat"},
		{name: "IP only", amountCents: 500, ipCountry: "NL", wantKind: "simplified", wantEvidence: "single_source", wantTaxStatus: "standard_vat"},
		{name: "billing only", amountCents: 500, country: "NL", wantKind: "simplified", wantEvidence: "single_source", wantTaxStatus: "standard_vat"},
		{name: "matching", amountCents: 500, country: "NL", ipCountry: "NL", wantKind: "simplified", wantEvidence: "complete", wantTaxStatus: "standard_vat"},
		{name: "conflicting", amountCents: 500, country: "NL", ipCountry: "DE", wantKind: "simplified", wantEvidence: "conflict", wantConflict: true, wantTaxStatus: "standard_vat"},
		{name: "exactly EUR 100 remains simplified", amountCents: 10_000, wantKind: "simplified", wantEvidence: "missing", wantTaxStatus: "standard_vat"},
		{name: "EUR 100.01 needs full identity", amountCents: 10_001, wantKind: "full", wantError: true},
		{name: "domestic VIES", amountCents: 500, legalName: "Dutch Customer", email: "billing@example.nl", company: "Customer BV", country: "NL", vatNumber: "NL123456789B01", ipCountry: "NL", wantKind: "full", wantEvidence: "complete", wantTaxStatus: "vies_valid_domestic"},
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

			if test.vatNumber != "" {
				country := test.vatNumber[:2]
				number := strings.TrimPrefix(test.vatNumber, country)
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

			profile := CryptoBillingProfile{
				Email: test.email, Name: test.legalName, Company: test.company,
				VATNumber: test.vatNumber,
			}
			if address != nil {
				profile.Address = append(json.RawMessage(nil), address.([]byte)...)
			}
			profileJSON, err := json.Marshal(profile)
			if err != nil {
				t.Fatal(err)
			}
			quoteID := uuid.NewString()
			observedAt := time.Date(2026, 8, 20, 9, index, 0, 0, time.UTC)
			if _, err := db.ExecContext(ctx, `
				INSERT INTO purser.x402_payment_quotes (
					id, tenant_id, resource, resource_class, network, asset, pay_to,
					amount_atomic, credit_amount_cents, eur_per_usd_rate,
					requirements_json, status, expires_at, tax_document_kind,
					tax_profile_snapshot, created_at
				) VALUES ($1, $2, 'mcp://test', 'mcp', 'eip155:8453',
				          '0x0000000000000000000000000000000000000001',
				          '0x0000000000000000000000000000000000000002',
				          5000000, $3, 0.8765432100, '{}', 'confirmed', NOW() + INTERVAL '1 hour',
				          $4, $5::jsonb, $6)
			`, quoteID, tenantID, test.amountCents, test.wantKind, profileJSON, observedAt); err != nil {
				t.Fatal(err)
			}
			txHash := fmt.Sprintf("0x%064x", index+1)
			if _, err := db.ExecContext(ctx, `
				INSERT INTO purser.x402_nonces (
					network, payer_address, nonce, tx_hash, tenant_id, amount_cents,
					status, quote_id
				) VALUES ('base', '0x0000000000000000000000000000000000000003', $1, $2, $3, $4, 'confirmed', $5)
			`, fmt.Sprintf("0x%x", index+1), txHash, tenantID, test.amountCents, quoteID); err != nil {
				t.Fatal(err)
			}

			handler := &X402Handler{
				db: db, supplierName: "FrameWorks B.V.", supplierAddress: "Amsterdam, NL",
				supplierVAT: "NL000000000B01", supplierRegistration: "12345678", supplierCountry: "NL",
				countryFromIP: func(string) string { return test.ipCountry },
			}
			invoiceNumber, err := handler.generateCryptoTopupInvoice(ctx, tenantID, test.amountCents, "x402_payment", txHash, "203.0.113.1", "base")
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
			var evidence, taxStatus, fxRate, fxSource, service, registration string
			var conflict bool
			var fxObserved time.Time
			var quantity int
			var serviceDate time.Time
			query := fmt.Sprintf(`
				SELECT evidence_status, evidence_conflict, tax_validation_status,
				       ecb_rate::text, fx_rate_source, fx_rate_observed_at,
				       supplier_registration_number, service_description,
				       service_quantity, service_date
				FROM purser.%s WHERE tenant_id = $1 AND reference_id = $2
			`, table)
			if err := db.QueryRowContext(ctx, query, tenantID, txHash).Scan(
				&evidence, &conflict, &taxStatus, &fxRate, &fxSource, &fxObserved,
				&registration, &service, &quantity, &serviceDate,
			); err != nil {
				t.Fatal(err)
			}
			if evidence != test.wantEvidence || conflict != test.wantConflict || taxStatus != test.wantTaxStatus {
				t.Fatalf("evidence/tax = (%s, %t, %s), want (%s, %t, %s)", evidence, conflict, taxStatus, test.wantEvidence, test.wantConflict, test.wantTaxStatus)
			}
			if fxRate != "0.876543" || !fxObserved.Equal(observedAt) || !strings.Contains(fxSource, "locked by x402 quote") {
				t.Fatalf("FX snapshot = (%s, %s, %s), want locked quote values", fxRate, fxSource, fxObserved)
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
