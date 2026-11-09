//go:build schema_verify

package grpc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestBillingTaxReviewsOperatorScopeAndAcknowledgements_RealPG(t *testing.T) {
	db := startPurserTransitionRealPG(t)
	server := &PurserServer{db: db}
	tenant, other, actor := uuid.NewString(), uuid.NewString(), uuid.NewString()
	ctx := context.WithValue(grantNoticeOperatorCtx(), ctxkeys.KeyUserID, actor)
	q := purserdb.New(db)
	for i := 0; i < 201; i++ {
		owner := other
		if i%100 == 0 {
			owner = tenant
		}
		if err := q.RecordBillingTaxReview(ctx, purserdb.RecordBillingTaxReviewParams{TenantID: owner, Kind: "provider_tax_document_review", Network: "stripe", ReferenceType: "provider_invoice", ReferenceID: uuid.NewString(), Detail: "Retained original", EvidenceJson: json.RawMessage(`{"tax_status":"legacy_unknown","reason":"unmatched accepted claim"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := server.AdminListBillingTaxReviews(ctx, &purserpb.AdminListBillingTaxReviewsRequest{TenantId: tenant, Provider: "stripe", Category: "provider_tax_document_review", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.GetRecordsJson()) != 1 || page.GetNextCursor() == "" {
		t.Fatalf("missing bounded sparse-tenant page: %+v", page)
	}
	var first struct {
		ID       string          `json:"id"`
		Tenant   string          `json:"tenant_id"`
		Hash     string          `json:"evidence_sha256"`
		Evidence json.RawMessage `json:"evidence_json"`
	}
	if err = json.Unmarshal(page.GetRecordsJson()[0], &first); err != nil {
		t.Fatal(err)
	}
	if first.Tenant != tenant {
		t.Fatalf("foreign review leaked: %s", page.GetRecordsJson()[0])
	}
	ack := &purserpb.AdminAcknowledgeBillingTaxReviewRequest{TenantId: tenant, ReviewId: first.ID, EvidenceSha256: first.Hash, Reason: "Reviewed original; supplier absorbs historical VAT"}
	if _, err = server.AdminAcknowledgeBillingTaxReview(ctx, &purserpb.AdminAcknowledgeBillingTaxReviewRequest{TenantId: other, ReviewId: first.ID, EvidenceSha256: first.Hash, Reason: ack.Reason}); status.Code(err) != codes.NotFound {
		t.Fatalf("foreign tenant acknowledgement: %v", err)
	}
	if _, err = db.Exec(`CREATE FUNCTION purser.reject_ack_audit() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit unavailable'; END $$;
CREATE TRIGGER reject_ack_audit BEFORE INSERT ON purser.crypto_accounting_anomalies FOR EACH ROW WHEN(NEW.kind='operator_tax_review_acknowledgement') EXECUTE FUNCTION purser.reject_ack_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err = server.AdminAcknowledgeBillingTaxReview(ctx, ack); status.Code(err) != codes.Internal {
		t.Fatalf("audit failure must abort acknowledgement: %v", err)
	}
	var before string
	if err = db.QueryRow(`SELECT status FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant, first.ID).Scan(&before); err != nil || before != "open" {
		t.Fatalf("failed audit committed review resolution: %s %v", before, err)
	}
	if _, err = db.Exec(`DROP TRIGGER reject_ack_audit ON purser.crypto_accounting_anomalies; DROP FUNCTION purser.reject_ack_audit()`); err != nil {
		t.Fatal(err)
	}
	out, err := server.AdminAcknowledgeBillingTaxReview(ctx, ack)
	if err != nil || !out.GetAcknowledged() {
		t.Fatalf("acknowledgement failed: %+v %v", out, err)
	}
	if out, err = server.AdminAcknowledgeBillingTaxReview(ctx, ack); err != nil || out.GetAcknowledged() {
		t.Fatalf("unchanged retry must not append duplicate audit: %+v %v", out, err)
	}
	var actorSaved, statusSaved string
	var evidenceSame bool
	var count int
	if err = db.QueryRow(`SELECT resolved_by::text,evidence_json=$3::jsonb,status FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant, first.ID, []byte(first.Evidence)).Scan(&actorSaved, &evidenceSame, &statusSaved); err != nil {
		t.Fatal(err)
	}
	if actorSaved != actor || statusSaved != "resolved" || !evidenceSame {
		t.Fatalf("actor/status lost: %s %s", actorSaved, statusSaved)
	}
	if err = db.QueryRow(`SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='operator_tax_review_acknowledgement' AND evidence_json->>'review_id'=$2 AND evidence_json->>'evidence_sha256'=$3 AND resolved_by=$4::uuid`, tenant, first.ID, first.Hash, actor).Scan(&count); err != nil || count != 1 {
		t.Fatalf("durable audit missing/duplicated: %d %v", count, err)
	}
	var reference string
	if err = db.QueryRow(`SELECT reference_id FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant, first.ID).Scan(&reference); err != nil {
		t.Fatal(err)
	}
	if err = q.RecordBillingTaxReview(ctx, purserdb.RecordBillingTaxReviewParams{TenantID: tenant, Kind: "provider_tax_document_review", Network: "stripe", ReferenceType: "provider_invoice", ReferenceID: reference, Detail: "New provider facts", EvidenceJson: json.RawMessage(`{"tax_status":"legacy_unknown","reason":"new facts"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = server.AdminAcknowledgeBillingTaxReview(ctx, ack); status.Code(err) != codes.Aborted {
		t.Fatalf("stale evidence acknowledgement must fail: %v", err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1::uuid AND kind='operator_tax_review_acknowledgement' AND evidence_json->>'review_id'=$2`, tenant, first.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("reopen lost earlier audit: %d %v", count, err)
	}
	var fundsID string
	if err = db.QueryRow(`INSERT INTO purser.crypto_accounting_anomalies(tenant_id,kind,network,reference_type,reference_id,detail) VALUES($1::uuid,'x402_consumed_authorization_missing_transaction','base','x402_nonce',$2,'Recover transaction') RETURNING id::text`, tenant, uuid.NewString()).Scan(&fundsID); err != nil {
		t.Fatal(err)
	}
	if _, err = server.AdminAcknowledgeBillingTaxReview(ctx, &purserpb.AdminAcknowledgeBillingTaxReviewRequest{TenantId: tenant, ReviewId: fundsID, EvidenceSha256: first.Hash, Reason: "A tax acknowledgement cannot fix funds"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("funds condition was dismissible: %v", err)
	}
	all, err := server.AdminListBillingTaxReviews(ctx, &purserpb.AdminListBillingTaxReviewsRequest{TenantId: tenant, Status: "all"})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range all.GetRecordsJson() {
		var row struct {
			Kind string `json:"kind"`
		}
		if err = json.Unmarshal(raw, &row); err != nil {
			t.Fatal(err)
		}
		if row.Kind == "x402_consumed_authorization_missing_transaction" {
			t.Fatal("funds correctness condition exposed as dismissible tax review")
		}
	}
	archive, err := server.AdminFinancialAuditPage(ctx, &purserpb.AdminFinancialAuditPageRequest{TenantId: tenant, Source: "billing_tax_reviews", From: timestamppb.New(time.Unix(0, 0))})
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.GetRecordsJson()) != 4 {
		t.Fatalf("financial archive must retain reviews plus independent acknowledgement audit: %d", len(archive.GetRecordsJson()))
	}
	if _, err = db.Exec(`INSERT INTO purser.provider_tax_assessments(tenant_id,profile_sha256,assessed_at,assessment) VALUES($1::uuid,$2,NOW(),'{"accepted_terms":"retained before provider write"}')`, tenant, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	archive, err = server.AdminFinancialAuditPage(ctx, &purserpb.AdminFinancialAuditPageRequest{TenantId: tenant, Source: "provider_tax_assessments", From: timestamppb.New(time.Unix(0, 0))})
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.GetRecordsJson()) != 1 {
		t.Fatalf("provider assessment history missing from archive: %+v", archive)
	}
}

func TestCryptoReadinessIgnoresFiatTaxReviews_RealPG(t *testing.T) {
	db := startPurserTransitionRealPG(t)
	tenant := uuid.NewString()
	q := purserdb.New(db)
	if _, err := db.Exec(`INSERT INTO purser.crypto_accounting_anomalies(tenant_id,kind,network,reference_type,reference_id,detail) VALUES($1::uuid,'provider_tax_document_review','stripe','provider_invoice',$2,'Retained tax review')`, tenant, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	count, err := q.CountOpenCryptoAccountingAnomalies(t.Context())
	if err != nil || count != 0 {
		t.Fatalf("fiat tax review must not block crypto custody readiness: count=%d err=%v", count, err)
	}
	if _, err = db.Exec(`INSERT INTO purser.crypto_accounting_anomalies(tenant_id,kind,network,reference_type,reference_id,detail) VALUES($1::uuid,'tax_document_missing','base','crypto_payment',$2,'Recover missing crypto tax document')`, tenant, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	count, err = q.CountOpenCryptoAccountingAnomalies(t.Context())
	if err != nil || count != 1 {
		t.Fatalf("actual crypto accounting defect must still block readiness: count=%d err=%v", count, err)
	}
}

func TestCryptoReadinessNeedsOnlyEffectiveDutchRate_RealPG(t *testing.T) {
	db := startPurserTransitionRealPG(t)
	configureRPCTaxSupplier(t)
	if _, err := db.Exec(`DELETE FROM purser.vat_rate_periods WHERE country_code<>'NL'`); err != nil {
		t.Fatal(err)
	}
	out, err := (&PurserServer{db: db, logger: logging.NewLogger()}).GetCryptoReadiness(grantNoticeOperatorCtx(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range out.GetChecks() {
		if check.GetComponent() == "tax.vat_rate_catalog" {
			if !check.GetReady() {
				t.Fatalf("effective Dutch rate must suffice for fixed NL supplier policy: %s", check.GetDetail())
			}
			return
		}
	}
	t.Fatal("VAT readiness check missing")
}
