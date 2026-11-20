package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"frameworks/api_control/internal/database/commodoredb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// tenantProcessTier is the billing tier whose processes and transcode ladders
// apply to a tenant; nil when the tenant has no subscription. Authority compiles
// claimed together share the answers.
func (s *CommodoreServer) tenantProcessTier(ctx context.Context, tenantID string) (*purserpb.BillingTier, error) {
	subResp, err := memoizedCompileInput(ctx, "subscription\x00"+tenantID, func() (*purserpb.GetSubscriptionResponse, error) {
		return s.purserClient.GetSubscription(ctx, tenantID)
	})
	if err != nil {
		return nil, fmt.Errorf("load subscription: %w", err)
	}
	sub := subResp.GetSubscription()
	if sub == nil {
		return nil, nil
	}
	tier, err := memoizedCompileInput(ctx, "billing_tier\x00"+sub.GetTierId(), func() (*purserpb.BillingTier, error) {
		return s.purserClient.GetBillingTier(ctx, sub.GetTierId())
	})
	if err != nil {
		return nil, fmt.Errorf("load billing tier: %w", err)
	}
	return tier, nil
}

// allowedTranscodeLadder returns the tier's allowed ladder of that name.
func allowedTranscodeLadder(tier *purserpb.BillingTier, name string) (*purserpb.TranscodeLadder, bool) {
	for _, ladder := range tier.GetTranscodeLadders() {
		if ladder.GetName() == name {
			return ladder, true
		}
	}
	return nil, false
}

func allowedTranscodeLadderNames(tier *purserpb.BillingTier) []string {
	names := make([]string, 0, len(tier.GetTranscodeLadders()))
	for _, ladder := range tier.GetTranscodeLadders() {
		names = append(names, ladder.GetName())
	}
	return names
}

// checkTranscodeLadderChoice normalizes a ladder a tenant or stream picks and
// refuses one the tenant's tier does not allow. Empty inherits and is always
// accepted.
func (s *CommodoreServer) checkTranscodeLadderChoice(ctx context.Context, tenantID, name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "", nil
	}
	if s.purserClient == nil {
		return "", status.Error(codes.FailedPrecondition, "transcode ladders are unavailable: billing is not configured")
	}
	tier, err := s.tenantProcessTier(ctx, tenantID)
	if err != nil {
		return "", status.Errorf(codes.Unavailable, "could not check transcode ladder %q against your plan: %v", name, err)
	}
	if _, ok := allowedTranscodeLadder(tier, name); ok {
		return name, nil
	}
	allowed := allowedTranscodeLadderNames(tier)
	if len(allowed) == 0 {
		return "", status.Errorf(codes.InvalidArgument, "transcode ladder %q is not available: your plan offers no transcode ladders", name)
	}
	return "", status.Errorf(codes.InvalidArgument, "transcode ladder %q is not available on your plan; choose one of: %s", name, strings.Join(allowed, ", "))
}

// tenantTranscodeLadder is the tenant's chosen ladder, "" when it inherits.
func (s *CommodoreServer) tenantTranscodeLadder(ctx context.Context, tenantID string) (string, error) {
	ladder, err := memoizedCompileInput(ctx, "tenant_transcode_ladder\x00"+tenantID, func() (sql.NullString, error) {
		value, queryErr := commodoredb.New(s.db).GetTenantTranscodeLadder(ctx, tenantID)
		if errors.Is(queryErr, sql.ErrNoRows) {
			return sql.NullString{}, nil
		}
		return value, queryErr
	})
	if err != nil {
		return "", fmt.Errorf("load tenant transcode ladder: %w", err)
	}
	return ladder.String, nil
}

// resolveTranscodeLadder picks the ladder for a stream or VOD: the stream's
// choice, then the tenant's, then the tier default. A choice the tier no longer
// allows (a downgrade since it was made) is skipped and logged, never served.
func (s *CommodoreServer) resolveTranscodeLadder(tier *purserpb.BillingTier, tenantID, streamID, streamLadder, tenantLadder string) (*purserpb.TranscodeLadder, error) {
	for _, choice := range []struct{ source, name string }{{"stream", streamLadder}, {"tenant", tenantLadder}} {
		if choice.name == "" {
			continue
		}
		if ladder, ok := allowedTranscodeLadder(tier, choice.name); ok {
			return ladder, nil
		}
		s.logger.WithFields(map[string]any{
			"tenant_id": tenantID, "stream_id": streamID, "tier": tier.GetTierName(),
			"transcode_ladder": choice.name, "choice": choice.source,
		}).Warn("Transcode ladder is not allowed on the tenant's tier; falling back")
	}
	ladder, ok := allowedTranscodeLadder(tier, tier.GetDefaultTranscodeLadder())
	if !ok {
		return nil, fmt.Errorf("tier %s default transcode ladder %q is not one it allows", tier.GetTierName(), tier.GetDefaultTranscodeLadder())
	}
	return ladder, nil
}

