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
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterRetentionTools registers media-retention MCP tools.
//
// Mutating tools are cost-affecting because changing retention changes storage
// billing. update_asset and reset_asset are destructive-adjacent: shortening a
// horizon schedules the artifact for deletion at the new horizon.
//
// Sensitivity flags surface to MCP consumers via docs/platform-features.yaml.
// The tool descriptions repeat the warnings at the point of use.
func RegisterRetentionTools(server *mcp.Server, clients *clients.ServiceClients, _ *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_retention_policy",
			Description: "Read the tenant-default DVR retention policy plus tier entitlement bounds and the value the cascade resolves to today.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return handleGetRetentionPolicy(ctx, clients, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "set_retention_policy",
			Description: "Set the tenant-default retention period for finalized DVR recordings (days). Cost-affecting: changes storage billing for all future recordings. Must be 1 ≤ value ≤ tier bound.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args SetRetentionPolicyInput) (*mcp.CallToolResult, any, error) {
			return handleSetRetentionPolicy(ctx, args, clients, checker, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "update_asset_retention",
			Description: "Apply a per-asset retention override on a finalized DVR / clip / VOD asset. Set target_type to 'dvr' | 'clip' | 'vod'. Cost-affecting and destructive-adjacent: shortening retention schedules the asset for deletion at the new horizon. Active assets are rejected.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args UpdateAssetRetentionInput) (*mcp.CallToolResult, any, error) {
			return handleUpdateAssetRetention(ctx, args, clients, checker, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "reset_asset_retention",
			Description: "Clear a per-asset retention override on a DVR / clip / VOD and recompute the horizon from the tenant default (or tier entitlement when no tenant default is set). Set target_type to 'dvr' | 'clip' | 'vod'.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args ResetAssetRetentionInput) (*mcp.CallToolResult, any, error) {
			return handleResetAssetRetention(ctx, args, clients, checker, logger)
		},
	)
}

type SetRetentionPolicyInput struct {
	RecordingRetentionDays int32 `json:"recording_retention_days" jsonschema:"required" jsonschema_description:"Days to retain finalized DVR recordings (1 ≤ value ≤ tier bound)"`
}

type UpdateAssetRetentionInput struct {
	TargetType     string `json:"target_type" jsonschema:"required" jsonschema_description:"Asset class: 'dvr' | 'clip' | 'vod'."`
	TargetID       string `json:"target_id" jsonschema:"required" jsonschema_description:"Asset identifier (UUID or hash for the target type)."`
	RetentionDays  int32  `json:"retention_days,omitempty" jsonschema_description:"Days from now (mutually exclusive with retention_until_iso)"`
	RetentionUntil string `json:"retention_until_iso,omitempty" jsonschema_description:"Absolute ISO-8601 timestamp (mutually exclusive with retention_days)"`
}

type ResetAssetRetentionInput struct {
	TargetType string `json:"target_type" jsonschema:"required" jsonschema_description:"Asset class: 'dvr' | 'clip' | 'vod'."`
	TargetID   string `json:"target_id" jsonschema:"required" jsonschema_description:"Asset identifier (UUID or hash for the target type)."`
}

type RetentionPolicyResult struct {
	RecordingRetentionDays          *int32  `json:"recording_retention_days,omitempty"`
	EffectiveRecordingRetentionDays int32   `json:"effective_recording_retention_days"`
	MaxRecordingRetentionDays       int32   `json:"max_recording_retention_days"`
	UpdatedBy                       string  `json:"updated_by,omitempty"`
	UpdatedAt                       *string `json:"updated_at,omitempty"`
}

type AssetRetentionResult struct {
	TargetID       string `json:"target_id"`
	RetentionDays  int32  `json:"retention_days"`
	RetentionUntil string `json:"retention_until_iso"`
	Source         string `json:"source"` // tenant_default | per_asset_override | tier_entitlement
}

