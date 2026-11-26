package grpc

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"frameworks/api_billing/internal/database/purserdb"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"time"
)

type cancellationReviewKey struct{}
type cancellationReview struct {
	preview  bool
	at       time.Time
	expected string
	source   json.RawMessage
}
type cancellationQuoteReady struct {
	quote *purserpb.FreeCancellationQuote
}

func (*cancellationQuoteReady) Error() string { return "cancellation preview rolled back" }
func cancellationReviewFrom(ctx context.Context) *cancellationReview {
	v, ok := ctx.Value(cancellationReviewKey{}).(*cancellationReview)
	if !ok {
		return nil
	}
	return v
}
func (s *PurserServer) PreviewFreeCancellation(ctx context.Context, req *purserpb.ChangeBillingTierRequest) (*purserpb.FreeCancellationQuote, error) {
	review := &cancellationReview{preview: true, at: time.Now().UTC().Truncate(time.Microsecond)}
	_, err := s.ChangeBillingTier(context.WithValue(ctx, cancellationReviewKey{}, review), req)
	var ready *cancellationQuoteReady
	if errors.As(err, &ready) {
		return ready.quote, nil
	}
	return nil, err
}
func (s *PurserServer) ConfirmFreeCancellation(ctx context.Context, req *purserpb.ConfirmFreeCancellationRequest) (*purserpb.ChangeBillingTierResponse, error) {
	if req.GetQuotedAt() == nil || req.GetQuotedAt().CheckValid() != nil || len(req.GetExpectedFingerprint()) != 64 {
		return nil, status.Error(codes.InvalidArgument, "a current cancellation review is required")
	}
	at := req.QuotedAt.AsTime()
	if at.After(time.Now().Add(5*time.Second)) || !at.Add(5*time.Minute).After(time.Now()) {
		return nil, status.Error(codes.FailedPrecondition, "cancellation quote expired; review it again")
	}
	return s.ChangeBillingTier(context.WithValue(ctx, cancellationReviewKey{}, &cancellationReview{at: at, expected: req.ExpectedFingerprint}), &purserpb.ChangeBillingTierRequest{TenantId: req.TenantId, TierId: req.TierId})
}
func finishCancellationReview(ctx context.Context, tenant, tier string, effective time.Time) error {
	review := cancellationReviewFrom(ctx)
	if review == nil {
		return nil
	}
	data, err := json.Marshal(struct {
		Source        json.RawMessage
		Tenant, Tier  string
		At, Effective time.Time
	}{review.source, tenant, tier, review.at, effective.UTC()})
	if err != nil {
		return err
	}
	hash := sha256.Sum256(data)
	fingerprint := hex.EncodeToString(hash[:])
	if review.preview {
		return &cancellationQuoteReady{&purserpb.FreeCancellationQuote{QuotedAt: timestamppb.New(review.at), ExpiresAt: timestamppb.New(review.at.Add(5 * time.Minute)), EffectiveAt: timestamppb.New(effective), Fingerprint: fingerprint, Currency: "EUR", FutureUsageMessage: "No charge, wallet credit or cash refund is created by scheduling this change. Your paid plan remains active until the displayed date. Usage and existing invoices remain payable under that plan; future usage cannot be priced in advance. Free limits and your existing retention policy apply when the change completes."}}
	}
	if subtle.ConstantTimeCompare([]byte(fingerprint), []byte(review.expected)) != 1 {
		return status.Error(codes.Aborted, "billing changed after cancellation review; review the current terms again")
	}
	return nil
}
func loadCancellationReview(ctx context.Context, q *purserdb.Queries, tenant, targetName string, targetLevel, currentLevel int32) error {
	review := cancellationReviewFrom(ctx)
	if review == nil {
		return nil
	}
	if targetName != "free" || targetLevel >= currentLevel {
		return status.Error(codes.FailedPrecondition, "this review schedules a paid subscription's move to Free only")
	}
	source, err := q.GetAssignmentReviewSource(ctx, purserdb.GetAssignmentReviewSourceParams{TenantID: tenant, TargetTier: targetName})
	if err != nil {
		return err
	}
	review.source = source
	return nil
}
