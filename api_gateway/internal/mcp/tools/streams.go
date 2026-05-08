package tools

import (
	"context"
	"fmt"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterStreamTools registers stream-related MCP tools.
func RegisterStreamTools(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	// create_stream - Create a new stream (requires balance)
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "create_stream",
			Description: "Create a new push or pull live stream. Push streams return a usable stream key; pull streams return redacted source configuration and playback ID.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args CreateStreamInput) (*mcp.CallToolResult, any, error) {
			return handleCreateStream(ctx, args, clients, checker, logger)
		},
	)

	// update_stream - Update stream settings
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "update_stream",
			Description: "Update stream settings, recording, or pull-source configuration.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args UpdateStreamInput) (*mcp.CallToolResult, any, error) {
			return handleUpdateStream(ctx, args, clients, checker, logger)
		},
	)

	// delete_stream - Delete a stream
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "delete_stream",
			Description: "Delete a stream. This action cannot be undone.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args DeleteStreamInput) (*mcp.CallToolResult, any, error) {
			return handleDeleteStream(ctx, args, clients, checker, logger)
		},
	)

	// refresh_stream_key - Generate a new stream key
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "refresh_stream_key",
			Description: "Generate a new stream key. The old key will stop working immediately.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args RefreshStreamKeyInput) (*mcp.CallToolResult, any, error) {
			return handleRefreshStreamKey(ctx, args, clients, logger)
		},
	)
}

// CreateStreamInput represents input for create_stream tool.
type CreateStreamInput struct {
	Name        string               `json:"name" jsonschema:"required" jsonschema_description:"Stream display name"`
	Description string               `json:"description,omitempty" jsonschema_description:"Stream description"`
	Record      bool                 `json:"record,omitempty" jsonschema_description:"Enable DVR recording"`
	Public      bool                 `json:"public,omitempty" jsonschema_description:"Make stream publicly discoverable"`
	IngestMode  string               `json:"ingest_mode,omitempty" jsonschema_description:"push or pull. Defaults to push."`
	PullSource  *PullSourceToolInput `json:"pull_source,omitempty" jsonschema_description:"Required when ingest_mode is pull"`
}

type PullSourceToolInput struct {
	SourceURI string `json:"source_uri" jsonschema:"required" jsonschema_description:"Upstream RTSP, SRT, RIST, HLS, DTSC, or TS source URI"`
	Enabled   *bool  `json:"enabled,omitempty" jsonschema_description:"Whether the media plane may pull from the source. Defaults to true."`
}

type PullSourceToolResult struct {
	SourceURIRedacted string `json:"source_uri_redacted"`
	Enabled           bool   `json:"enabled"`
	Class             string `json:"class"`
}

// CreateStreamResult represents the result of creating a stream.
type CreateStreamResult struct {
	ID         string                `json:"id"`
	StreamID   string                `json:"stream_id"`
	StreamKey  string                `json:"stream_key,omitempty"`
	PlaybackID string                `json:"playback_id"`
	Name       string                `json:"name"`
	IngestMode string                `json:"ingest_mode"`
	PullSource *PullSourceToolResult `json:"pull_source,omitempty"`
	Message    string                `json:"message"`
}

