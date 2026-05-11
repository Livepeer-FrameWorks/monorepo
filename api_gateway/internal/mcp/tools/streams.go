package tools

import (
	"context"
	"fmt"
	"time"

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
			Description: "Rotate the primary stream key. The old key stops working immediately. Requires confirm=\"ROTATE STREAM KEY\".",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args RefreshStreamKeyInput) (*mcp.CallToolResult, any, error) {
			return handleRefreshStreamKey(ctx, args, clients, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "list_stream_keys",
			Description: "List stream keys for a stream, including active state and last-used timestamps.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args ListStreamKeysInput) (*mcp.CallToolResult, any, error) {
			return handleListStreamKeys(ctx, args, clients, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "create_stream_key",
			Description: "Create an additional ingest key for a stream. The key value is returned in the response. Requires confirm=\"CREATE STREAM KEY\".",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args CreateStreamKeyInput) (*mcp.CallToolResult, any, error) {
			return handleCreateStreamKey(ctx, args, clients, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "delete_stream_key",
			Description: "Deactivate a stream key. Active encoders using it will fail ingest. Requires confirm=\"DELETE STREAM KEY\".",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args DeleteStreamKeyInput) (*mcp.CallToolResult, any, error) {
			return handleDeleteStreamKey(ctx, args, clients, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "validate_stream_key",
			Description: "Validate an ingest stream key and return whether it can authenticate an ingest session.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args ValidateStreamKeyInput) (*mcp.CallToolResult, any, error) {
			return handleValidateStreamKey(ctx, args, clients, logger)
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
	Confirm  string `json:"confirm" jsonschema:"required" jsonschema_description:"Must be exactly 'ROTATE STREAM KEY'."`
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

	if result, meta, err := requireConfirmation(args.Confirm, "ROTATE STREAM KEY"); result != nil || meta != nil || err != nil {
		return result, meta, err
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

type ListStreamKeysInput struct {
	StreamID string `json:"stream_id" jsonschema:"required" jsonschema_description:"Relay ID or stream_id to list keys for"`
}

type CreateStreamKeyInput struct {
	StreamID string `json:"stream_id" jsonschema:"required" jsonschema_description:"Relay ID or stream_id to create a key for"`
	Name     string `json:"name" jsonschema:"required" jsonschema_description:"Human-readable key name"`
	Confirm  string `json:"confirm" jsonschema:"required" jsonschema_description:"Must be exactly 'CREATE STREAM KEY'."`
}

type DeleteStreamKeyInput struct {
	StreamID string `json:"stream_id" jsonschema:"required" jsonschema_description:"Relay ID or stream_id that owns the key"`
	KeyID    string `json:"key_id" jsonschema:"required" jsonschema_description:"Stream key UUID"`
	Confirm  string `json:"confirm" jsonschema:"required" jsonschema_description:"Must be exactly 'DELETE STREAM KEY'."`
}

type ValidateStreamKeyInput struct {
	StreamKey string `json:"stream_key" jsonschema:"required" jsonschema_description:"Raw ingest stream key to validate"`
}

type StreamKeyToolResult struct {
	ID         string `json:"id"`
	StreamID   string `json:"stream_id"`
	KeyValue   string `json:"key_value,omitempty"`
	KeyName    string `json:"key_name,omitempty"`
	IsActive   bool   `json:"is_active"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
}

type ListStreamKeysResult struct {
	Keys []StreamKeyToolResult `json:"keys"`
}

type CreateStreamKeyResult struct {
	Key     StreamKeyToolResult `json:"key"`
	Warning string              `json:"warning"`
}

type ValidateStreamKeyResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func handleListStreamKeys(ctx context.Context, args ListStreamKeysInput, clients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
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
	resp, err := clients.Commodore.ListStreamKeys(ctx, streamID, nil)
	if err != nil {
		logger.WithError(err).Warn("Failed to list stream keys")
		return toolError(fmt.Sprintf("Failed to list stream keys: %v", err))
	}
	out := ListStreamKeysResult{Keys: make([]StreamKeyToolResult, 0, len(resp.GetStreamKeys()))}
	for _, k := range resp.GetStreamKeys() {
		out.Keys = append(out.Keys, streamKeyToToolResult(k, false))
	}
	return toolSuccess(out)
}

func handleCreateStreamKey(ctx context.Context, args CreateStreamKeyInput, clients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if result, meta, err := requireConfirmation(args.Confirm, "CREATE STREAM KEY"); result != nil || meta != nil || err != nil {
		return result, meta, err
	}
	if args.StreamID == "" || args.Name == "" {
		return toolError("stream_id and name are required")
	}
	streamID, err := decodeStreamID(args.StreamID)
	if err != nil {
		return toolError(err.Error())
	}
	resp, err := clients.Commodore.CreateStreamKey(ctx, streamID, args.Name)
	if err != nil {
		logger.WithError(err).Warn("Failed to create stream key")
		return toolError(fmt.Sprintf("Failed to create stream key: %v", err))
	}
	return toolSuccess(CreateStreamKeyResult{
		Key:     streamKeyToToolResult(resp.GetStreamKey(), true),
		Warning: "STREAM KEY IS RETURNED IN THIS RESPONSE. Store it in your encoder or secret manager before discarding the response.",
	})
}

func handleDeleteStreamKey(ctx context.Context, args DeleteStreamKeyInput, clients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if result, meta, err := requireConfirmation(args.Confirm, "DELETE STREAM KEY"); result != nil || meta != nil || err != nil {
		return result, meta, err
	}
	if args.StreamID == "" || args.KeyID == "" {
		return toolError("stream_id and key_id are required")
	}
	streamID, err := decodeStreamID(args.StreamID)
	if err != nil {
		return toolError(err.Error())
	}
	if err := clients.Commodore.DeactivateStreamKey(ctx, streamID, args.KeyID); err != nil {
		logger.WithError(err).Warn("Failed to delete stream key")
		return toolError(fmt.Sprintf("Failed to delete stream key: %v", err))
	}
	return toolSuccess(map[string]any{"stream_id": streamID, "key_id": args.KeyID, "deleted": true})
}

func handleValidateStreamKey(ctx context.Context, args ValidateStreamKeyInput, clients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if args.StreamKey == "" {
		return toolError("stream_key is required")
	}
	resp, err := clients.Commodore.ValidateStreamKey(ctx, args.StreamKey)
	if err != nil {
		logger.WithError(err).Warn("Failed to validate stream key")
		return toolSuccess(ValidateStreamKeyResult{Status: "ERROR", Error: err.Error()})
	}
	if !resp.GetValid() {
		return toolSuccess(ValidateStreamKeyResult{Status: "INVALID", Error: resp.GetError()})
	}
	return toolSuccess(ValidateStreamKeyResult{Status: "VALID"})
}

func streamKeyToToolResult(k *pb.StreamKey, includeSecret bool) StreamKeyToolResult {
	if k == nil {
		return StreamKeyToolResult{}
	}
	out := StreamKeyToolResult{
		ID:       k.GetId(),
		StreamID: k.GetStreamId(),
		KeyName:  k.GetKeyName(),
		IsActive: k.GetIsActive(),
	}
	if includeSecret {
		out.KeyValue = k.GetKeyValue()
	}
	if ts := k.GetLastUsedAt(); ts != nil {
		out.LastUsedAt = ts.AsTime().Format(time.RFC3339)
	}
	if ts := k.GetCreatedAt(); ts != nil {
		out.CreatedAt = ts.AsTime().Format(time.RFC3339)
	}
	return out
}
