package grpc

import (
	"context"
	"database/sql"
	"errors"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestPlaybackAccess is the Bridge → Commodore → Foghorn facade for the
// dry-run playback policy evaluator. Commodore validates that the caller's
// tenant owns the playback target before forwarding to the owning Foghorn
// — Foghorn trusts the (tenant_id, internal_name) pair this handler stamps
// on the outgoing request.
//
// Exactly one of playback_id / internal_name must be supplied. When the
// caller passes a public playback_id, Commodore resolves the canonical
// internal_name (the same value the live USER_NEW path keys on) and forwards
// only that to Foghorn. This keeps the webhook payload's streamName field
// faithful to what live viewer connects would see, regardless of which
// identifier the operator supplied.
//
// Auth logic itself lives in Foghorn's evaluator (see
// api_balancing/internal/triggers/playback_auth.go EvaluatePlaybackPolicyDetailed).
// Commodore does NOT reimplement JWT verification or webhook calling here.
func (s *CommodoreServer) TestPlaybackAccess(ctx context.Context, req *pb.TestPlaybackAccessRequest) (*pb.TestPlaybackAccessResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	playbackID := req.GetPlaybackId()
	internalName := req.GetInternalName()
	switch {
	case playbackID == "" && internalName == "":
		return nil, status.Error(codes.InvalidArgument, "exactly one of playback_id or internal_name is required")
	case playbackID != "" && internalName != "":
		return nil, status.Error(codes.InvalidArgument, "exactly one of playback_id or internal_name may be provided")
	}

	// Validate tenant ownership. Resolve the policy locally first so we can
	// confirm the caller's tenant matches the policy's tenant_id before
	// forwarding to Foghorn (which would otherwise execute against the
	// target without tenant scoping).
	var policyReq *pb.ResolvePlaybackPolicyRequest
	if playbackID != "" {
		policyReq = &pb.ResolvePlaybackPolicyRequest{PlaybackId: playbackID}
	} else {
		policyReq = &pb.ResolvePlaybackPolicyRequest{InternalName: internalName}
	}
	policy, perr := s.ResolvePlaybackPolicy(ctx, policyReq)
	if perr != nil {
		return nil, status.Errorf(codes.NotFound, "playback target not found: %v", perr)
	}
	if policy.GetTenantId() == "" {
		return nil, status.Error(codes.NotFound, "playback target has no resolvable tenant")
	}
	if policy.GetTenantId() != tenantID {
		return nil, status.Error(codes.PermissionDenied, "tenant mismatch")
	}

	// When the caller passed a public playback_id, resolve the canonical
	// internal_name so Foghorn's evaluator sees the same StreamName the
	// live USER_NEW path would. Without this, webhook payloads would carry
	// streamName="" — fine for JWT mode (verification doesn't read it) but
	// misleading for webhook test mode.
	if internalName == "" {
		canonical, cerr := s.lookupInternalNameByPlaybackID(ctx, playbackID, tenantID)
		if cerr != nil {
			return nil, cerr
		}
		internalName = canonical
	}

	foghornClient, _, err := s.resolveFoghornForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	// Stamp validated tenant + canonical internal_name on the outgoing
	// request. Drop playback_id so Foghorn only ever sees the resolved
	// internal_name (single source of truth downstream).
	req.TenantId = tenantID
	req.PlaybackId = ""
	req.InternalName = internalName

	resp, _, err := foghornClient.TestPlaybackAccess(ctx, req)
	if err != nil {
		s.logger.WithError(err).Error("Foghorn.TestPlaybackAccess failed")
		return nil, status.Errorf(codes.Internal, "test playback access failed: %v", err)
	}
	return resp, nil
}

// lookupInternalNameByPlaybackID resolves a public playback_id to the
// canonical MistServer internal_name across streams, vod_assets, clips, and
// dvr_recordings. Tenant ownership has already been validated by the caller
// via ResolvePlaybackPolicy; this lookup is purely a denormalization step
// to avoid mismatched StreamName values in the webhook payload.
//
// DVR artifacts have their own routing internal_name (the dvr+<hash> form
// MistServer uses for archive playback) — distinct from the source stream's
// internal_name. The live USER_NEW path keys on the artifact's name, so we
// return d.internal_name, not s.internal_name.
func (s *CommodoreServer) lookupInternalNameByPlaybackID(ctx context.Context, playbackID, tenantID string) (string, error) {
	var internalName string
	for _, q := range []string{
		`SELECT internal_name FROM commodore.streams       WHERE playback_id = $1 AND tenant_id::text = $2`,
		`SELECT internal_name FROM commodore.vod_assets    WHERE playback_id = $1 AND tenant_id::text = $2`,
		`SELECT internal_name FROM commodore.clips         WHERE playback_id = $1 AND tenant_id::text = $2`,
		`SELECT internal_name FROM commodore.dvr_recordings WHERE playback_id = $1 AND tenant_id::text = $2`,
	} {
		err := s.db.QueryRowContext(ctx, q, playbackID, tenantID).Scan(&internalName)
		if err == nil {
			return internalName, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			s.logger.WithError(err).Error("lookup internal_name by playback_id failed")
			return "", status.Errorf(codes.Internal, "internal name lookup failed: %v", err)
		}
	}
	return "", status.Error(codes.NotFound, "playback target not found")
}
