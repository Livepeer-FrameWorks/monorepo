package grpc

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type assignmentReviewKey struct{}
type assignmentReview struct {
	at       time.Time
	expected string
	preview  bool
}
type assignmentQuoteReady struct {
	quote *purserpb.AdminAssignmentQuote
}

func (*assignmentQuoteReady) Error() string { return "assignment preview rolled back" }
func assignmentReviewFrom(ctx context.Context) *assignmentReview {
	v, ok := ctx.Value(assignmentReviewKey{}).(*assignmentReview)
	if !ok {
		return nil
	}
	return v
}
func requireAssignmentOperator(ctx context.Context) error {
	if !middleware.IsServiceCall(ctx) && !ctxkeys.IsPlatformOperator(ctx) {
		return status.Error(codes.PermissionDenied, "assignment review requires platform operator access")
	}
	return nil
}
func (s *PurserServer) PreviewAdminAssignTier(ctx context.Context, req *purserpb.AdminAssignTierRequest) (*purserpb.AdminAssignmentQuote, error) {
	if err := requireAssignmentOperator(ctx); err != nil {
		return nil, err
	}
	review := &assignmentReview{at: time.Now().UTC().Truncate(time.Microsecond), preview: true}
	_, err := s.AdminAssignTier(context.WithValue(ctx, assignmentReviewKey{}, review), req)
	var ready *assignmentQuoteReady
	if errors.As(err, &ready) {
		return ready.quote, nil
	}
	return nil, err
}
func (s *PurserServer) AdminAssignTierWithReview(ctx context.Context, req *purserpb.AdminAssignTierReviewedRequest) (*purserpb.AdminAssignTierResponse, error) {
	if err := requireAssignmentOperator(ctx); err != nil {
		return nil, err
	}
	if req.GetAssignment() == nil || req.GetQuotedAt() == nil || req.GetQuotedAt().CheckValid() != nil || len(req.GetExpectedFingerprint()) != 64 {
		return nil, status.Error(codes.InvalidArgument, "a current assignment quote and fingerprint are required")
	}
	at := req.GetQuotedAt().AsTime()
	now := time.Now()
	if at.After(now.Add(5*time.Second)) || !at.Add(5*time.Minute).After(now) {
		return nil, status.Error(codes.FailedPrecondition, "assignment quote expired; preview again")
	}
	return s.AdminAssignTier(context.WithValue(ctx, assignmentReviewKey{}, &assignmentReview{at: at, expected: req.GetExpectedFingerprint()}), req.Assignment)
}
func (s *PurserServer) checkAssignmentReviewSupported(ctx context.Context, tenant string) error {
	row, err := purserdb.New(s.db).GetAssignmentReviewSource(ctx, purserdb.GetAssignmentReviewSourceParams{TenantID: tenant})
	if err != nil {
		return status.Error(codes.Internal, "could not read assignment source")
	}
	var source struct {
		Subscription *struct {
			StripeSubscriptionID *string `json:"stripe_subscription_id"`
			MollieSubscriptionID *string `json:"mollie_subscription_id"`
		} `json:"subscription"`
	}
	if json.Unmarshal(row, &source) != nil {
		return status.Error(codes.Internal, "could not read assignment source")
	}
	if source.Subscription == nil {
		return status.Error(codes.FailedPrecondition, "initialise the tenant billing account before reviewing a plan assignment")
	}
	if source.Subscription.StripeSubscriptionID != nil && *source.Subscription.StripeSubscriptionID != "" || source.Subscription.MollieSubscriptionID != nil && *source.Subscription.MollieSubscriptionID != "" {
		return status.Error(codes.FailedPrecondition, "native provider subscriptions require their provider recovery workflow before a reviewed assignment")
	}
	grant, err := loadGrantForMutation(ctx, s.db, tenant)
	if err != nil {
		return err
	}
	if grant != nil {
		return status.Error(codes.FailedPrecondition, "review the operator grant separately before assigning an arrangement")
	}
	return nil
}
func finishAssignmentReview(ctx context.Context, q *purserdb.Queries, tenant, tier, model, reason string, source []byte, prepaid *handlers.PrepaidPhaseClose, postpaid *handlers.PostpaidPhaseClose) error {
	review := assignmentReviewFrom(ctx)
	if review == nil {
		return nil
	}
	balance, err := q.GetAssignmentReviewWallet(ctx, tenant)
	if err != nil {
		return txStatusErrorf(err, codes.Internal, "read reviewed wallet: %v", err)
	}
	if postpaid.AssignmentEffects().InvoiceStatus == "manual_review" {
		return status.Error(codes.FailedPrecondition, "closing invoice cannot be fully priced until its pricing or tax review is resolved")
	}
	var before struct {
		Wallet struct {
			Balance int64 `json:"balance"`
		} `json:"wallet"`
	}
	if decodeErr := json.Unmarshal(source, &before); decodeErr != nil {
		return decodeErr
	}
	effects := struct {
		Prepaid         handlers.AssignmentPhaseEffects `json:"prepaid"`
		Postpaid        handlers.AssignmentPhaseEffects `json:"postpaid"`
		WalletBeforeEUR int64                           `json:"wallet_before_eur_cents"`
		WalletAfterEUR  int64                           `json:"wallet_after_eur_cents"`
	}{prepaid.AssignmentEffects(), postpaid.AssignmentEffects(), before.Wallet.Balance, balance}
	effectsJSON, err := json.Marshal(effects)
	if err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		Source                             json.RawMessage
		Tenant, Tier, Model, Reason, Actor string
		At                                 time.Time
		Effects                            json.RawMessage
	}{source, tenant, tier, model, reason, middleware.GetUserID(ctx), review.at, effectsJSON})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(body)
	fingerprint := hex.EncodeToString(digest[:])
	quote := &purserpb.AdminAssignmentQuote{QuotedAt: timestamppb.New(review.at), ExpiresAt: timestamppb.New(review.at.Add(5 * time.Minute)), Fingerprint: fingerprint, EffectsJson: string(effectsJSON)}
	if review.preview {
		return &assignmentQuoteReady{quote}
	}
	if subtle.ConstantTimeCompare([]byte(fingerprint), []byte(review.expected)) != 1 {
		return status.Error(codes.Aborted, "billing or usage changed after review; preview again before assigning the arrangement")
	}
	return nil
}