func handleCreateStream(ctx context.Context, args CreateStreamInput, clients *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
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
	if args.Name == "" {
		return toolError("Stream name is required")
	}

	// Call Commodore to create stream (tenantID is in context metadata)
	resp, err := clients.Commodore.CreateStream(ctx, &pb.CreateStreamRequest{
		Title:       args.Name,
		Description: args.Description,
		IsPublic:    args.Public,
		IsRecording: args.Record,
		IngestMode:  args.IngestMode,
		PullSource:  toProtoPullSource(args.PullSource),
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create stream")
		return toolError(fmt.Sprintf("Failed to create stream: %v", err))
	}

	message := fmt.Sprintf("Push stream '%s' created. Use stream key to start broadcasting.", resp.Title)
	streamKey := resp.StreamKey
	if resp.GetIngestMode() == "pull" {
		message = fmt.Sprintf("Pull stream '%s' created. FrameWorks will pull from the configured source when viewers request playback.", resp.Title)
		streamKey = ""
	}
	result := CreateStreamResult{
		ID:         globalid.Encode(globalid.TypeStream, resp.Id),
		StreamID:   resp.Id,
		StreamKey:  streamKey,
		PlaybackID: resp.PlaybackId,
		Name:       resp.Title,
		IngestMode: resp.IngestMode,
		PullSource: fromProtoPullSource(resp.PullSource),
		Message:    message,
	}

	return toolSuccess(result)
}

// UpdateStreamInput represents input for update_stream tool.
type UpdateStreamInput struct {
	StreamID    string               `json:"stream_id" jsonschema:"required" jsonschema_description:"Relay ID or stream_id to update"`
	Name        *string              `json:"name,omitempty" jsonschema_description:"New stream name"`
	Description *string              `json:"description,omitempty" jsonschema_description:"New description"`
	Record      *bool                `json:"record,omitempty" jsonschema_description:"Enable/disable recording"`
	IngestMode  *string              `json:"ingest_mode,omitempty" jsonschema_description:"Existing ingest mode. A different value is rejected."`
	PullSource  *PullSourceToolInput `json:"pull_source,omitempty" jsonschema_description:"Replacement pull-source configuration for pull streams"`
}

// UpdateStreamResult represents the result of updating a stream.
type UpdateStreamResult struct {
	ID         string                `json:"id"`
	StreamID   string                `json:"stream_id"`
	Name       string                `json:"name"`
	IngestMode string                `json:"ingest_mode"`
	PullSource *PullSourceToolResult `json:"pull_source,omitempty"`
	Message    string                `json:"message"`
}

func handleUpdateStream(ctx context.Context, args UpdateStreamInput, clients *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}

	// Pre-flight: require positive balance
	if err := checker.RequireBalance(ctx); err != nil {
		if pfe, ok := preflight.IsPreflightError(err); ok {
			return toolErrorWithResolution(pfe.Blocker)
		}
		return toolError(fmt.Sprintf("Failed to check balance: %v", err))
	}

	if args.StreamID == "" {
		return toolError("stream_id is required")
	}
	streamID, err := decodeStreamID(args.StreamID)
	if err != nil {
		return toolError(err.Error())
	}

	// Call Commodore to update stream
	stream, err := clients.Commodore.UpdateStream(ctx, &pb.UpdateStreamRequest{
		StreamId:    streamID,
		Name:        args.Name,
		Description: args.Description,
		Record:      args.Record,
		IngestMode:  args.IngestMode,
		PullSource:  toProtoPullSource(args.PullSource),
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to update stream")
		return toolError(fmt.Sprintf("Failed to update stream: %v", err))
	}

	result := UpdateStreamResult{
		ID:         globalid.Encode(globalid.TypeStream, stream.StreamId),
		StreamID:   stream.StreamId,
		Name:       stream.Title,
		IngestMode: stream.IngestMode,
		PullSource: fromProtoPullSource(stream.PullSource),
		Message:    fmt.Sprintf("Stream '%s' updated.", stream.Title),
	}

	return toolSuccess(result)
}

func toProtoPullSource(input *PullSourceToolInput) *pb.PullSourceInput {
	if input == nil {
		return nil
	}
	return &pb.PullSourceInput{
		SourceUri: input.SourceURI,
		Enabled:   input.Enabled,
	}
}

func fromProtoPullSource(input *pb.PullSourceView) *PullSourceToolResult {
	if input == nil {
		return nil
	}
	return &PullSourceToolResult{
		SourceURIRedacted: input.SourceUriRedacted,
		Enabled:           input.Enabled,
		Class:             input.Class,
	}
}

// DeleteStreamInput represents input for delete_stream tool.
type DeleteStreamInput struct {
	StreamID string `json:"stream_id" jsonschema:"required" jsonschema_description:"Relay ID or stream_id to delete"`
}

// DeleteStreamResult represents the result of deleting a stream.
type DeleteStreamResult struct {
	ID       string `json:"id"`
	StreamID string `json:"stream_id"`
	Deleted  bool   `json:"deleted"`
	Message  string `json:"message"`
}

func handleDeleteStream(ctx context.Context, args DeleteStreamInput, clients *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}

	// Pre-flight: require positive balance
	if err := checker.RequireBalance(ctx); err != nil {
		if pfe, ok := preflight.IsPreflightError(err); ok {
			return toolErrorWithResolution(pfe.Blocker)
		}
		return toolError(fmt.Sprintf("Failed to check balance: %v", err))
	}

	if args.StreamID == "" {
		return toolError("stream_id is required")
	}
	streamID, err := decodeStreamID(args.StreamID)
	if err != nil {
		return toolError(err.Error())
	}

	// Call Commodore to delete stream
	resp, err := clients.Commodore.DeleteStream(ctx, streamID)
	if err != nil {
		logger.WithError(err).Warn("Failed to delete stream")
		return toolError(fmt.Sprintf("Failed to delete stream: %v", err))
	}

	result := DeleteStreamResult{
		ID:       globalid.Encode(globalid.TypeStream, resp.StreamId),
		StreamID: resp.StreamId,
		Deleted:  true,
		Message:  resp.Message,
	}

	return toolSuccess(result)
}

// RefreshStreamKeyInput represents input for refresh_stream_key tool.
type RefreshStreamKeyInput struct {
	StreamID string `json:"stream_id" jsonschema:"required" jsonschema_description:"Relay ID or stream_id to refresh key for"`
}

// RefreshStreamKeyResult represents the result of refreshing a stream key.
type RefreshStreamKeyResult struct {
	ID           string `json:"id"`
	StreamID     string `json:"stream_id"`
	NewStreamKey string `json:"new_stream_key"`
	Message      string `json:"message"`
}

func handleRefreshStreamKey(ctx context.Context, args RefreshStreamKeyInput, clients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}

	if args.StreamID == "" {
		return toolError("stream_id is required")
	}
	streamID, err := decodeStreamID(args.StreamID)
	if err != nil {
		return toolError(err.Error())
	}

	// Call Commodore to refresh stream key
	resp, err := clients.Commodore.RefreshStreamKey(ctx, streamID)
	if err != nil {
		logger.WithError(err).Warn("Failed to refresh stream key")
		return toolError(fmt.Sprintf("Failed to refresh stream key: %v", err))
	}

	result := RefreshStreamKeyResult{
		ID:           globalid.Encode(globalid.TypeStream, resp.StreamId),
		StreamID:     resp.StreamId,
		NewStreamKey: resp.StreamKey,
		Message:      "Stream key refreshed. Update your broadcasting software with the new key.",
	}

	return toolSuccess(result)
}
