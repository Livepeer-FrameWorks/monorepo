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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterStreamTools registers stream-related MCP tools.
func RegisterStreamTools(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	registerStreamMediaTools(server, resolver)

	addTool(server, &mcp.Tool{Name: "get_stream_ingest", Description: "Read a stream's current ingest session: idle, connected or live, with its session ID and timestamps."},
		func(ctx context.Context, req *mcp.CallToolRequest, args GetStreamIngestInput) (*mcp.CallToolResult, any, error) {
			id, err := decodeStreamID(args.StreamID)
			if err != nil {
				return toolError(err.Error())
			}
			stream, err := resolver.DoGetStream(ctx, id)
			if err != nil {
				return nil, nil, err
			}
			view, err := resolver.DoStreamIngest(ctx, stream)
			if err != nil {
				return nil, nil, err
			}
			return toolSuccess(view)
		})
	addTool(server, &mcp.Tool{Name: "disconnect_ingest", Description: "End the currently claimed push publisher. Pull streams are stopped by setting pull_source.enabled=false. Requires confirm='DISCONNECT INGEST'."}, func(ctx context.Context, _ *mcp.CallToolRequest, args DisconnectIngestInput) (*mcp.CallToolResult, any, error) {
		return handleDisconnectIngest(ctx, args, clients)
	})

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
	TranscodeLadder string               `json:"transcode_ladder,omitempty" jsonschema:"Allowed ladder name; omit to inherit"`
	Name            string               `json:"name" jsonschema:"Stream display name"`
	Description     string               `json:"description,omitempty" jsonschema:"Stream description"`
	Record          bool                 `json:"record,omitempty" jsonschema:"Enable DVR recording"`
	LiveVideoABR    string               `json:"live_video_abr,omitempty" jsonschema:"INHERIT (default) or OFF for live video renditions"`
	Public          bool                 `json:"public,omitempty" jsonschema:"Make stream publicly discoverable"`
	IngestMode      string               `json:"ingest_mode,omitempty" jsonschema:"push or pull. Defaults to push."`
	PullSource      *PullSourceToolInput `json:"pull_source,omitempty" jsonschema:"Required when ingest_mode is pull"`
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
		Title:           args.Name,
		Description:     args.Description,
		IsPublic:        args.Public,
		IsRecording:     args.Record,
		LiveVideoAbr:    args.LiveVideoABR,
		TranscodeLadder: args.TranscodeLadder,
		IngestMode:      args.IngestMode,
		PullSource:      toProtoPullSource(args.PullSource),
		SourceLocation:  location,
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
	TranscodeLadder *string              `json:"transcode_ladder,omitempty" jsonschema:"Ladder name; empty inherits; omit to keep current"`
	StreamID        string               `json:"stream_id" jsonschema:"Relay ID or stream_id to update"`
	Name            *string              `json:"name,omitempty" jsonschema:"New stream name"`
	Description     *string              `json:"description,omitempty" jsonschema:"New description"`
	Record          *bool                `json:"record,omitempty" jsonschema:"Enable/disable recording"`
	LiveVideoABR    *string              `json:"live_video_abr,omitempty" jsonschema:"INHERIT or OFF; omit to keep the current setting"`
	IngestMode      *string              `json:"ingest_mode,omitempty" jsonschema:"Existing ingest mode. A different value is rejected."`
	PullSource      *PullSourceToolInput `json:"pull_source,omitempty" jsonschema:"Replacement pull-source configuration for pull streams"`
	// Omitted keeps the current location.
	SourceLocation *SourceLocationToolInput `json:"source_location,omitempty" jsonschema:"Replacement source location. Omit to keep the current one. Custom placement rules are edited with the media placement tools."`
}

// UpdateStreamResult represents the result of updating a stream.
type UpdateStreamResult struct {
	ID             string                    `json:"id"`
	StreamID       string                    `json:"stream_id"`
	Name           string                    `json:"name"`
	IngestMode     string                    `json:"ingest_mode"`
	LiveVideoABR   string                    `json:"live_video_abr"`
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
		StreamId:        streamID,
		Name:            args.Name,
		Description:     args.Description,
		Record:          args.Record,
		LiveVideoAbr:    args.LiveVideoABR,
		TranscodeLadder: args.TranscodeLadder,
		IngestMode:      args.IngestMode,
		PullSource:      toProtoPullSource(args.PullSource),
		SourceLocation:  location,
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
		LiveVideoABR:   stream.GetLiveVideoAbr(),
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
	EndActiveSession bool   `json:"end_active_session,omitempty" jsonschema:"Also end the publisher active when the key is rotated"`
	StreamID         string `json:"stream_id" jsonschema:"Relay ID or stream_id to refresh key for"`
	Confirm          string `json:"confirm" jsonschema:"Must be exactly 'ROTATE STREAM KEY'."`
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
	var resp *commodorepb.RefreshStreamKeyResponse
	if args.EndActiveSession {
		resp, err = clients.Commodore.RefreshStreamKeyAndEndIngest(ctx, streamID)
	} else {
		resp, err = clients.Commodore.RefreshStreamKey(ctx, streamID)
	}
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

type ValidateStreamKeyInput struct {
	StreamKey string `json:"stream_key" jsonschema:"Raw ingest stream key to validate"`
}

type ValidateStreamKeyResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
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

type GetStreamIngestInput struct {
	StreamID string `json:"stream_id" jsonschema:"Stream relay ID or stream UUID"`
}

type DisconnectIngestInput struct {
	StreamID string `json:"stream_id" jsonschema:"Stream UUID or GraphQL ID"`
	Confirm  string `json:"confirm" jsonschema:"Must be exactly 'DISCONNECT INGEST'."`
}

func handleDisconnectIngest(ctx context.Context, args DisconnectIngestInput, clients *clients.ServiceClients) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	if result, metadata, err := requireConfirmation(args.Confirm, "DISCONNECT INGEST"); result != nil || metadata != nil || err != nil {
		return result, metadata, err
	}
	id, err := decodeStreamID(args.StreamID)
	if err != nil {
		return toolError(err.Error())
	}
	response, err := clients.Commodore.DisconnectIngest(ctx, id)
	if err != nil {
		return toolError(err.Error())
	}
	return toolSuccess(response)
}
