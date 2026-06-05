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

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

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
// bundle_version in internal_names. Foghorn's cache watermark bumps to that
// value on receipt, invalidating prior bundles.
func (s *CommodoreServer) GetSignedPolicyBundle(ctx context.Context, req *commodorepb.GetSignedPolicyBundleRequest) (*commodorepb.GetSignedPolicyBundleResponse, error) {
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

	bundleVersion, err := s.nextPolicyBundleVersion(ctx, tenantID, streamID)
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

	if err := s.persistPolicyBundle(ctx, tenantID, streamID, bundleVersion, bundleJWT, issuedAt, expiresAt); err != nil {
		s.logger.WithError(err).WithFields(map[string]any{
			"tenant_id":      tenantID,
			"stream_id":      streamID,
			"bundle_version": bundleVersion,
		}).Error("persist policy bundle failed")
		return nil, status.Errorf(codes.Internal, "persist bundle: %v", err)
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
	var (
		policy       sql.NullString
		internalName string
		rowTenantID  string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(playback_policy::text, ''), internal_name, tenant_id::text
		FROM commodore.streams
		WHERE id = $1::uuid
	`, streamID).Scan(&policy, &internalName, &rowTenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, "", status.Errorf(codes.Internal, "stream lookup: %v", err)
	}
	if rowTenantID != tenantID {
		return nil, "", status.Error(codes.PermissionDenied, "tenant_id mismatch for stream")
	}
	if !policy.Valid || policy.String == "" {
		return nil, internalName, nil
	}
	return []byte(policy.String), internalName, nil
}

// lookupTenantClusterEntitlement returns the cluster IDs this tenant is
// entitled to use and the coarse plan class. Plan class is sourced from the
// tenant row's primary_cluster_id's cluster_class for the v1 bundle; a
// follow-up wires Purser plan tier directly.
func (s *CommodoreServer) lookupTenantClusterEntitlement(ctx context.Context, tenantID string) ([]string, string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT cluster_id
		FROM quartermaster.tenant_cluster_access
		WHERE tenant_id = $1::uuid
		  AND is_active = TRUE
		  AND subscription_status = 'active'
		ORDER BY cluster_id
	`, tenantID)
	if err != nil {
		return nil, "", status.Errorf(codes.Internal, "tenant cluster access lookup: %v", err)
	}
	defer rows.Close()
	var allowed []string
	for rows.Next() {
		var c string
		if scanErr := rows.Scan(&c); scanErr != nil {
			return nil, "", status.Errorf(codes.Internal, "tenant cluster access scan: %v", scanErr)
		}
		allowed = append(allowed, c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", status.Errorf(codes.Internal, "tenant cluster access rows: %v", err)
	}

	var planClass sql.NullString
	if scanErr := s.db.QueryRowContext(ctx, `
		SELECT c.cluster_class
		FROM quartermaster.tenants t
		LEFT JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = t.primary_cluster_id
		WHERE t.id = $1::uuid
	`, tenantID).Scan(&planClass); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		s.logger.WithError(scanErr).WithField("tenant_id", tenantID).
			Warn("plan class lookup failed; bundle issued without plan_class")
	}
	return allowed, planClass.String, nil
}

func (s *CommodoreServer) nextPolicyBundleVersion(ctx context.Context, tenantID, streamID string) (int64, error) {
	var next int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(bundle_version), 0) + 1
		FROM commodore.policy_bundle_versions
		WHERE tenant_id = $1::uuid AND stream_id = $2::uuid
	`, tenantID, streamID).Scan(&next)
	if err != nil {
		return 0, fmt.Errorf("max(bundle_version): %w", err)
	}
	return next, nil
}

func (s *CommodoreServer) persistPolicyBundle(ctx context.Context, tenantID, streamID string, version int64, bundleJWT string, issuedAt, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO commodore.policy_bundle_versions
			(tenant_id, stream_id, bundle_version, bundle_jwt, issued_at, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6)
	`, tenantID, streamID, version, bundleJWT, issuedAt, expiresAt)
	if err != nil {
		return fmt.Errorf("insert policy_bundle_versions: %w", err)
	}
	return nil
}
