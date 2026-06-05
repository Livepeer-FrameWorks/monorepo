package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterRetentionTools registers media-retention MCP tools.
//
// Mutating tools are cost-affecting because retention drives storage
// billing. update_asset and reset_asset are destructive-adjacent:
// shortening a horizon schedules the artifact for deletion at the new
// horizon. Sensitivity flags surface to MCP consumers via
// docs/platform-features.yaml; tool descriptions repeat them at the
// point of use.
func RegisterRetentionTools(server *mcp.Server, clients *clients.ServiceClients, _ *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_retention_policy",
			Description: "Read the tenant per-class retention defaults plus the tier cap and the values the cascade resolves to today for a new artifact of each class.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return handleGetRetentionPolicy(ctx, clients, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "set_retention_policy",
			Description: "Set the tenant per-class retention default for VOD uploads, DVR recordings, or clips. target_type is required ('vod' | 'dvr' | 'clip'). days is in [0, tier_cap] where 0 = keep forever (Free clamps to the cap). Pass clear=true to NULL the column so the tenant inherits the system default (VOD: keep forever, DVR/clip: 30d). Cost-affecting: changes storage billing for future artifacts.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args SetRetentionPolicyInput) (*mcp.CallToolResult, any, error) {
			return handleSetRetentionPolicy(ctx, args, clients, checker, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "set_stream_retention_overrides",
			Description: "Set per-stream DVR / clip retention overrides for a single stream. Unset fields are left alone; pass 0 for keep forever (paid only — Free clamps to the cap), >0 for finite days. To clear an existing override and inherit the tenant default, set clear_dvr_retention_override / clear_clip_retention_override.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args SetStreamRetentionOverridesInput) (*mcp.CallToolResult, any, error) {
			return handleSetStreamRetentionOverrides(ctx, args, clients, checker, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "update_asset_retention",
			Description: "Apply a per-asset retention override on a finalized DVR / clip / VOD asset. Set target_type to 'dvr' | 'clip' | 'vod'. retention_days = 0 means keep forever (paid only); >0 sets the days; retention_until_iso is an alternative absolute deadline. Cost-affecting and destructive-adjacent: shortening retention schedules the asset for deletion at the new horizon.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args UpdateAssetRetentionInput) (*mcp.CallToolResult, any, error) {
			return handleUpdateAssetRetention(ctx, args, clients, checker, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "reset_asset_retention",
			Description: "Clear a per-asset retention override and recompute the horizon from the cascade (per-stream → tenant per-class default → system default). Set target_type to 'dvr' | 'clip' | 'vod'.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args ResetAssetRetentionInput) (*mcp.CallToolResult, any, error) {
			return handleResetAssetRetention(ctx, args, clients, checker, logger)
		},
	)
}

type SetRetentionPolicyInput struct {
	TargetType string `json:"target_type" jsonschema:"required" jsonschema_description:"Asset class: 'vod' | 'dvr' | 'clip'."`
	Days       *int32 `json:"days,omitempty" jsonschema_description:"Days to retain new artifacts of this class. 0 = keep forever (paid only). Required unless clear=true."`
	Clear      bool   `json:"clear,omitempty" jsonschema_description:"When true, clears the column so the tenant inherits the system default."`
}

type SetStreamRetentionOverridesInput struct {
	StreamID                   string `json:"stream_id" jsonschema:"required" jsonschema_description:"Stream UUID."`
	DvrRetentionDaysOverride   *int32 `json:"dvr_retention_days_override,omitempty" jsonschema_description:"DVR override days; 0 = keep forever, >0 = days."`
	ClipRetentionDaysOverride  *int32 `json:"clip_retention_days_override,omitempty" jsonschema_description:"Clip override days; 0 = keep forever, >0 = days."`
	ClearDvrRetentionOverride  bool   `json:"clear_dvr_retention_override,omitempty" jsonschema_description:"When true, clears the DVR override; inherits tenant default."`
	ClearClipRetentionOverride bool   `json:"clear_clip_retention_override,omitempty" jsonschema_description:"When true, clears the clip override; inherits tenant default."`
}

type UpdateAssetRetentionInput struct {
	TargetType     string `json:"target_type" jsonschema:"required" jsonschema_description:"Asset class: 'dvr' | 'clip' | 'vod'."`
	TargetID       string `json:"target_id" jsonschema:"required" jsonschema_description:"Asset identifier (UUID or hash for the target type)."`
	RetentionDays  *int32 `json:"retention_days,omitempty" jsonschema_description:"Days from now (mutually exclusive with retention_until_iso). 0 = keep forever."`
	RetentionUntil string `json:"retention_until_iso,omitempty" jsonschema_description:"Absolute ISO-8601 timestamp (mutually exclusive with retention_days)"`
}

type ResetAssetRetentionInput struct {
	TargetType string `json:"target_type" jsonschema:"required" jsonschema_description:"Asset class: 'dvr' | 'clip' | 'vod'."`
	TargetID   string `json:"target_id" jsonschema:"required" jsonschema_description:"Asset identifier (UUID or hash for the target type)."`
}

type RetentionPolicyResult struct {
	DefaultVodRetentionDays    *int32  `json:"default_vod_retention_days,omitempty"`
	DefaultDvrRetentionDays    *int32  `json:"default_dvr_retention_days,omitempty"`
	DefaultClipRetentionDays   *int32  `json:"default_clip_retention_days,omitempty"`
	EffectiveVodRetentionDays  int32   `json:"effective_vod_retention_days"`
	EffectiveDvrRetentionDays  int32   `json:"effective_dvr_retention_days"`
	EffectiveClipRetentionDays int32   `json:"effective_clip_retention_days"`
	MaxRecordingRetentionDays  int32   `json:"max_recording_retention_days"`
	UpdatedBy                  string  `json:"updated_by,omitempty"`
	UpdatedAt                  *string `json:"updated_at,omitempty"`
}

type StreamRetentionOverridesResult struct {
	StreamID                  string `json:"stream_id"`
	DvrRetentionDaysOverride  *int32 `json:"dvr_retention_days_override,omitempty"`
	ClipRetentionDaysOverride *int32 `json:"clip_retention_days_override,omitempty"`
}

type AssetRetentionResult struct {
	TargetID       string `json:"target_id"`
	RetentionDays  int32  `json:"retention_days"`
	RetentionUntil string `json:"retention_until_iso,omitempty"`
	Source         string `json:"source"` // tenant_default | per_asset_override | tier_entitlement
}

func handleGetRetentionPolicy(ctx context.Context, c *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	resp, err := c.Commodore.GetMediaRetentionPolicy(ctx, &commodorepb.GetMediaRetentionPolicyRequest{TenantId: tenantID})
	if err != nil {
		logger.WithError(err).Warn("get_retention_policy failed")
		return toolError(fmt.Sprintf("failed to read retention policy: %v", err))
	}
	out := RetentionPolicyResult{
		EffectiveVodRetentionDays:  resp.GetEffectiveVodRetentionDays(),
		EffectiveDvrRetentionDays:  resp.GetEffectiveDvrRetentionDays(),
		EffectiveClipRetentionDays: resp.GetEffectiveClipRetentionDays(),
		MaxRecordingRetentionDays:  resp.GetBounds().GetMaxRecordingRetentionDays(),
		UpdatedBy:                  resp.GetUpdatedBy(),
	}
	if v := resp.DefaultVodRetentionDays; v != nil {
		dv := *v
		out.DefaultVodRetentionDays = &dv
	}
	if v := resp.DefaultDvrRetentionDays; v != nil {
		dv := *v
		out.DefaultDvrRetentionDays = &dv
	}
	if v := resp.DefaultClipRetentionDays; v != nil {
		dv := *v
		out.DefaultClipRetentionDays = &dv
	}
	if resp.GetUpdatedAt() != nil {
		s := resp.GetUpdatedAt().AsTime().UTC().Format(time.RFC3339)
		out.UpdatedAt = &s
	}
	return toolSuccess(out)
}

func handleSetRetentionPolicy(ctx context.Context, args SetRetentionPolicyInput, c *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if result, meta, err := requirePositiveBalance(ctx, checker); result != nil || meta != nil || err != nil {
		return result, meta, err
	}
	target := targetTypeFromString(args.TargetType)
	if target == commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_UNSPECIFIED {
		return toolError("target_type must be one of: vod, dvr, clip")
	}
	days := int32(0)
	if !args.Clear {
		if args.Days == nil {
			return toolError("days is required unless clear=true")
		}
		days = *args.Days
		if days < 0 {
			return toolError("days must be >= 0 (0 = keep forever)")
		}
	}
	resp, err := c.Commodore.SetMediaRetentionPolicy(ctx, &commodorepb.SetMediaRetentionPolicyRequest{
		TenantId:   tenantID,
		TargetType: target,
		Days:       days,
		Clear:      args.Clear,
	})
	if err != nil {
		logger.WithError(err).Warn("set_retention_policy failed")
		return toolError(fmt.Sprintf("failed to set retention policy: %v", err))
	}
	policy := resp.GetPolicy()
	out := RetentionPolicyResult{
		EffectiveVodRetentionDays:  policy.GetEffectiveVodRetentionDays(),
		EffectiveDvrRetentionDays:  policy.GetEffectiveDvrRetentionDays(),
		EffectiveClipRetentionDays: policy.GetEffectiveClipRetentionDays(),
		MaxRecordingRetentionDays:  policy.GetBounds().GetMaxRecordingRetentionDays(),
		UpdatedBy:                  policy.GetUpdatedBy(),
	}
	if v := policy.DefaultVodRetentionDays; v != nil {
		dv := *v
		out.DefaultVodRetentionDays = &dv
	}
	if v := policy.DefaultDvrRetentionDays; v != nil {
		dv := *v
		out.DefaultDvrRetentionDays = &dv
	}
	if v := policy.DefaultClipRetentionDays; v != nil {
		dv := *v
		out.DefaultClipRetentionDays = &dv
	}
	if policy.GetUpdatedAt() != nil {
		s := policy.GetUpdatedAt().AsTime().UTC().Format(time.RFC3339)
		out.UpdatedAt = &s
	}
	return toolSuccess(out)
}

func handleSetStreamRetentionOverrides(ctx context.Context, args SetStreamRetentionOverridesInput, c *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if result, meta, err := requirePositiveBalance(ctx, checker); result != nil || meta != nil || err != nil {
		return result, meta, err
	}
	if args.StreamID == "" {
		return toolError("stream_id is required")
	}
	if args.DvrRetentionDaysOverride == nil && args.ClipRetentionDaysOverride == nil &&
		!args.ClearDvrRetentionOverride && !args.ClearClipRetentionOverride {
		return toolError("at least one override or clear flag is required")
	}
	req := &commodorepb.SetStreamRetentionOverridesRequest{
		TenantId:                   tenantID,
		StreamId:                   args.StreamID,
		DvrRetentionDaysOverride:   args.DvrRetentionDaysOverride,
		ClipRetentionDaysOverride:  args.ClipRetentionDaysOverride,
		ClearDvrRetentionOverride:  args.ClearDvrRetentionOverride,
		ClearClipRetentionOverride: args.ClearClipRetentionOverride,
	}
	resp, err := c.Commodore.SetStreamRetentionOverrides(ctx, req)
	if err != nil {
		logger.WithError(err).Warn("set_stream_retention_overrides failed")
		return toolError(fmt.Sprintf("failed to set stream retention overrides: %v", err))
	}
	out := StreamRetentionOverridesResult{StreamID: resp.GetStreamId()}
	if v := resp.DvrRetentionDaysOverride; v != nil {
		dv := *v
		out.DvrRetentionDaysOverride = &dv
	}
	if v := resp.ClipRetentionDaysOverride; v != nil {
		dv := *v
		out.ClipRetentionDaysOverride = &dv
	}
	return toolSuccess(out)
}

// targetTypeFromString maps the MCP-side string discriminator to the proto
// enum. Returns UNSPECIFIED on unknown so Commodore returns a clear
// InvalidArgument rather than the tool silently coercing.
func targetTypeFromString(s string) commodorepb.MediaRetentionTarget {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "dvr":
		return commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_DVR
	case "clip":
		return commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_CLIP
	case "vod":
		return commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_VOD
	}
	return commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_UNSPECIFIED
}

