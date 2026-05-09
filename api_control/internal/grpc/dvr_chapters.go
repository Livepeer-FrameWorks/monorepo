package grpc

import (
	"context"
	"database/sql"
	"errors"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Commodore — DVR chapter pass-through (gateway → Commodore → Foghorn)
// Commodore is the customer-facing intermediary. It validates tenant
// ownership of the DVR artifact, then forwards the request to Foghorn,
// which owns the dvr_segments ledger and chapter materialization.
//
// UTC-only — civil-time chapters resolve at the edge and submit
// (start_ms, end_ms) directly. No timezone state in Commodore.

// RetrieveDVRChapter validates tenant ownership then forwards to the
// DVR's origin Foghorn (where the dvr_segments ledger and chapter rows live).
func (s *CommodoreServer) RetrieveDVRChapter(ctx context.Context, req *pb.RetrieveDVRChapterRequest) (*pb.RetrieveDVRChapterResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	originClusterID, dvrHash, err := s.assertDVRTenant(ctx, req.GetDvrArtifactId(), tenantID)
	if err != nil {
		return nil, err
	}
	foghornClient, err := s.resolveFoghornForArtifact(ctx, tenantID, originClusterID)
	if err != nil {
		return nil, err
	}
	req.TenantId = tenantID
	req.DvrArtifactId = dvrHash
	resp, _, err := foghornClient.RetrieveDVRChapter(ctx, req)
	if err != nil {
		s.logger.WithError(err).Error("Foghorn.RetrieveDVRChapter failed")
		return nil, status.Errorf(codes.Internal, "retrieve chapter failed: %v", err)
	}
	return resp, nil
}

// ListDVRChapters validates tenant ownership then forwards to the DVR's
// origin Foghorn.
func (s *CommodoreServer) ListDVRChapters(ctx context.Context, req *pb.ListDVRChaptersRequest) (*pb.ListDVRChaptersResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	originClusterID, dvrHash, err := s.assertDVRTenant(ctx, req.GetDvrArtifactId(), tenantID)
	if err != nil {
		return nil, err
	}
	foghornClient, err := s.resolveFoghornForArtifact(ctx, tenantID, originClusterID)
	if err != nil {
		return nil, err
	}
	req.TenantId = tenantID
	req.DvrArtifactId = dvrHash
	resp, _, err := foghornClient.ListDVRChapters(ctx, req)
	if err != nil {
		s.logger.WithError(err).Error("Foghorn.ListDVRChapters failed")
		return nil, status.Errorf(codes.Internal, "list chapters failed: %v", err)
	}
	return resp, nil
}

// SetDVRChapterPolicy validates tenant ownership then forwards to the DVR's
// origin Foghorn.
func (s *CommodoreServer) SetDVRChapterPolicy(ctx context.Context, req *pb.SetDVRChapterPolicyRequest) (*pb.SetDVRChapterPolicyResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	originClusterID, dvrHash, err := s.assertDVRTenant(ctx, req.GetDvrArtifactId(), tenantID)
	if err != nil {
		return nil, err
	}
	foghornClient, err := s.resolveFoghornForArtifact(ctx, tenantID, originClusterID)
	if err != nil {
		return nil, err
	}
	req.TenantId = tenantID
	req.DvrArtifactId = dvrHash
	resp, _, err := foghornClient.SetDVRChapterPolicy(ctx, req)
	if err != nil {
		s.logger.WithError(err).Error("Foghorn.SetDVRChapterPolicy failed")
		return nil, status.Errorf(codes.Internal, "set chapter policy failed: %v", err)
	}
	return resp, nil
}

// assertDVRTenant verifies the DVR artifact belongs to the given tenant and
// returns the (origin_cluster_id, dvr_hash) pair. The caller's identifier
// may be either the dvr_recordings UUID (DVRRequest.id) or the dvr_hash
// (DVRRequest.dvrHash); both resolve to the same row. The returned dvr_hash
// is what Foghorn keys ledger and chapter rows on, so the caller forwards
// THAT (not the user-supplied id) downstream.
func (s *CommodoreServer) assertDVRTenant(ctx context.Context, dvrIdentifier, tenantID string) (originClusterID, dvrHash string, err error) {
	if tenantID == "" {
		return "", "", status.Error(codes.PermissionDenied, "tenant_id required")
	}
	if dvrIdentifier == "" {
		return "", "", status.Error(codes.InvalidArgument, "dvr id is required")
	}
	var ownerTenantID string
	// Look up by dvr_hash OR id::text. dvr_hash is 32 hex chars and the UUID
	// is 36 chars with dashes; the OR handles both without needing the
	// caller to pre-disambiguate.
	if scanErr := s.db.QueryRowContext(ctx,
		`SELECT tenant_id::text, COALESCE(origin_cluster_id, ''), dvr_hash
		   FROM commodore.dvr_recordings
		  WHERE dvr_hash = $1 OR id::text = $1`,
		dvrIdentifier,
	).Scan(&ownerTenantID, &originClusterID, &dvrHash); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return "", "", status.Error(codes.NotFound, "DVR not found")
		}
		return "", "", status.Errorf(codes.Internal, "tenant lookup failed: %v", scanErr)
	}
	if ownerTenantID != tenantID {
		return "", "", status.Error(codes.PermissionDenied, "tenant mismatch")
	}
	if originClusterID == "" {
		return "", "", status.Error(codes.FailedPrecondition, "DVR origin cluster is missing")
	}
	return originClusterID, dvrHash, nil
}
