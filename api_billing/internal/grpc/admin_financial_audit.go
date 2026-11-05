package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AdminFinancialAuditPage retains source rows for an operator's evidence bundle;
// mutable payment statuses are observed at captured_at, not reconstructed as of
// the requested occurrence cutoff.
func (s *PurserServer) AdminFinancialAuditPage(ctx context.Context, req *purserpb.AdminFinancialAuditPageRequest) (*purserpb.AdminFinancialAuditPageResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if req.GetFrom() == nil || req.GetFrom().CheckValid() != nil {
		return nil, status.Error(codes.InvalidArgument, "a valid occurrence start is required")
	}
	if req.GetThrough() == nil {
		req = &purserpb.AdminFinancialAuditPageRequest{TenantId: req.GetTenantId(), Source: req.GetSource(), AfterId: req.GetAfterId(), From: req.GetFrom(), Through: timestamppb.New(now)}
	}
	if req.GetThrough().CheckValid() != nil || !req.GetThrough().AsTime().After(req.GetFrom().AsTime()) || req.GetThrough().AsTime().After(now) {
		return nil, status.Error(codes.InvalidArgument, "a valid occurrence interval ending no later than server now is required")
	}
	if strings.HasPrefix(req.GetSource(), "stripe_") {
		reader, ok := s.stripeClient.(interface {
			FinancialEvidencePage(context.Context, string, string, time.Time, time.Time) (*billingstripe.FinancialAuditPage, error)
		})
		if !ok {
			return nil, status.Error(codes.FailedPrecondition, "Stripe financial evidence reader is unavailable")
		}
		page, err := reader.FinancialEvidencePage(ctx, req.GetSource(), req.GetAfterId(), req.GetFrom().AsTime(), req.GetThrough().AsTime())
		if err != nil {
			return nil, status.Errorf(codes.Unavailable, "read Stripe financial evidence: %v", err)
		}
		s.logFinancialEvidenceRead(ctx, req, len(page.Records))
		return &purserpb.AdminFinancialAuditPageResponse{RecordsJson: page.Records, NextCursor: page.NextCursor, CapturedAt: timestamppb.Now(), EffectiveThrough: req.GetThrough()}, nil
	}
	if _, err := uuid.Parse(req.GetTenantId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "valid tenant_id required")
	}
	if req.GetSource() == "tax_profile" {
		if req.GetAfterId() != "" {
			return nil, status.Error(codes.InvalidArgument, "tax profile is not paginated")
		}
		row, err := purserdb.New(s.db).GetCryptoDocumentBillingProfile(ctx, req.GetTenantId())
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tenant has no billing profile")
		}
		if err != nil {
			return nil, status.Errorf(codes.Internal, "read billing profile: %v", err)
		}
		profile := handlers.CryptoBillingProfile{CustomerType: row.CustomerType, Email: row.BillingEmail.String, Name: row.BillingName.String, Company: row.BillingCompany.String, VATNumber: row.TaxID.String, Address: row.BillingAddress}
		fingerprint, err := handlers.CryptoBusinessProfileFingerprint(profile)
		if err != nil {
			return nil, status.Error(codes.FailedPrecondition, "save a complete business billing profile before reviewing establishment evidence")
		}
		revision, revisionErr := purserdb.New(s.db).GetBusinessEvidenceRevision(ctx, req.GetTenantId())
		if revisionErr != nil {
			return nil, status.Errorf(codes.Internal, "read evidence revision: %v", revisionErr)
		}
		payload, err := json.Marshal(map[string]any{"id": req.GetTenantId(), "customer_type": "business", "saved_customer_type": profile.CustomerType, "profile_sha256": fingerprint, "profile": profile, "evidence_revision": revision})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "encode tax profile: %v", err)
		}
		s.logFinancialEvidenceRead(ctx, req, 1)
		return &purserpb.AdminFinancialAuditPageResponse{RecordsJson: [][]byte{payload}, CapturedAt: timestamppb.Now(), EffectiveThrough: req.GetThrough()}, nil
	}
	switch req.GetSource() {
	case "invoices", "payments", "simplified_invoices", "crypto_invoices", "credit_notes", "provider_settlements", "pending_topups", "balance_transactions", "business_establishment_reviews", "provider_billing_documents", "provider_tax_profiles":
	default:
		return nil, status.Error(codes.InvalidArgument, "unsupported financial source")
	}
	if req.GetAfterId() != "" {
		if _, err := uuid.Parse(req.GetAfterId()); err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid audit cursor")
		}
	}
	if req.GetSource() == "payments" {
		ready, err := purserdb.New(s.db).FinancialAuditPaymentOwnershipReady(ctx, req.GetTenantId())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "verify payment ownership completeness: %v", err)
		}
		if !ready.Valid || !ready.Bool {
			return nil, status.Error(codes.FailedPrecondition, "payment ownership backfill must complete before exporting payment evidence")
		}
	}
	rows, err := purserdb.New(s.db).FinancialAuditPage(ctx, purserdb.FinancialAuditPageParams{TenantID: req.GetTenantId(), Source: req.GetSource(), FromAt: req.GetFrom().AsTime(), ThroughAt: req.GetThrough().AsTime(), AfterID: req.GetAfterId()})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read financial evidence: %v", err)
	}
	resp := &purserpb.AdminFinancialAuditPageResponse{CapturedAt: timestamppb.Now(), EffectiveThrough: req.GetThrough()}
	bytes := 0
	for index, row := range rows {
		if index == 100 || bytes+len(row.Payload) > 2*1024*1024 {
			if index == 0 {
				return nil, status.Error(codes.ResourceExhausted, "one financial record exceeds the evidence-page limit")
			}
			resp.NextCursor = rows[index-1].ID
			break
		}
		resp.RecordsJson = append(resp.RecordsJson, []byte(row.Payload))
		bytes += len(row.Payload)
	}
	s.logFinancialEvidenceRead(ctx, req, len(resp.RecordsJson))
	return resp, nil
}

func (s *PurserServer) logFinancialEvidenceRead(ctx context.Context, req *purserpb.AdminFinancialAuditPageRequest, count int) {
	if s.logger == nil {
		return
	}
	s.logger.WithFields(logging.Fields{"actor_user_id": ctxkeys.GetUserID(ctx), "tenant_id": req.GetTenantId(), "source": req.GetSource(), "from": req.GetFrom().AsTime(), "through": req.GetThrough().AsTime(), "records": count}).Info("Operator financial evidence read")
}