func handleUpdateAssetRetention(ctx context.Context, args UpdateAssetRetentionInput, c *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if result, meta, err := requirePositiveBalance(ctx, checker); result != nil || meta != nil || err != nil {
		return result, meta, err
	}
	if args.TargetID == "" {
		return toolError("target_id is required")
	}
	tt := targetTypeFromString(args.TargetType)
	if tt == commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_UNSPECIFIED {
		return toolError("target_type must be one of: dvr, clip, vod")
	}
	hasDays := args.RetentionDays != nil
	hasUntil := strings.TrimSpace(args.RetentionUntil) != ""
	if !hasDays && !hasUntil {
		return toolError("either retention_days or retention_until_iso is required")
	}
	if hasDays && hasUntil {
		return toolError("retention_days and retention_until_iso are mutually exclusive")
	}
	if hasDays && *args.RetentionDays < 0 {
		return toolError("retention_days must be >= 0 (0 = keep forever)")
	}

	req := &commodorepb.UpdateAssetRetentionRequest{
		TenantId:   tenantID,
		TargetType: tt,
		TargetId:   args.TargetID,
	}
	if hasUntil {
		t, err := time.Parse(time.RFC3339, args.RetentionUntil)
		if err != nil {
			return toolError(fmt.Sprintf("retention_until_iso must be RFC3339: %v", err))
		}
		req.RetentionUntil = timestamppb.New(t)
	} else {
		v := *args.RetentionDays
		req.RetentionDays = &v
	}

	resp, err := c.Commodore.UpdateAssetRetention(ctx, req)
	if err != nil {
		logger.WithError(err).Warn("update_asset_retention failed")
		return toolError(fmt.Sprintf("failed to update asset retention: %v", err))
	}
	return toolSuccess(toAssetRetentionResult(resp))
}

