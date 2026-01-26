package tools

import (
	"context"
	"fmt"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterClipTools registers clip-related MCP tools.
func RegisterClipTools(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	// create_clip - Create a clip from a stream
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "create_clip",
			Description: "Create a clip from a live or recorded stream. Requires billing details and balance.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args CreateClipInput) (*mcp.CallToolResult, any, error) {
			return handleCreateClip(ctx, args, clients, checker, logger)
		},
	)

	// delete_clip - Delete a clip
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "delete_clip",
			Description: "Delete a clip. This action cannot be undone.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args DeleteClipInput) (*mcp.CallToolResult, any, error) {
			return handleDeleteClip(ctx, args, clients, logger)
		},
	)
}

// CreateClipInput represents input for create_clip tool.
type CreateClipInput struct {
	StreamID    string `json:"stream_id" jsonschema:"required" jsonschema_description:"Relay ID or stream_id to clip from"`
	Title       string `json:"title" jsonschema:"required" jsonschema_description:"Clip title"`
	Description string `json:"description,omitempty" jsonschema_description:"Clip description"`
	StartSec    *int64 `json:"start_sec,omitempty" jsonschema_description:"Start time in seconds (negative for relative to now)"`
	DurationSec *int64 `json:"duration_sec,omitempty" jsonschema_description:"Duration in seconds (omit to use API default)"`
}

// CreateClipResult represents the result of creating a clip.
type CreateClipResult struct {
	ClipHash string `json:"clip_hash"`
	Status   string `json:"status"`
	Message  string `json:"message"`
}

func handleCreateClip(ctx context.Context, args CreateClipInput, clients *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok || tenantID == "" {
		return nil, nil, fmt.Errorf("not authenticated")
	}

	// Pre-flight: require billing details
	if err := checker.RequireBillingDetails(ctx); err != nil {
		if pfe, ok := preflight.IsPreflightError(err); ok {
			return toolErrorWithResolution(pfe.Blocker)
		}
		return toolError(fmt.Sprintf("Failed to check billing details: %v", err))
	}

	// Pre-flight: require positive balance
	if err := checker.RequireBalance(ctx); err != nil {
		if pfe, ok := preflight.IsPreflightError(err); ok {
			return toolErrorWithResolution(pfe.Blocker)
		}
		return toolError(fmt.Sprintf("Failed to check balance: %v", err))
	}

	// Validate required fields
	if args.StreamID == "" {
		return toolError("stream_id is required")
	}
	if args.Title == "" {
		return toolError("title is required")
	}
	streamID, err := decodeStreamID(args.StreamID)
	if err != nil {
		return toolError(err.Error())
	}

	// Call Commodore to create clip - let API handle default duration
	resp, err := clients.Commodore.CreateClip(ctx, &pb.CreateClipRequest{
		TenantId:    tenantID,
		StreamId:    &streamID,
		Format:      "mp4",
		Title:       args.Title,
		Description: args.Description,
		StartUnix:   args.StartSec,
		DurationSec: args.DurationSec,
		Mode:        pb.ClipMode_CLIP_MODE_RELATIVE,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create clip")
		return toolError(fmt.Sprintf("Failed to create clip: %v", err))
	}

	result := CreateClipResult{
		ClipHash: resp.ClipHash,
		Status:   resp.Status,
		Message:  fmt.Sprintf("Clip creation started. Hash: %s. Status: %s", resp.ClipHash, resp.Status),
	}

	return toolSuccess(result)
}

// DeleteClipInput represents input for delete_clip tool.
type DeleteClipInput struct {
	ClipHash string `json:"clip_hash" jsonschema:"required" jsonschema_description:"Clip hash to delete"`
}

// DeleteClipResult represents the result of deleting a clip.
type DeleteClipResult struct {
	ClipHash string `json:"clip_hash"`
	Deleted  bool   `json:"deleted"`
	Message  string `json:"message"`
}

func handleDeleteClip(ctx context.Context, args DeleteClipInput, clients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok || tenantID == "" {
		return nil, nil, fmt.Errorf("not authenticated")
	}

	if args.ClipHash == "" {
		return toolError("clip_hash is required")
	}

	// Call Commodore to delete clip (returns error only)
	err := clients.Commodore.DeleteClip(ctx, args.ClipHash)
	if err != nil {
		logger.WithError(err).Warn("Failed to delete clip")
		return toolError(fmt.Sprintf("Failed to delete clip: %v", err))
	}

	result := DeleteClipResult{
		ClipHash: args.ClipHash,
		Deleted:  true,
		Message:  "Clip deleted successfully.",
	}

	return toolSuccess(result)
}