func handleGetRetentionPolicy(ctx context.Context, c *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	resp, err := c.Commodore.GetMediaRetentionPolicy(ctx, &pb.GetMediaRetentionPolicyRequest{TenantId: tenantID})
	if err != nil {
		logger.WithError(err).Warn("get_retention_policy failed")
		return toolError(fmt.Sprintf("failed to read retention policy: %v", err))
	}
	out := RetentionPolicyResult{
		EffectiveRecordingRetentionDays: resp.GetEffectiveRecordingRetentionDays(),
		MaxRecordingRetentionDays:       resp.GetBounds().GetMaxRecordingRetentionDays(),
		UpdatedBy:                       resp.GetUpdatedBy(),
	}
	if resp.GetRecordingRetentionDaysSet() {
		v := resp.GetRecordingRetentionDays()
		out.RecordingRetentionDays = &v
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
	if args.RecordingRetentionDays < 1 {
		return toolError("recording_retention_days must be >= 1")
	}
	resp, err := c.Commodore.SetMediaRetentionPolicy(ctx, &pb.SetMediaRetentionPolicyRequest{
		TenantId:               tenantID,
		RecordingRetentionDays: args.RecordingRetentionDays,
	})
	if err != nil {
		logger.WithError(err).Warn("set_retention_policy failed")
		return toolError(fmt.Sprintf("failed to set retention policy: %v", err))
	}
	policy := resp.GetPolicy()
	out := RetentionPolicyResult{
		EffectiveRecordingRetentionDays: policy.GetEffectiveRecordingRetentionDays(),
		MaxRecordingRetentionDays:       policy.GetBounds().GetMaxRecordingRetentionDays(),
		UpdatedBy:                       policy.GetUpdatedBy(),
	}
	if policy.GetRecordingRetentionDaysSet() {
		v := policy.GetRecordingRetentionDays()
		out.RecordingRetentionDays = &v
	}
	if policy.GetUpdatedAt() != nil {
		s := policy.GetUpdatedAt().AsTime().UTC().Format(time.RFC3339)
		out.UpdatedAt = &s
	}
	return toolSuccess(out)
}

// targetTypeFromString maps the MCP-side string discriminator to the proto
// enum. Returns UNSPECIFIED on unknown so Commodore returns a clear
// InvalidArgument rather than the tool silently coercing to DVR.
func targetTypeFromString(s string) pb.MediaRetentionTarget {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "dvr":
		return pb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_DVR
	case "clip":
		return pb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_CLIP
	case "vod":
		return pb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_VOD
	}
	return pb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_UNSPECIFIED
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
	if tt == pb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_UNSPECIFIED {
		return toolError("target_type must be one of: dvr, clip, vod")
	}
	if args.RetentionDays <= 0 && args.RetentionUntil == "" {
		return toolError("either retention_days or retention_until_iso is required")
	}
	if args.RetentionDays > 0 && args.RetentionUntil != "" {
		return toolError("retention_days and retention_until_iso are mutually exclusive")
	}

	req := &pb.UpdateAssetRetentionRequest{
		TenantId:   tenantID,
		TargetType: tt,
		TargetId:   args.TargetID,
	}
	if args.RetentionUntil != "" {
		t, err := time.Parse(time.RFC3339, args.RetentionUntil)
		if err != nil {
			return toolError(fmt.Sprintf("retention_until_iso must be RFC3339: %v", err))
		}
		req.RetentionUntil = timestamppb.New(t)
	} else {
		req.RetentionDays = args.RetentionDays
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
	if tt == pb.MediaRetentionTarget_MEDIA_RETENTION_TARGET_UNSPECIFIED {
		return toolError("target_type must be one of: dvr, clip, vod")
	}
	resp, err := c.Commodore.ResetAssetRetention(ctx, &pb.ResetAssetRetentionRequest{
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

func toAssetRetentionResult(resp *pb.UpdateAssetRetentionResponse) AssetRetentionResult {
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