func handleResetAssetRetention(ctx context.Context, args ResetAssetRetentionInput, c *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if result, meta, err := requirePositiveBalance(ctx, checker); result != nil || meta != nil || err != nil {
		return result, meta, err
	}
	if args.TargetID == "" {
		return toolError("target_id is required")
	}
	tt := targetTypeFromString(args.TargetType)
	if tt == commodorepb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_UNSPECIFIED {
		return toolError("target_type must be one of: dvr, clip, vod")
	}
	resp, err := c.Commodore.ResetAssetRetention(ctx, &commodorepb.ResetAssetRetentionRequest{
		TenantId:   tenantID,
		TargetType: tt,
		TargetId:   args.TargetID,
	})
	if err != nil {
		logger.WithError(err).Warn("reset_asset_retention failed")
		return toolError(fmt.Sprintf("failed to reset asset retention: %v", err))
	}
	return toolSuccess(toAssetRetentionResult(resp))
}

func toAssetRetentionResult(resp *commodorepb.UpdateAssetRetentionResponse) AssetRetentionResult {
	out := AssetRetentionResult{
		TargetID:      resp.GetTargetId(),
		RetentionDays: resp.GetRetentionDays(),
		Source:        resp.GetSource(),
	}
	if ts := resp.GetRetentionUntil(); ts != nil {
		out.RetentionUntil = ts.AsTime().UTC().Format(time.RFC3339)
	}
	return out
}
