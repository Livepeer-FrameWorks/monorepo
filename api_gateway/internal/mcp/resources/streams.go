package resources

import (
	"context"
	"fmt"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/globalid"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterStreamResources registers stream-related MCP resources.
func RegisterStreamResources(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, logger logging.Logger) {
	// streams://list - List all streams
	server.AddResource(&mcp.Resource{
		URI:         "streams://list",
		Name:        "Stream List",
		Description: "List all streams in the account.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleStreamsList(ctx, clients, logger)
	})

	// streams://{id} - Stream details
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "streams://{id}",
		Name:        "Stream Details",
		Description: "Details for a specific stream by relay ID or stream_id.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return HandleStreamByID(ctx, req.Params.URI, clients, logger)
	})

	// streams://{id}/health - Stream health metrics
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "streams://{id}/health",
		Name:        "Stream Health",
		Description: "Health metrics for a specific stream by relay ID or stream_id.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return HandleStreamByID(ctx, req.Params.URI, clients, logger)
	})
}

// StreamInfo represents a stream in the list.
type StreamInfo struct {
	ID             string `json:"id"`
	StreamID       string `json:"stream_id"`
	Title          string `json:"title"`
	Description    string `json:"description,omitempty"`
	Status         string `json:"status,omitempty"`
	IsLive         bool   `json:"is_live"`
	IsRecording    bool   `json:"is_recording"`
	RecordEnabled  bool   `json:"record_enabled"`
	PlaybackID     string `json:"playback_id"`
	CurrentViewers int    `json:"current_viewers,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
}

// StreamsListResponse represents the streams://list response.
type StreamsListResponse struct {
	Streams []StreamInfo `json:"streams"`
	HasMore bool         `json:"has_more"`
}

func handleStreamsList(ctx context.Context, clients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, mcperrors.AuthRequired()
	}

	// Build pagination request
	pagination := &pb.CursorPaginationRequest{
		First: 50,
	}

	// Get streams from Commodore (tenantID is passed via context metadata)
	resp, err := clients.Commodore.ListStreams(ctx, pagination)
	if err != nil {
		logger.WithError(err).Warn("Failed to list streams")
		return nil, fmt.Errorf("failed to list streams: %w", err)
	}

	streams := make([]StreamInfo, 0, len(resp.Streams))
	for _, s := range resp.Streams {
		info := StreamInfo{
			ID:             globalid.Encode(globalid.TypeStream, s.StreamId),
			StreamID:       s.StreamId,
			Title:          s.Title,
			Description:    s.Description,
			Status:         s.Status,
			IsLive:         s.IsLive,
			IsRecording:    s.IsRecording,
			RecordEnabled:  s.IsRecordingEnabled,
			PlaybackID:     s.PlaybackId,
			CurrentViewers: int(s.CurrentViewers),
		}
		if s.CreatedAt != nil {
			info.CreatedAt = s.CreatedAt.AsTime().Format("2006-01-02T15:04:05Z")
		}
		streams = append(streams, info)
	}

	hasMore := resp.Pagination != nil && resp.Pagination.HasNextPage
	return marshalResourceResult("streams://list", StreamsListResponse{
		Streams: streams,
		HasMore: hasMore,
	})
}

// HandleStreamByID handles requests for streams://{id} resources.
// This is called when a dynamic resource URI is requested.
func HandleStreamByID(ctx context.Context, uri string, clients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, mcperrors.AuthRequired()
	}

	rawID := strings.TrimPrefix(uri, "streams://")
	if rawID == "" || rawID == "list" {
		return nil, fmt.Errorf("invalid stream ID")
	}

	// Check if this is a health request
	if strings.HasSuffix(rawID, "/health") {
		rawID = strings.TrimSuffix(rawID, "/health")
		if rawID == "" {
			return nil, fmt.Errorf("invalid stream ID")
		}
		streamID, err := decodeStreamIdentifier(rawID)
		if err != nil {
			return nil, err
		}
		return handleStreamHealth(ctx, streamID, clients, logger)
	}

	streamID, err := decodeStreamIdentifier(rawID)
	if err != nil {
		return nil, err
	}

	// Get stream details from Commodore (tenantID is passed via context metadata)
	stream, err := clients.Commodore.GetStream(ctx, streamID)
	if err != nil {
		return nil, fmt.Errorf("stream not found: %w", err)
	}

	info := StreamInfo{
		ID:             globalid.Encode(globalid.TypeStream, stream.StreamId),
		StreamID:       stream.StreamId,
		Title:          stream.Title,
		Description:    stream.Description,
		Status:         stream.Status,
		IsLive:         stream.IsLive,
		IsRecording:    stream.IsRecording,
		RecordEnabled:  stream.IsRecordingEnabled,
		PlaybackID:     stream.PlaybackId,
		CurrentViewers: int(stream.CurrentViewers),
	}
	if stream.CreatedAt != nil {
		info.CreatedAt = stream.CreatedAt.AsTime().Format("2006-01-02T15:04:05Z")
	}

	return marshalResourceResult(uri, info)
}

// StreamHealthInfo represents stream health metrics.
type StreamHealthInfo struct {
	ID             string `json:"id"`
	StreamID       string `json:"stream_id"`
	Status         string `json:"status,omitempty"`
	IsLive         bool   `json:"is_live"`
	CurrentViewers int    `json:"current_viewers,omitempty"`
	PeakViewers    int    `json:"peak_viewers,omitempty"`
	Duration       int    `json:"duration_seconds,omitempty"`
}

func handleStreamHealth(ctx context.Context, streamID string, clients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	// Get stream to check its current state
	stream, err := clients.Commodore.GetStream(ctx, streamID)
	if err != nil {
		return nil, fmt.Errorf("stream not found: %w", err)
	}

	health := StreamHealthInfo{
		ID:             globalid.Encode(globalid.TypeStream, stream.StreamId),
		StreamID:       stream.StreamId,
		Status:         stream.Status,
		IsLive:         stream.IsLive,
		CurrentViewers: int(stream.CurrentViewers),
		PeakViewers:    int(stream.PeakViewers),
		Duration:       int(stream.Duration),
	}

	return marshalResourceResult(fmt.Sprintf("streams://%s/health", streamID), health)
}

func decodeStreamIdentifier(input string) (string, error) {
	if input == "" {
		return "", fmt.Errorf("invalid stream ID")
	}
	if typ, id, ok := globalid.Decode(input); ok {
		if typ != globalid.TypeStream {
			return "", fmt.Errorf("invalid stream relay ID type: %s", typ)
		}
		return id, nil
	}
	return input, nil
}
