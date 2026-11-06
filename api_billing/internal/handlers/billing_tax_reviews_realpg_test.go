//go:build schema_verify

package handlers

import (
	"encoding/json"
	"testing"

	"frameworks/api_billing/internal/database/purserdb"
)

func TestBillingTaxReviewOnlyNewFactsReopen_RealPG(t *testing.T) {
	db, _, _ := newStripeReassessmentService(t)
	tenant := seedPrepaidLedgerTenant(t, db)
	q := purserdb.New(db)
	p := purserdb.RecordBillingTaxReviewParams{TenantID: tenant, Kind: "provider_invoice_tax_review", Network: "stripe", ReferenceType: "stripe_invoice", ReferenceID: "in_review_facts", Detail: "Review original invoice", EvidenceJson: json.RawMessage(`{"code":"unsupported","input_fingerprint":"first"}`)}
	if err := q.RecordBillingTaxReview(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE purser.crypto_accounting_anomalies SET status='resolved',resolved_at=NOW(),resolution_note='Operator reviewed' WHERE tenant_id=$1::uuid AND kind=$2 AND reference_id=$3`, tenant, p.Kind, p.ReferenceID); err != nil {
		t.Fatal(err)
	}
	p.Detail = "Reworded operator guidance"
	if err := q.RecordBillingTaxReview(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	var status, note string
	var count int
	read := func() {
		t.Helper()
		if err := db.QueryRow(`SELECT status,occurrences,COALESCE(resolution_note,'') FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind=$2 AND reference_id=$3`, tenant, p.Kind, p.ReferenceID).Scan(&status, &count, &note); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if status != "resolved" || count != 1 || note != "Operator reviewed" {
		t.Fatalf("guidance-only change reopened same facts: %s count%d note%s", status, count, note)
	}
	p.EvidenceJson = json.RawMessage(`{"code":"unsupported","input_fingerprint":"changed"}`)
	if err := q.RecordBillingTaxReview(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	read()
	if status != "open" || count != 2 || note != "" {
		t.Fatalf("new facts failed to reopen review: %s count%d note%s", status, count, note)
	}
}
