package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterStreamTools registers stream-related MCP tools.
func RegisterStreamTools(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	// create_stream - Create a new stream (requires balance)
	addTool(server,
		&mcp.Tool{
			Name:        "create_stream",
			Description: "Create a new push or pull live stream. Push streams return a usable stream key; pull streams return redacted source configuration and playback ID.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args CreateStreamInput) (*mcp.CallToolResult, any, error) {
			return handleCreateStream(ctx, args, clients, checker, logger)
		},
	)

	// update_stream - Update stream settings
	addTool(server,
		&mcp.Tool{
			Name:        "update_stream",
			Description: "Update stream settings, recording, or pull-source configuration.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args UpdateStreamInput) (*mcp.CallToolResult, any, error) {
			return handleUpdateStream(ctx, args, clients, checker, logger)
		},
	)

	// delete_stream - Delete a stream
	addTool(server,
		&mcp.Tool{
			Name:        "delete_stream",
			Description: "Delete a stream. This action cannot be undone.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args DeleteStreamInput) (*mcp.CallToolResult, any, error) {
			return handleDeleteStream(ctx, args, clients, checker, logger)
		},
	)

	// refresh_stream_key - Generate a new stream key
	addTool(server,
		&mcp.Tool{
			Name:        "refresh_stream_key",
			Description: "Rotate a push stream's primary ingest key. The old key stops working immediately. Pull and managed streams reject this operation. Requires confirm=\"ROTATE STREAM KEY\".",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args RefreshStreamKeyInput) (*mcp.CallToolResult, any, error) {
			return handleRefreshStreamKey(ctx, args, clients, logger)
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "list_stream_keys",
			Description: "List ingest keys for a push stream, including active state and last-used timestamps. Pull and managed streams reject this operation.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args ListStreamKeysInput) (*mcp.CallToolResult, any, error) {
			return handleListStreamKeys(ctx, args, clients, logger)
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "create_stream_key",
			Description: "Create an additional ingest key for a push stream. Pull and managed streams reject this operation. The key value is returned in the response. Requires confirm=\"CREATE STREAM KEY\".",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args CreateStreamKeyInput) (*mcp.CallToolResult, any, error) {
			return handleCreateStreamKey(ctx, args, clients, logger)
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "delete_stream_key",
			Description: "Deactivate a push stream's ingest key. Active encoders using it will fail ingest. Pull and managed streams reject this operation. Requires confirm=\"DELETE STREAM KEY\".",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args DeleteStreamKeyInput) (*mcp.CallToolResult, any, error) {
			return handleDeleteStreamKey(ctx, args, clients, logger)
		},
	)

	addTool(server,
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
	Name        string               `json:"name" jsonschema:"Stream display name"`
	Description string               `json:"description,omitempty" jsonschema:"Stream description"`
	Record      bool                 `json:"record,omitempty" jsonschema:"Enable DVR recording"`
	Public      bool                 `json:"public,omitempty" jsonschema:"Make stream publicly discoverable"`
	IngestMode  string               `json:"ingest_mode,omitempty" jsonschema:"push or pull. Defaults to push."`
	PullSource  *PullSourceToolInput `json:"pull_source,omitempty" jsonschema:"Required when ingest_mode is pull"`
	// Private and multicast pull sources need a restricted location.
	SourceLocation *SourceLocationToolInput `json:"source_location,omitempty" jsonschema:"Where the source may be ingested. Omit for any cluster FrameWorks chooses; required as restricted for private (LAN) or multicast pull sources."`
}

// SourceLocationToolInput mirrors the GraphQL SourceLocationInput.
type SourceLocationToolInput struct {
	Mode         string                           `json:"mode" jsonschema:"any or restricted"`
	Clusters     []SourceLocationClusterToolInput `json:"clusters,omitempty" jsonschema:"Clusters the source may run on; required for restricted"`
	AvoidNodeIDs []string                         `json:"avoid_node_ids,omitempty" jsonschema:"Nodes the source must never run on, only on clusters the tenant owns"`
}

type SourceLocationClusterToolInput struct {
	ClusterID string   `json:"cluster_id" jsonschema:"Cluster ID from the tenant's cluster access list"`
	NodeIDs   []string `json:"node_ids,omitempty" jsonschema:"Nodes of this cluster the source may run on; omit for any node. Only on clusters the tenant owns."`
}

// SourceLocationToolResult reports a stream's source location. mode custom
// means the stream's own ingest placement rules hold more than a location.
type SourceLocationToolResult struct {
	Mode         string                           `json:"mode"`
	Clusters     []SourceLocationClusterToolInput `json:"clusters,omitempty"`
	AvoidNodeIDs []string                         `json:"avoid_node_ids,omitempty"`
}

type PullSourceToolInput struct {
	SourceURI string `json:"source_uri" jsonschema:"Upstream RTSP, SRT, RIST, HLS, DTSC, or TS source URI"`
	Enabled   *bool  `json:"enabled,omitempty" jsonschema:"Whether the media plane may pull from the source. Defaults to true."`
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
	// Absent when Commodore did not report a location.
	SourceLocation *SourceLocationToolResult `json:"source_location,omitempty"`
	Message        string                    `json:"message"`
}

func handleCreateStream(ctx context.Context, args CreateStreamInput, clients *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}

	// Validate required fields
	if args.Name == "" {
		return toolError("Stream name is required")
	}

	location, locationErr := toProtoSourceLocation(args.SourceLocation)
	if locationErr != "" {
		return toolError(locationErr)
	}

	// Call Commodore to create stream (tenantID is in context metadata)
	resp, err := clients.Commodore.CreateStream(ctx, &commodorepb.CreateStreamRequest{
		Title:          args.Name,
		Description:    args.Description,
		IsPublic:       args.Public,
		IsRecording:    args.Record,
		IngestMode:     args.IngestMode,
		PullSource:     toProtoPullSource(args.PullSource),
		SourceLocation: location,
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
		ID:             globalid.Encode(globalid.TypeStream, resp.Id),
		StreamID:       resp.Id,
		StreamKey:      streamKey,
		PlaybackID:     resp.PlaybackId,
		Name:           resp.Title,
		IngestMode:     resp.IngestMode,
		PullSource:     fromProtoPullSource(resp.PullSource),
		SourceLocation: fromProtoSourceLocation(resp.GetSourceLocation()),
		Message:        message,
	}

	return toolSuccess(result)
}

// UpdateStreamInput represents input for update_stream tool.
type UpdateStreamInput struct {
	StreamID    string               `json:"stream_id" jsonschema:"Relay ID or stream_id to update"`
	Name        *string              `json:"name,omitempty" jsonschema:"New stream name"`
	Description *string              `json:"description,omitempty" jsonschema:"New description"`
	Record      *bool                `json:"record,omitempty" jsonschema:"Enable/disable recording"`
	IngestMode  *string              `json:"ingest_mode,omitempty" jsonschema:"Existing ingest mode. A different value is rejected."`
	PullSource  *PullSourceToolInput `json:"pull_source,omitempty" jsonschema:"Replacement pull-source configuration for pull streams"`
	// Omitted keeps the current location.
	SourceLocation *SourceLocationToolInput `json:"source_location,omitempty" jsonschema:"Replacement source location. Omit to keep the current one. Custom placement rules are edited with the media placement tools."`
}

// UpdateStreamResult represents the result of updating a stream.
type UpdateStreamResult struct {
	ID             string                    `json:"id"`
	StreamID       string                    `json:"stream_id"`
	Name           string                    `json:"name"`
	IngestMode     string                    `json:"ingest_mode"`
	PullSource     *PullSourceToolResult     `json:"pull_source,omitempty"`
	SourceLocation *SourceLocationToolResult `json:"source_location,omitempty"`
	Message        string                    `json:"message"`
}

func handleUpdateStream(ctx context.Context, args UpdateStreamInput, clients *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
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

	location, locationErr := toProtoSourceLocation(args.SourceLocation)
	if locationErr != "" {
		return toolError(locationErr)
	}

	// Call Commodore to update stream
	stream, err := clients.Commodore.UpdateStream(ctx, &commodorepb.UpdateStreamRequest{
		StreamId:       streamID,
		Name:           args.Name,
		Description:    args.Description,
		Record:         args.Record,
		IngestMode:     args.IngestMode,
		PullSource:     toProtoPullSource(args.PullSource),
		SourceLocation: location,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to update stream")
		return toolError(fmt.Sprintf("Failed to update stream: %v", err))
	}

	result := UpdateStreamResult{
		ID:             globalid.Encode(globalid.TypeStream, stream.StreamId),
		StreamID:       stream.StreamId,
		Name:           stream.Title,
		IngestMode:     stream.IngestMode,
		PullSource:     fromProtoPullSource(stream.PullSource),
		SourceLocation: fromProtoSourceLocation(stream.GetSourceLocation()),
		Message:        fmt.Sprintf("Stream '%s' updated.", stream.Title),
	}

	return toolSuccess(result)
}

// toProtoSourceLocation converts the tool input through the same shape rules
// as GraphQL; Commodore checks entitlement, node ownership and private-source
// consent.
func toProtoSourceLocation(input *SourceLocationToolInput) (*commodorepb.StreamSourceLocation, string) {
	if input == nil {
		return nil, ""
	}
	gql := &model.SourceLocationInput{AvoidNodeIds: input.AvoidNodeIDs}
	switch strings.ToLower(strings.TrimSpace(input.Mode)) {
	case "any":
		gql.Mode = model.SourceLocationModeAny
	case "restricted":
		gql.Mode = model.SourceLocationModeRestricted
	default:
		return nil, "source_location.mode must be any or restricted"
	}
	for _, cluster := range input.Clusters {
		gql.Clusters = append(gql.Clusters, &model.SourceLocationClusterInput{ClusterID: cluster.ClusterID, NodeIds: cluster.NodeIDs})
	}
	location, validationErr := resolvers.SourceLocationInputToProto(gql)
	if validationErr != nil {
		return nil, validationErr.Message
	}
	return location, ""
}

func fromProtoSourceLocation(location *commodorepb.StreamSourceLocation) *SourceLocationToolResult {
	if location == nil {
		return nil
	}
	out := &SourceLocationToolResult{AvoidNodeIDs: location.GetAvoidNodeIds()}
	switch location.GetMode() {
	case commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_ANY:
		out.Mode = "any"
	case commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_RESTRICTED:
		out.Mode = "restricted"
	case commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_CUSTOM:
		out.Mode = "custom"
	default:
		return nil
	}
	for _, cluster := range location.GetClusters() {
		out.Clusters = append(out.Clusters, SourceLocationClusterToolInput{ClusterID: cluster.GetClusterId(), NodeIDs: cluster.GetNodeIds()})
	}
	return out
}

func toProtoPullSource(input *PullSourceToolInput) *commodorepb.PullSourceInput {
	if input == nil {
		return nil
	}
	return &commodorepb.PullSourceInput{
		SourceUri: input.SourceURI,
		Enabled:   input.Enabled,
	}
}

func fromProtoPullSource(input *commodorepb.PullSourceView) *PullSourceToolResult {
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
	StreamID string `json:"stream_id" jsonschema:"Relay ID or stream_id to delete"`
}

// DeleteStreamResult represents the result of deleting a stream.
type DeleteStreamResult struct {
	ID       string `json:"id"`
	StreamID string `json:"stream_id"`
	Deleted  bool   `json:"deleted"`
	// Pending is true when the deletion was accepted but is NOT yet finalized — the serving cell has not yet
	// acknowledged the cleanup tombstone (deletion_pending). The two-phase saga converges via the outbox worker.
	Pending bool   `json:"pending"`
	Message string `json:"message"`
}

func handleDeleteStream(ctx context.Context, args DeleteStreamInput, clients *clients.ServiceClients, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
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

	// Call Commodore to delete stream
	resp, err := clients.Commodore.DeleteStream(ctx, streamID)
	if err != nil {
		logger.WithError(err).Warn("Failed to delete stream")
		return toolError(fmt.Sprintf("Failed to delete stream: %v", err))
	}

	// Report the TRUTHFUL saga state: deletion is "deleted" only after the serving cell acked the tombstone;
	// otherwise it is pending (the outbox worker converges it). resp.Message already carries the pending text.
	finalized := resp.GetDeletionStatus() == "deleted"
	result := DeleteStreamResult{
		ID:       globalid.Encode(globalid.TypeStream, resp.StreamId),
		StreamID: resp.StreamId,
		Deleted:  finalized,
		Pending:  !finalized,
		Message:  resp.Message,
	}

	return toolSuccess(result)
}

// RefreshStreamKeyInput represents input for refresh_stream_key tool.
type RefreshStreamKeyInput struct {
	StreamID string `json:"stream_id" jsonschema:"Relay ID or stream_id to refresh key for"`
	Confirm  string `json:"confirm" jsonschema:"Must be exactly 'ROTATE STREAM KEY'."`
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
	StreamID string `json:"stream_id" jsonschema:"Relay ID or stream_id to list keys for"`
}

type CreateStreamKeyInput struct {
	StreamID string `json:"stream_id" jsonschema:"Relay ID or stream_id to create a key for"`
	Name     string `json:"name" jsonschema:"Human-readable key name"`
	Confirm  string `json:"confirm" jsonschema:"Must be exactly 'CREATE STREAM KEY'."`
}

type DeleteStreamKeyInput struct {
	StreamID string `json:"stream_id" jsonschema:"Relay ID or stream_id that owns the key"`
	KeyID    string `json:"key_id" jsonschema:"Stream key UUID"`
	Confirm  string `json:"confirm" jsonschema:"Must be exactly 'DELETE STREAM KEY'."`
}

type ValidateStreamKeyInput struct {
	StreamKey string `json:"stream_key" jsonschema:"Raw ingest stream key to validate"`
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

func streamKeyToToolResult(k *commodorepb.StreamKey, includeSecret bool) StreamKeyToolResult {
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
