//go:build schema_verify

package handlers

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
)

const providerGraceBackfillMigration = "migrations/purser/v0.3.14/postdeploy/019_backfill_provider_tax_evidence_reviews.sql"

func TestProviderGraceBackfillPreservesIssuedFactsAndClosedReviews_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	q := purserdb.New(db)
	type retained struct {
		tenant, id, kind, status string
		proof                    json.RawMessage
		facts                    []byte
		wantReview, closed       bool
	}
	var documents []retained
	for index, tc := range []struct {
		kind, status, proof string
		wantReview, closed  bool
	}{
		{"provider_invoice", "ready", `{"evidence":{"vat_validation_mode":"outage_grace"}}`, true, false},
		{"provider_credit_note", "ready", `{"vat_validation_mode":"outage_grace"}`, true, false},
		{"provider_invoice", "ready", `{"review_required":true}`, true, false},
		{"provider_invoice", "ready", `{"evidence":{"review_required":true}}`, true, true},
		{"provider_invoice", "ready", `{"evidence":{"vat_validation_mode":"fresh_cache"}}`, false, false},
		{"provider_invoice", "legacy_unknown", `{"evidence":{"vat_validation_mode":"outage_grace"}}`, false, false},
	} {
		tenant := seedPrepaidLedgerTenant(t, db)
		name := fmt.Sprintf("in_grace_backfill_%d", index)
		proof := json.RawMessage(tc.proof)
		stored, err := q.StoreProviderBillingDocument(t.Context(), purserdb.StoreProviderBillingDocumentParams{
			TenantID: tenant, ProviderAccountID: "acct_backfill", DocumentKind: tc.kind,
			ProviderDocumentID: name, OriginalNumber: "ORIGINAL-" + name, Status: "paid", Currency: "EUR",
			NetMinor: sql.NullInt64{Int64: 10000, Valid: true}, VatMinor: sql.NullInt64{Int64: 0, Valid: true}, GrossMinor: 10000,
			TaxStatus: tc.status, TaxGroups: json.RawMessage(`[]`), SupplierSnapshot: json.RawMessage(`{"name":"Original supplier"}`),
			CustomerSnapshot: json.RawMessage(`{"name":"Original buyer"}`), RawFacts: json.RawMessage(`{"total":10000}`),
			TaxAssessment: proof, OriginalPdf: []byte("%PDF-1.4 original"), PdfSha256: sql.NullString{String: fmt.Sprintf("%x", sha256.Sum256([]byte("%PDF-1.4 original"))), Valid: true}, IssuedAt: time.Now().Add(-24 * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		if tc.closed {
			if err := q.RecordBillingTaxReview(t.Context(), purserdb.RecordBillingTaxReviewParams{TenantID: tenant, Kind: "provider_tax_evidence_review", Network: "stripe", ReferenceType: tc.kind, ReferenceID: stored.ID, Detail: "Reviewed original", EvidenceJson: proof}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE purser.crypto_accounting_anomalies SET status='resolved',resolved_at=NOW(),resolution_note='Operator accepted original proof' WHERE tenant_id=$1::uuid AND kind='provider_tax_evidence_review' AND reference_id=$2`, tenant, stored.ID); err != nil {
				t.Fatal(err)
			}
		}
		documents = append(documents, retained{tenant: tenant, id: stored.ID, kind: tc.kind, status: tc.status, proof: proof, facts: stripeCapturedTaxFacts(t, db, tenant, stored.ID), wantReview: tc.wantReview, closed: tc.closed})
	}
	for pass := range 2 {
		applyMigrationFile(t, db, providerGraceBackfillMigration)
		for _, document := range documents {
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='provider_tax_evidence_review' AND reference_type=$2 AND reference_id=$3`, document.tenant, document.kind, document.id).Scan(&count); err != nil {
				t.Fatal(err)
			}
			wantCount := 0
			if document.wantReview {
				wantCount = 1
			}
			if count != wantCount {
				t.Fatalf("backfill pass%d document%s review count%d want%d", pass, document.id, count, wantCount)
			}
			if count != 0 {
				var status, note string
				var occurrences int
				var sameProof bool
				if err := db.QueryRow(`SELECT status,COALESCE(resolution_note,''),occurrences,evidence_json=$4::jsonb FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='provider_tax_evidence_review' AND reference_type=$2 AND reference_id=$3`, document.tenant, document.kind, document.id, document.proof).Scan(&status, &note, &occurrences, &sameProof); err != nil {
					t.Fatal(err)
				}
				wantStatus := "open"
				if document.closed {
					wantStatus = "resolved"
				}
				if status != wantStatus || occurrences != 1 || !sameProof || document.closed && note != "Operator accepted original proof" {
					t.Fatalf("backfill changed retained review: status%s occurrences%d sameProof%t note%q", status, occurrences, sameProof, note)
				}
			}
			if !bytes.Equal(document.facts, stripeCapturedTaxFacts(t, db, document.tenant, document.id)) {
				t.Fatal("backfill rewrote issued amount, proof, parties, number, PDF or retention")
			}
		}
	}
}

func TestProviderDocumentsHaveNoInactiveReassessmentSchema_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	var columns, guards, indexes int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema='purser' AND table_name='provider_billing_documents' AND column_name IN ('reviewed_tax_status','reviewed_tax_assessment','tax_reviewed_at')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pg_trigger WHERE tgrelid='purser.provider_billing_documents'::regclass AND tgname='provider_document_review_guard'`).Scan(&guards); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pg_indexes WHERE schemaname='purser' AND tablename='provider_billing_documents' AND indexname='idx_provider_document_tax_review_due'`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if columns != 0 || guards != 0 || indexes != 0 {
		t.Fatalf("inactive reassessment schema remains: columns%d guards%d indexes%d", columns, guards, indexes)
	}
}
