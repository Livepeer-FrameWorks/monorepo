package grpc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// tenantEntitlementAPI is the narrow Quartermaster surface the signed-policy-
// bundle path depends on. The concrete *qmclient.GRPCClient satisfies it.
type tenantEntitlementAPI interface {
	GetTenantEntitlement(ctx context.Context, tenantID string) (*quartermasterpb.GetTenantEntitlementResponse, error)
}

const (
	// policyBundleSoftTTL is how long Foghorn may serve a cached bundle
	// before background-refreshing. Matches the Commodore client cache TTL
	// already used for ResolvePlaybackPolicy.
	policyBundleSoftTTL = 60 * time.Second
	// policyBundleHardTTL is the ceiling — Foghorn must refuse to use a
	// bundle past this duration even under central outage. Sized so a 30-
	// minute Commodore outage still lets existing streams play through to
	// natural end without dropping under-cached policy.
	policyBundleHardTTL = 30 * time.Minute
)

// signedBundleClaims is the canonical JWT payload Commodore signs for the
// (tenant, stream) pair. Foghorn reads this back after verifying the
// signature; bundle_version is the monotonic watermark consulted on
// revocation events.
type signedBundleClaims struct {
	jwt.RegisteredClaims
	TenantID          string          `json:"tenant_id"`
	StreamID          string          `json:"stream_id"`
	BundleVersion     int64           `json:"bundle_version"`
	AllowedClusterIDs []string        `json:"allowed_cluster_ids,omitempty"`
	TenantPlanClass   string          `json:"tenant_plan_class,omitempty"`
	PlaybackPolicy    json.RawMessage `json:"playback_policy,omitempty"`
}

// GetSignedPolicyBundle mints a fresh signed policy bundle for a (tenant_id,
// stream_id) pair, persists it in commodore.policy_bundle_versions with the
// next monotonic bundle_version, and returns the signed JWT envelope.
//
// Revocation: callers (Purser plan downgrade, Quartermaster entitlement
// removal, etc.) enqueue a `bundle_revoke` row into
// playback_policy_invalidation_outbox with the minimum-acceptable
// bundle_version in the bundle_min_version column. Foghorn's cache watermark
// bumps to that value on receipt, invalidating prior bundles.
func (s *CommodoreServer) GetSignedPolicyBundle(ctx context.Context, req *commodorepb.GetSignedPolicyBundleRequest) (*commodorepb.GetSignedPolicyBundleResponse, error) {
	// Service-token only. The bundle is a Foghorn-facing artifact minted for the
	// admission path; no user session should reach it. Defense-in-depth — it also
	// keeps the downstream service-only Quartermaster entitlement RPC reachable
	// with Commodore's own service token rather than a forwarded user JWT.
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "service token required")
	}

	tenantID := strings.TrimSpace(req.GetTenantId())
	streamID := strings.TrimSpace(req.GetStreamId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	secret, err := policyBundleSigningSecret()
	if err != nil {
		s.logger.WithError(err).Error("policy bundle signing secret unavailable")
		return nil, status.Error(codes.Internal, "signing key unavailable")
	}

	policyJSON, internalName, err := s.lookupPolicyForStream(ctx, tenantID, streamID)
	if err != nil {
		return nil, err
	}
	_ = internalName // reserved for future correlation logging

	allowed, planClass, err := s.lookupTenantClusterEntitlement(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	issuedAt := now
	softExpiresAt := now.Add(policyBundleSoftTTL)
	expiresAt := now.Add(policyBundleHardTTL)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin bundle transaction: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on non-commit paths
	queries := commodoredb.New(tx)
	if lockErr := queries.LockPolicyBundleStream(ctx, commodoredb.LockPolicyBundleStreamParams{TenantID: tenantID, StreamID: streamID}); lockErr != nil {
		return nil, status.Errorf(codes.Internal, "lock bundle version: %v", lockErr)
	}
	bundleVersion, err := nextPolicyBundleVersionWith(ctx, tx, tenantID, streamID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "next bundle version: %v", err)
	}

	claims := signedBundleClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "commodore",
			Subject:   tenantID,
			Audience:  jwt.ClaimStrings{"foghorn"},
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			NotBefore: jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
		TenantID:          tenantID,
		StreamID:          streamID,
		BundleVersion:     bundleVersion,
		AllowedClusterIDs: allowed,
		TenantPlanClass:   planClass,
		PlaybackPolicy:    policyJSON,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	bundleJWT, err := token.SignedString(secret)
	if err != nil {
		s.logger.WithError(err).Error("sign policy bundle failed")
		return nil, status.Error(codes.Internal, "sign bundle")
	}

	if err := persistPolicyBundleWith(ctx, tx, tenantID, streamID, bundleVersion, bundleJWT, issuedAt, expiresAt); err != nil {
		s.logger.WithError(err).WithFields(map[string]any{
			"tenant_id":      tenantID,
			"stream_id":      streamID,
			"bundle_version": bundleVersion,
		}).Error("persist policy bundle failed")
		return nil, status.Errorf(codes.Internal, "persist bundle: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit bundle: %v", err)
	}

	return &commodorepb.GetSignedPolicyBundleResponse{
		Bundle: &commodorepb.SignedPolicyBundle{
			BundleJwt:     bundleJWT,
			BundleVersion: bundleVersion,
			IssuedAt:      timestamppb.New(issuedAt),
			SoftExpiresAt: timestamppb.New(softExpiresAt),
			ExpiresAt:     timestamppb.New(expiresAt),
			TenantId:      tenantID,
			StreamId:      streamID,
		},
	}, nil
}

