package grpc

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"
	billingtax "frameworks/api_billing/internal/tax"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/countries"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AdminRecordBusinessEvidence records an operator's establishment review against
// the exact profile they inspected. It does not change a customer's tax rate.
func (s *PurserServer) AdminRecordBusinessEvidence(ctx context.Context, req *purserpb.AdminRecordBusinessEvidenceRequest) (*purserpb.AdminRecordBusinessEvidenceResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(req.GetTenantId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "valid tenant_id required")
	}
	actor, actorErr := uuid.Parse(ctxkeys.GetUserID(ctx))
	if actorErr != nil {
		return nil, status.Error(codes.Unauthenticated, "operator identity required")
	}
	if req.GetRevoke() {
		if req.GetProfileSha256() != "" || req.GetValidUntil() != nil {
			return nil, status.Error(codes.InvalidArgument, "tenant-wide revocation omits profile_sha256 and valid_until")
		}
	} else {
		hash, err := hex.DecodeString(req.GetProfileSha256())
		if err != nil || len(hash) != 32 {
			return nil, status.Error(codes.InvalidArgument, "the reviewed billing-profile SHA256 is required")
		}
	}

	reference := strings.TrimSpace(req.GetReference())
	if len(reference) == 0 || len([]rune(reference)) > 1024 {
		return nil, status.Error(codes.InvalidArgument, "a review reference of 1 to 1024 characters is required")
	}
	now := time.Now().UTC()
	if !req.GetRevoke() && (req.GetValidUntil() == nil || req.GetValidUntil().CheckValid() != nil || !req.GetValidUntil().AsTime().After(now) || req.GetValidUntil().AsTime().After(now.Add(366*24*time.Hour))) {
		return nil, status.Error(codes.InvalidArgument, "business evidence must expire within the next 366 days")
	}
	var response *purserpb.AdminRecordBusinessEvidenceResponse
	stage, err := runRetryableTx(ctx, s.db, func(tx *sql.Tx) error {
		q := purserdb.New(tx)
		row, err := q.LockBusinessEvidenceProfile(ctx, req.GetTenantId())
		if errors.Is(err, sql.ErrNoRows) {
			return status.Error(codes.NotFound, "tenant has no active billing profile")
		}
		if err != nil {
			return txStatusErrorf(err, codes.Internal, "read billing profile: %v", err)
		}
		fingerprint, country := "", ""
		if !req.GetRevoke() {
			revision, revisionErr := q.GetBusinessEvidenceRevision(ctx, req.GetTenantId())
			if revisionErr != nil {
				return txStatusErrorf(revisionErr, codes.Internal, "read business evidence revision: %v", revisionErr)
			}
			if req.GetExpectedRevision() < 0 || revision != req.GetExpectedRevision() {
				return status.Error(codes.Aborted, "business evidence changed since review; inspect the current profile and evidence revision")
			}
			if row.Status == "cancelled" {
				return status.Error(codes.FailedPrecondition, "cancelled billing profiles cannot receive new establishment approvals")
			}
			profile := handlers.CryptoBillingProfile{CustomerType: row.CustomerType, Email: row.BillingEmail, Name: row.BillingName, Company: row.BillingCompany, VATNumber: row.TaxID, Address: row.BillingAddress}
			fingerprint, err = handlers.CryptoBusinessProfileFingerprint(profile)
			if err != nil {
				return status.Error(codes.FailedPrecondition, "save a complete business billing profile before recording evidence")
			}
			if !strings.EqualFold(fingerprint, req.GetProfileSha256()) {
				return status.Error(codes.FailedPrecondition, "billing profile changed since review; inspect its current fingerprint before recording evidence")
			}
			var address struct {
				Country string `json:"country"`
			}
			if decodeErr := json.Unmarshal(profile.Address, &address); decodeErr != nil {
				return status.Error(codes.FailedPrecondition, "valid business address required")
			}
			country = countries.Normalize(address.Country)
			if !countries.IsValid(country) || billingtax.IsEU(country) {
				return status.Error(codes.InvalidArgument, "this evidence path is for non-EU business establishment; EU VAT claims use VIES")
			}
		}
		validUntil := now
		if req.GetValidUntil() != nil {
			validUntil = req.GetValidUntil().AsTime()
		}

		recorded, err := q.RecordBusinessEstablishmentReview(ctx, purserdb.RecordBusinessEstablishmentReviewParams{TenantID: req.GetTenantId(), ProfileSha256: fingerprint, Country: country, Reference: reference, ReviewedBy: actor.String(), ValidUntil: validUntil, IsRevocation: req.GetRevoke()})
		if err != nil {
			return txStatusErrorf(err, codes.Internal, "record business evidence: %v", err)
		}
		response = &purserpb.AdminRecordBusinessEvidenceResponse{Id: recorded.ID, TenantId: req.GetTenantId(), ProfileSha256: fingerprint, Country: country, Reference: reference, ValidUntil: timestamppb.New(recorded.ValidUntil), RecordedAt: timestamppb.New(recorded.CreatedAt), Revoked: req.GetRevoke(), Revision: recorded.Revision}
		return nil
	})
	if err != nil {
		if stage == txStageBegin || stage == txStageCommit {
			return nil, status.Errorf(codes.Internal, "record business evidence transaction: %v", err)
		}
		return nil, err
	}
	return response, nil
}
