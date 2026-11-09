package grpc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Acknowledgement records an operator's tax review; it cannot dismiss missing
// funds, missing documents, scanner faults or pending VIES verification work.
var manualTaxReviewKinds = []string{"provider_tax_review", "provider_invoice_tax_review", "provider_tax_document_review", "provider_tax_evidence_review", "invoice_tax_review", "invoice_tax_evidence_review", "tax_assessment_review"}

const taxReviewAcknowledgementKind = "operator_tax_review_acknowledgement"

func manualTaxReviewKind(kind string) bool {
	for _, candidate := range manualTaxReviewKinds {
		if candidate == kind {
			return true
		}
	}
	return false
}

func billingReviewJSON(raw json.RawMessage) ([]byte, string, error) {
	var row map[string]json.RawMessage
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(row["evidence_json"])
	hash := hex.EncodeToString(sum[:])
	encoded, err := json.Marshal(hash)
	if err != nil {
		return nil, "", err
	}
	row["evidence_sha256"] = encoded
	result, err := json.Marshal(row)
	return result, hash, err
}

func (s *PurserServer) AdminListBillingTaxReviews(ctx context.Context, req *purserpb.AdminListBillingTaxReviewsRequest) (*purserpb.AdminListBillingTaxReviewsResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(req.GetTenantId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	if req.GetAfterId() != "" {
		if _, err := uuid.Parse(req.GetAfterId()); err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid review cursor")
		}
	}
	provider, category := strings.TrimSpace(req.GetProvider()), strings.TrimSpace(req.GetCategory())
	if len(provider) > 64 || strings.ContainsFunc(provider, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return nil, status.Error(codes.InvalidArgument, "invalid provider/network scope")
	}
	if category != "" && !manualTaxReviewKind(category) && category != taxReviewAcknowledgementKind {
		return nil, status.Error(codes.InvalidArgument, "unsupported tax review category")
	}
	reviewStatus := req.GetStatus()
	if reviewStatus == "" {
		reviewStatus = "open"
	}
	if reviewStatus != "open" && reviewStatus != "resolved" && reviewStatus != "all" {
		return nil, status.Error(codes.InvalidArgument, "status must be open, resolved or all")
	}
	limit := req.GetLimit()
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return nil, status.Error(codes.InvalidArgument, "limit must be 1..100")
	}
	kinds := append(append([]string(nil), manualTaxReviewKinds...), taxReviewAcknowledgementKind)
	rows, err := purserdb.New(s.db).ListBillingTaxReviews(ctx, purserdb.ListBillingTaxReviewsParams{TenantID: req.GetTenantId(), Provider: provider, Category: category, ReviewStatus: reviewStatus, AfterID: req.GetAfterId(), RowLimit: int64(limit) + 1, Kinds: kinds})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read tax reviews: %v", err)
	}
	out := &purserpb.AdminListBillingTaxReviewsResponse{}
	for i, raw := range rows {
		if i == int(limit) {
			break
		}
		body, _, err := billingReviewJSON(raw)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "encode tax review: %v", err)
		}
		out.RecordsJson = append(out.RecordsJson, body)
	}
	if len(rows) > int(limit) {
		var last struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rows[limit-1], &last); err != nil {
			return nil, status.Errorf(codes.Internal, "read review cursor: %v", err)
		}
		out.NextCursor = last.ID
	}
	return out, nil
}