// policyBundleSigningSecret returns the shared HMAC secret used to sign
// policy bundles. Sourced from POLICY_BUNDLE_SIGNING_SECRET; defaults to a
// SHA-256 of SERVICE_TOKEN for dev environments where bundle integrity is
// less critical than ease of bootstrap. Production deployments must set
// POLICY_BUNDLE_SIGNING_SECRET explicitly.
func policyBundleSigningSecret() ([]byte, error) {
	if v := strings.TrimSpace(os.Getenv("POLICY_BUNDLE_SIGNING_SECRET")); v != "" {
		return []byte(v), nil
	}
	if v := strings.TrimSpace(os.Getenv("SERVICE_TOKEN")); v != "" {
		h := sha256.Sum256([]byte("policy-bundle-v1:" + v))
		return h[:], nil
	}
	return nil, errors.New("POLICY_BUNDLE_SIGNING_SECRET or SERVICE_TOKEN must be set")
}

// lookupPolicyForStream returns the stream's playback_policy JSON, the
// stream's internal name, and an error. The policy may be empty when the
// stream is public; consumers treat empty as "no auth required."
func (s *CommodoreServer) lookupPolicyForStream(ctx context.Context, tenantID, streamID string) ([]byte, string, error) {
	row, err := commodoredb.New(s.db).GetStreamPolicyForBundle(ctx, streamID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, "", status.Errorf(codes.Internal, "stream lookup: %v", err)
	}
	if row.TenantID != tenantID {
		return nil, "", status.Error(codes.PermissionDenied, "tenant_id mismatch for stream")
	}
	if row.PlaybackPolicy == "" {
		return nil, row.InternalName, nil
	}
	return []byte(row.PlaybackPolicy), row.InternalName, nil
}

// lookupTenantClusterEntitlement returns the cluster IDs this tenant is
// entitled to use and the coarse plan class, sourced from Quartermaster (the
// schema owner) via GetTenantEntitlement. Fails closed: a missing client or an
// RPC error prevents the bundle from being issued.
func (s *CommodoreServer) lookupTenantClusterEntitlement(ctx context.Context, tenantID string) ([]string, string, error) {
	if s.qmEntitlements == nil {
		return nil, "", status.Error(codes.Unavailable, "quartermaster not available for tenant entitlement")
	}
	resp, err := s.qmEntitlements.GetTenantEntitlement(ctx, tenantID)
	if err != nil {
		return nil, "", status.Errorf(codes.Internal, "tenant entitlement lookup: %v", err)
	}
	return resp.GetAllowedClusterIds(), resp.GetPlanClass(), nil
}

func (s *CommodoreServer) nextPolicyBundleVersion(ctx context.Context, tenantID, streamID string) (int64, error) {
	return nextPolicyBundleVersionWith(ctx, s.db, tenantID, streamID)
}

func nextPolicyBundleVersionWith(ctx context.Context, exec commodoredb.DBTX, tenantID, streamID string) (int64, error) {
	next, err := commodoredb.New(exec).NextPolicyBundleVersion(ctx, commodoredb.NextPolicyBundleVersionParams{
		TenantID: tenantID,
		StreamID: streamID,
	})
	if err != nil {
		return 0, fmt.Errorf("max(bundle_version): %w", err)
	}
	return next, nil
}

func persistPolicyBundleWith(ctx context.Context, exec commodoredb.DBTX, tenantID, streamID string, version int64, bundleJWT string, issuedAt, expiresAt time.Time) error {
	err := commodoredb.New(exec).InsertPolicyBundle(ctx, commodoredb.InsertPolicyBundleParams{
		TenantID:      tenantID,
		StreamID:      streamID,
		BundleVersion: version,
		BundleJwt:     bundleJWT,
		IssuedAt:      issuedAt,
		ExpiresAt:     expiresAt,
	})
	if err != nil {
		return fmt.Errorf("insert policy_bundle_versions: %w", err)
	}
	return nil
}
