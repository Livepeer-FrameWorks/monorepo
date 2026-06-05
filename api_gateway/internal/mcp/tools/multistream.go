package tools

import (
	"context"
	"fmt"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterMultistreamTools registers push-target management. Cost-affecting:
// each enabled push target multiplies the egress bill while the source
// stream is live.
func RegisterMultistreamTools(server *mcp.Server, serviceClients *clients.ServiceClients, _ *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_push_targets",
		Description: "List multistream push targets attached to a stream. target_uri values are masked in the response (rtmp://...****xxxx).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ListPushTargetsInput) (*mcp.CallToolResult, any, error) {
		return handleListPushTargets(ctx, args, serviceClients, logger)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_push_target",
		Description: "Add a multistream push target. Platform must be one of twitch, youtube, facebook, kick, x, or custom. When the source stream is live, FrameWorks pushes to every enabled target. Cost-affecting: each enabled target multiplies egress.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args CreatePushTargetInput) (*mcp.CallToolResult, any, error) {
		return handleCreatePushTarget(ctx, args, serviceClients, checker, logger)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "update_push_target",
		Description: "Update a push target's name / target URI / enabled flag. Disable instead of delete to keep history.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args UpdatePushTargetInput) (*mcp.CallToolResult, any, error) {
		return handleUpdatePushTarget(ctx, args, serviceClients, checker, logger)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_push_target",
		Description: "Delete a push target. Live pushes already in flight to this target are stopped.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args DeletePushTargetInput) (*mcp.CallToolResult, any, error) {
		return handleDeletePushTarget(ctx, args, serviceClients, logger)
	})
}

// ===== Inputs =====

type ListPushTargetsInput struct {
	StreamID string `json:"stream_id" jsonschema:"required"`
}

type CreatePushTargetInput struct {
	StreamID  string `json:"stream_id" jsonschema:"required"`
	Platform  string `json:"platform" jsonschema:"required" jsonschema_description:"'twitch' | 'youtube' | 'facebook' | 'kick' | 'x' | 'custom'"`
	Name      string `json:"name" jsonschema:"required"`
	TargetURI string `json:"target_uri" jsonschema:"required" jsonschema_description:"Full RTMP/RTMPS/SRT URI including the destination stream key."`
}

type UpdatePushTargetInput struct {
	ID        string  `json:"id" jsonschema:"required"`
	Name      *string `json:"name,omitempty"`
	TargetURI *string `json:"target_uri,omitempty"`
	IsEnabled *bool   `json:"is_enabled,omitempty"`
}

type DeletePushTargetInput struct {
	ID string `json:"id" jsonschema:"required"`
}

// ===== Result shapes =====

type PushTargetResult struct {
	ID           string `json:"id"`
	StreamID     string `json:"stream_id"`
	Platform     string `json:"platform"`
	Name         string `json:"name"`
	TargetURI    string `json:"target_uri"` // masked on read
	IsEnabled    bool   `json:"is_enabled"`
	Status       string `json:"status"`
	LastError    string `json:"last_error,omitempty"`
	LastPushedAt string `json:"last_pushed_at,omitempty"`
}

// ===== Handlers =====

func handleListPushTargets(ctx context.Context, args ListPushTargetsInput, c *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if args.StreamID == "" {
		return toolError("stream_id is required")
	}
	resp, err := c.Commodore.ListPushTargets(ctx, args.StreamID)
	if err != nil {
		logger.WithError(err).Warn("list_push_targets failed")
		return toolError(fmt.Sprintf("list push targets: %v", err))
	}
	out := make([]PushTargetResult, 0, len(resp.GetPushTargets()))
	for _, t := range resp.GetPushTargets() {
		out = append(out, pushTargetToResult(t))
	}
	return toolSuccess(out)
}

func handleCreatePushTarget(ctx context.Context, args CreatePushTargetInput, c *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if result, meta, err := requirePositiveBalance(ctx, checker); result != nil || meta != nil || err != nil {
		return result, meta, err
	}
	if args.StreamID == "" || args.TargetURI == "" || args.Name == "" || args.Platform == "" {
		return toolError("stream_id, platform, name, and target_uri are all required")
	}
	t, err := c.Commodore.CreatePushTarget(ctx, &commodorepb.CreatePushTargetRequest{
		StreamId:  args.StreamID,
		Platform:  strings.ToLower(args.Platform),
		Name:      args.Name,
		TargetUri: args.TargetURI,
	})
	if err != nil {
		logger.WithError(err).Warn("create_push_target failed")
		return toolError(fmt.Sprintf("create push target: %v", err))
	}
	return toolSuccess(pushTargetToResult(t))
}

func handleUpdatePushTarget(ctx context.Context, args UpdatePushTargetInput, c *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if result, meta, err := requirePositiveBalance(ctx, checker); result != nil || meta != nil || err != nil {
		return result, meta, err
	}
	if args.ID == "" {
		return toolError("id is required")
	}
	req := &commodorepb.UpdatePushTargetRequest{Id: args.ID}
	req.Name = args.Name
	req.TargetUri = args.TargetURI
	req.IsEnabled = args.IsEnabled
	t, err := c.Commodore.UpdatePushTarget(ctx, req)
	if err != nil {
		logger.WithError(err).Warn("update_push_target failed")
		return toolError(fmt.Sprintf("update push target: %v", err))
	}
	return toolSuccess(pushTargetToResult(t))
}

func handleDeletePushTarget(ctx context.Context, args DeletePushTargetInput, c *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if args.ID == "" {
		return toolError("id is required")
	}
	resp, err := c.Commodore.DeletePushTarget(ctx, args.ID)
	if err != nil {
		logger.WithError(err).Warn("delete_push_target failed")
		return toolError(fmt.Sprintf("delete push target: %v", err))
	}
	return toolSuccess(map[string]any{
		"id":      resp.GetId(),
		"message": resp.GetMessage(),
	})
}

func pushTargetToResult(t *commodorepb.PushTarget) PushTargetResult {
	if t == nil {
		return PushTargetResult{}
	}
	out := PushTargetResult{
		ID:        t.GetId(),
		StreamID:  t.GetStreamId(),
		Platform:  t.GetPlatform(),
		Name:      t.GetName(),
		TargetURI: t.GetTargetUri(),
		IsEnabled: t.GetIsEnabled(),
		Status:    t.GetStatus(),
		LastError: t.GetLastError(),
	}
	if ts := t.GetLastPushedAt(); ts != nil {
		out.LastPushedAt = ts.AsTime().Format("2006-01-02T15:04:05Z07:00")
	}
	return out
}
