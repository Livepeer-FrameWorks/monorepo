package tools

import (
	"context"
	"fmt"
	"strings"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterDVRTools registers DVR-related MCP tools.
func RegisterDVRTools(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	// start_dvr - Start DVR recording for a stream
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "start_dvr",
			Description: "Start DVR (catch-up/time-shift) recording for a stream. Requires positive balance.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args StartDVRInput) (*mcp.CallToolResult, any, error) {
			return handleStartDVR(ctx, args, clients, checker, logger)
		},
	)

	// stop_dvr - Stop DVR recording
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "stop_dvr",
			Description: "Stop an active DVR recording session.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args StopDVRInput) (*mcp.CallToolResult, any, error) {
			return handleStopDVR(ctx, args, clients, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "delete_dvr",
			Description: "Delete a DVR recording and its stored media. Requires confirm=\"DELETE DVR\".",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args DeleteDVRInput) (*mcp.CallToolResult, any, error) {
			return handleDeleteDVR(ctx, args, resolver, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "set_dvr_chapter_policy",
			Description: "Set how a DVR archive materializes replay chapters. Modes: WINDOW_SIZED, FIXED_INTERVAL, EXPLICIT_RANGE, NONE.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args SetDVRChapterPolicyInput) (*mcp.CallToolResult, any, error) {
			return handleSetDVRChapterPolicy(ctx, args, resolver, logger)
		},
	)
}

// StartDVRInput represents input for start_dvr tool.
type StartDVRInput struct {
	StreamID string `json:"stream_id" jsonschema:"required" jsonschema_description:"Relay ID or stream_id to start DVR for"`
}

// StartDVRResult represents the result of starting DVR.
type StartDVRResult struct {
	DVRHash string `json:"dvr_hash"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

func handleStartDVR(ctx context.Context, args StartDVRInput, clients *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
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
	streamID, err := decodeStreamID(args.StreamID)
	if err != nil {
		return toolError(err.Error())
	}

	stream, err := clients.Commodore.GetStream(ctx, streamID)
	if err != nil {
		logger.WithError(err).Warn("Failed to resolve stream for DVR")
		return toolError(fmt.Sprintf("Failed to resolve stream: %v", err))
	}

	// Call Commodore to start DVR
	resp, err := clients.Commodore.StartDVR(ctx, &pb.StartDVRRequest{
		TenantId:     tenantID,
		InternalName: stream.InternalName,
		StreamId:     &streamID,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to start DVR")
		return toolError(fmt.Sprintf("Failed to start DVR: %v", err))
	}

	result := StartDVRResult{
		DVRHash: resp.DvrHash,
		Status:  resp.Status,
		Message: fmt.Sprintf("DVR recording started. Hash: %s", resp.DvrHash),
	}

	return toolSuccess(result)
}

// StopDVRInput represents input for stop_dvr tool.
type StopDVRInput struct {
	DVRHash  string `json:"dvr_hash" jsonschema:"required" jsonschema_description:"DVR hash to stop"`
	StreamID string `json:"stream_id,omitempty" jsonschema_description:"Relay ID or stream_id (optional)"`
}

// StopDVRResult represents the result of stopping DVR.
type StopDVRResult struct {
	DVRHash string `json:"dvr_hash"`
	Stopped bool   `json:"stopped"`
	Message string `json:"message"`
}

func handleStopDVR(ctx context.Context, args StopDVRInput, clients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
	}

	if args.DVRHash == "" {
		return toolError("dvr_hash is required")
	}

	// Call Commodore to stop DVR (returns error only)
	err := clients.Commodore.StopDVR(ctx, args.DVRHash)
	if err != nil {
		logger.WithError(err).Warn("Failed to stop DVR")
		return toolError(fmt.Sprintf("Failed to stop DVR: %v", err))
	}

	result := StopDVRResult{
		DVRHash: args.DVRHash,
		Stopped: true,
		Message: "DVR recording stopped.",
	}

	return toolSuccess(result)
}

type DeleteDVRInput struct {
	DVRHash string `json:"dvr_hash" jsonschema:"required" jsonschema_description:"DVR hash to delete"`
	Confirm string `json:"confirm" jsonschema:"required" jsonschema_description:"Must be exactly 'DELETE DVR'."`
}

func handleDeleteDVR(ctx context.Context, args DeleteDVRInput, resolver *resolvers.Resolver, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if result, meta, err := requireConfirmation(args.Confirm, "DELETE DVR"); result != nil || meta != nil || err != nil {
		return result, meta, err
	}
	if args.DVRHash == "" {
		return toolError("dvr_hash is required")
	}
	resp, err := resolver.DoDeleteDVR(ctx, args.DVRHash)
	if err != nil {
		logger.WithError(err).Warn("Failed to delete DVR")
		return toolError(fmt.Sprintf("Failed to delete DVR: %v", err))
	}
	return toolSuccess(resp)
}

type SetDVRChapterPolicyInput struct {
	DVRID           string `json:"dvr_id" jsonschema:"required" jsonschema_description:"DVR recording ID or hash"`
	Mode            string `json:"mode" jsonschema:"required" jsonschema_description:"WINDOW_SIZED, FIXED_INTERVAL, EXPLICIT_RANGE, or NONE"`
	IntervalSeconds *int   `json:"interval_seconds,omitempty" jsonschema_description:"Required for FIXED_INTERVAL; ignored by other modes."`
}

func handleSetDVRChapterPolicy(ctx context.Context, args SetDVRChapterPolicyInput, resolver *resolvers.Resolver, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if strings.TrimSpace(args.DVRID) == "" {
		return toolError("dvr_id is required")
	}
	mode, err := parseDVRChapterMode(args.Mode)
	if err != nil {
		return toolError(err.Error())
	}
	var intervalSeconds int32
	if args.IntervalSeconds != nil {
		intervalSeconds = int32(*args.IntervalSeconds)
	}
	dvrHash, err := resolvers.NormalizeDvrID(args.DVRID)
	if err != nil {
		return toolError(err.Error())
	}
	resp, err := resolver.DoSetDVRChapterPolicy(ctx, dvrHash, mode, intervalSeconds)
	if err != nil {
		logger.WithError(err).Warn("Failed to set DVR chapter policy")
		return toolError(fmt.Sprintf("Failed to set DVR chapter policy: %v", err))
	}
	return toolSuccess(resp)
}

func parseDVRChapterMode(value string) (model.DVRChapterMode, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "WINDOW_SIZED":
		return model.DVRChapterModeWindowSized, nil
	case "FIXED_INTERVAL":
		return model.DVRChapterModeFixedInterval, nil
	case "EXPLICIT_RANGE":
		return model.DVRChapterModeExplicitRange, nil
	case "NONE":
		return model.DVRChapterModeNone, nil
	default:
		return "", fmt.Errorf("invalid mode %q; expected WINDOW_SIZED, FIXED_INTERVAL, EXPLICIT_RANGE, or NONE", value)
	}
}
