package tools

import (
	"context"
	"fmt"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

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
		return nil, nil, fmt.Errorf("not authenticated")
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
		return nil, nil, fmt.Errorf("not authenticated")
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
