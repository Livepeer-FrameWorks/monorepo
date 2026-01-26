package tools

import (
	"context"
	"fmt"

	"frameworks/api_gateway/internal/clients"
	"frameworks/pkg/globalid"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/logging"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterPlaybackTools registers playback-related MCP tools.
func RegisterPlaybackTools(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	// resolve_playback_endpoint - Get playback URLs for content
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "resolve_playback_endpoint",
			Description: "Resolve playback URLs for a stream or VOD content. Returns primary and fallback endpoints.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args ResolvePlaybackInput) (*mcp.CallToolResult, any, error) {
			return handleResolvePlayback(ctx, args, clients, logger)
		},
	)
}

// ResolvePlaybackInput represents input for resolve_playback_endpoint tool.
type ResolvePlaybackInput struct {
	ContentID string `json:"content_id" jsonschema:"required" jsonschema_description:"Content ID (playback_id, stream_id, or Relay ID)"`
	ViewerIP  string `json:"viewer_ip,omitempty" jsonschema_description:"Viewer IP for geo-routing (optional)"`
}

// PlaybackEndpoint represents a resolved playback endpoint.
type PlaybackEndpoint struct {
	NodeID   string  `json:"node_id"`
	URL      string  `json:"url"`
	Protocol string  `json:"protocol"`
	Distance float64 `json:"geo_distance,omitempty"`
}

// ResolvePlaybackResult represents the result of resolving playback.
type ResolvePlaybackResult struct {
	Primary   PlaybackEndpoint   `json:"primary"`
	Fallbacks []PlaybackEndpoint `json:"fallbacks,omitempty"`
	Message   string             `json:"message"`
}

func handleResolvePlayback(ctx context.Context, args ResolvePlaybackInput, clients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if args.ContentID == "" {
		return toolError("content_id is required")
	}

	contentID := args.ContentID
	if typ, id, ok := globalid.Decode(args.ContentID); ok {
		switch typ {
		case globalid.TypeStream:
			contentID = id
		case globalid.TypeVodAsset:
			artifactHash, err := resolveVodIdentifier(ctx, args.ContentID, clients)
			if err != nil {
				return toolError(err.Error())
			}
			contentID = artifactHash
		default:
			return toolError(fmt.Sprintf("unsupported relay ID type: %s", typ))
		}
	}

	// Call Commodore to resolve viewer endpoint
	resp, err := clients.Commodore.ResolveViewerEndpoint(ctx, contentID, args.ViewerIP)
	if err != nil {
		logger.WithError(err).Warn("Failed to resolve playback endpoint")
		return toolError(fmt.Sprintf("Failed to resolve playback endpoint: %v", err))
	}

	if resp.Primary == nil {
		return toolError("No playback endpoint available for this content")
	}

	result := ResolvePlaybackResult{
		Primary: PlaybackEndpoint{
			NodeID:   resp.Primary.NodeId,
			URL:      resp.Primary.Url,
			Protocol: resp.Primary.Protocol,
			Distance: resp.Primary.GeoDistance,
		},
		Fallbacks: make([]PlaybackEndpoint, 0, len(resp.Fallbacks)),
		Message:   fmt.Sprintf("Playback endpoint resolved. Primary: %s (%s)", resp.Primary.Url, resp.Primary.Protocol),
	}

	for _, fb := range resp.Fallbacks {
		result.Fallbacks = append(result.Fallbacks, PlaybackEndpoint{
			NodeID:   fb.NodeId,
			URL:      fb.Url,
			Protocol: fb.Protocol,
			Distance: fb.GeoDistance,
		})
	}

	return toolSuccess(result)
}