// applyTranscodeLadder gives the Livepeer entries of a tier process config the
// ladder's renditions.
func applyTranscodeLadder(processesJSON string, ladder *purserpb.TranscodeLadder) (string, error) {
	renditions, err := mist.ParseTranscodeRenditions([]byte(ladder.GetRenditionsJson()))
	if err != nil {
		return "", fmt.Errorf("transcode ladder %s: %w", ladder.GetName(), err)
	}
	return mist.SetTranscodeLadder(processesJSON, renditions)
}

// GetTranscodeLadderPolicy reports the ladders the tenant's tier allows and the
// ladder its streams inherit.
func (s *CommodoreServer) GetTranscodeLadderPolicy(ctx context.Context, req *commodorepb.GetTranscodeLadderPolicyRequest) (*commodorepb.TranscodeLadderPolicy, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	if t := req.GetTenantId(); t != "" && t != tenantID {
		return nil, status.Error(codes.PermissionDenied, "tenant mismatch")
	}
	return s.transcodeLadderPolicy(ctx, tenantID)
}

// SetTenantTranscodeLadder sets the ladder the tenant's streams and VODs
// inherit; empty returns them to the tier default.
func (s *CommodoreServer) SetTenantTranscodeLadder(ctx context.Context, req *commodorepb.SetTenantTranscodeLadderRequest) (*commodorepb.TranscodeLadderPolicy, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	if t := req.GetTenantId(); t != "" && t != tenantID {
		return nil, status.Error(codes.PermissionDenied, "tenant mismatch")
	}
	if permissionErr := requireTenantStreamPermission(ctx, "streams:write"); permissionErr != nil {
		return nil, permissionErr
	}
	name, err := s.checkTranscodeLadderChoice(ctx, tenantID, req.GetTranscodeLadder())
	if err != nil {
		return nil, err
	}
	if writeErr := commodoredb.New(s.db).SetTenantTranscodeLadder(ctx, commodoredb.SetTenantTranscodeLadderParams{
		TenantID: tenantID, TranscodeLadder: sql.NullString{String: name, Valid: name != ""},
	}); writeErr != nil {
		return nil, status.Errorf(codes.Internal, "save transcode ladder: %v", writeErr)
	}
	return s.transcodeLadderPolicy(ctx, tenantID)
}

func (s *CommodoreServer) transcodeLadderPolicy(ctx context.Context, tenantID string) (*commodorepb.TranscodeLadderPolicy, error) {
	policy := &commodorepb.TranscodeLadderPolicy{}
	tenantLadder, err := s.tenantTranscodeLadder(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	policy.TenantLadder = tenantLadder
	if s.purserClient == nil {
		return policy, nil
	}
	tier, err := s.tenantProcessTier(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "load plan transcode ladders: %v", err)
	}
	policy.TierDefault = tier.GetDefaultTranscodeLadder()
	for _, ladder := range tier.GetTranscodeLadders() {
		option := &commodorepb.TranscodeLadderOption{Name: ladder.GetName(), DisplayName: ladder.GetDisplayName(), Description: ladder.GetDescription()}
		if renditions, parseErr := mist.ParseTranscodeRenditions([]byte(ladder.GetRenditionsJson())); parseErr == nil {
			for _, r := range renditions {
				option.RenditionNames = append(option.RenditionNames, r.Name)
			}
		}
		policy.Ladders = append(policy.Ladders, option)
	}
	if len(policy.GetLadders()) > 0 {
		effective, resolveErr := s.resolveTranscodeLadder(tier, tenantID, "", "", tenantLadder)
		if resolveErr != nil {
			return nil, status.Errorf(codes.Internal, "%v", resolveErr)
		}
		policy.EffectiveLadder = effective.GetName()
	}
	return policy, nil
}

func (s *CommodoreServer) resolveVODProcessesJSON(ctx context.Context, tenantID, clusterID, hash string) (string, error) {
	choice, err := commodoredb.New(s.db).GetVODTranscodeLadder(ctx, commodoredb.GetVODTranscodeLadderParams{TenantID: tenantID, VodHash: hash})
	if err != nil {
		return "", fmt.Errorf("load upload transcode ladder: %w", err)
	}
	return s.resolveProcessesJSONWithLadder(ctx, tenantID, "", clusterID, "vod", choice)
}