func (s *PurserServer) AdminAcknowledgeBillingTaxReview(ctx context.Context, req *purserpb.AdminAcknowledgeBillingTaxReviewRequest) (*purserpb.AdminAcknowledgeBillingTaxReviewResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	for _, id := range []string{req.GetTenantId(), req.GetReviewId()} {
		if _, err := uuid.Parse(id); err != nil {
			return nil, status.Error(codes.InvalidArgument, "tenant_id and review_id must be UUIDs")
		}
	}
	actor, err := uuid.Parse(ctxkeys.GetUserID(ctx))
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "operator user identity required")
	}
	expected, err := hex.DecodeString(req.GetEvidenceSha256())
	if err != nil || len(expected) != sha256.Size {
		return nil, status.Error(codes.InvalidArgument, "reviewed evidence SHA256 required")
	}
	reason := strings.TrimSpace(req.GetReason())
	if reason == "" || len([]rune(reason)) > 2048 {
		return nil, status.Error(codes.InvalidArgument, "reason must contain 1..2048 characters")
	}
	out := &purserpb.AdminAcknowledgeBillingTaxReviewResponse{}
	stage, err := runRetryableTx(ctx, s.db, func(tx *sql.Tx) error {
		q := purserdb.New(tx)
		raw, readErr := q.LockBillingTaxReview(ctx, purserdb.LockBillingTaxReviewParams{TenantID: req.GetTenantId(), ReviewID: req.GetReviewId()})
		if errors.Is(readErr, sql.ErrNoRows) {
			return status.Error(codes.NotFound, "tenant tax review not found")
		}
		if readErr != nil {
			return txStatusErrorf(readErr, codes.Internal, "lock tax review: %v", readErr)
		}
		var row struct {
			Kind     string          `json:"kind"`
			Provider string          `json:"network"`
			Status   string          `json:"status"`
			Evidence json.RawMessage `json:"evidence_json"`
		}
		if decodeErr := json.Unmarshal(raw, &row); decodeErr != nil {
			return status.Errorf(codes.Internal, "decode tax review: %v", decodeErr)
		}
		if !manualTaxReviewKind(row.Kind) {
			return status.Error(codes.FailedPrecondition, "this condition requires correction or automatic verification, not tax acknowledgement")
		}
		body, hash, encodeErr := billingReviewJSON(raw)
		if encodeErr != nil {
			return status.Errorf(codes.Internal, "encode tax review: %v", encodeErr)
		}
		if !strings.EqualFold(hash, req.GetEvidenceSha256()) {
			return status.Error(codes.Aborted, "tax review evidence changed; inspect the current evidence before acknowledging")
		}
		out.RecordJson = body
		out.Acknowledged = false
		if row.Status == "resolved" {
			return nil
		}
		if _, updateErr := q.AcknowledgeBillingTaxReview(ctx, purserdb.AcknowledgeBillingTaxReviewParams{TenantID: req.GetTenantId(), ReviewID: req.GetReviewId(), ActorID: actor.String(), Reason: reason}); updateErr != nil {
			return txStatusErrorf(updateErr, codes.Internal, "acknowledge tax review: %v", updateErr)
		}
		evidence, encodeErr := json.Marshal(map[string]any{"review_id": req.GetReviewId(), "category": row.Kind, "evidence_sha256": hash, "reviewed_evidence": row.Evidence, "actor_id": actor.String(), "reason": reason})
		if encodeErr != nil {
			return status.Errorf(codes.Internal, "encode acknowledgement audit: %v", encodeErr)
		}
		if auditErr := q.RecordBillingTaxReviewAcknowledgement(ctx, purserdb.RecordBillingTaxReviewAcknowledgementParams{TenantID: req.GetTenantId(), Provider: row.Provider, AuditID: uuid.NewString(), ActorID: actor.String(), Reason: reason, Evidence: evidence}); auditErr != nil {
			return txStatusErrorf(auditErr, codes.Internal, "retain acknowledgement audit: %v", auditErr)
		}
		updated, readErr := q.LockBillingTaxReview(ctx, purserdb.LockBillingTaxReviewParams{TenantID: req.GetTenantId(), ReviewID: req.GetReviewId()})
		if readErr != nil {
			return txStatusErrorf(readErr, codes.Internal, "read acknowledged tax review: %v", readErr)
		}
		out.RecordJson, _, encodeErr = billingReviewJSON(updated)
		out.Acknowledged = true
		return encodeErr
	})
	if err != nil {
		if stage == txStageBegin || stage == txStageCommit {
			return nil, status.Errorf(codes.Internal, "acknowledge tax review transaction: %v", err)
		}
		return nil, err
	}
	return out, nil
}
