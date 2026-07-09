package tools

import (
	"context"
	"fmt"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterPlaybackTools registers playback-related MCP tools.
func RegisterPlaybackTools(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	// resolve_playback_endpoint - Get playback URLs for content
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "resolve_playback_endpoint",
			Description: "Resolve playback URLs for a live stream, VOD, clip, or DVR recording. Returns primary and fallback endpoints, plus thumbnail and sprite preview URLs when available.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args ResolvePlaybackInput) (*mcp.CallToolResult, any, error) {
			return handleResolvePlayback(ctx, args, clients, logger)
		},
	)
}

// ResolvePlaybackInput represents input for resolve_playback_endpoint tool.
type ResolvePlaybackInput struct {
	ContentID string `json:"content_id" jsonschema:"required" jsonschema_description:"Public playback_id (live, VOD, clip, or DVR), or a FrameWorks global ID (Stream/Clip/VodAsset). A raw internal stream UUID / stream_id / internal_name is not accepted — use the playback_id."`
	ViewerIP  string `json:"viewer_ip,omitempty" jsonschema_description:"Viewer IP for geo-routing (optional)"`
}

// PlaybackEndpoint represents a resolved playback endpoint.
type PlaybackEndpoint struct {
	NodeID   string  `json:"node_id"`
	URL      string  `json:"url"`
	Protocol string  `json:"protocol"`
	Distance float64 `json:"geo_distance,omitempty"`
}

type PlaybackThumbnailAssets struct {
	PosterURL    string `json:"poster_url,omitempty"`
	SpriteVTTURL string `json:"sprite_vtt_url,omitempty"`
	SpriteJPGURL string `json:"sprite_jpg_url,omitempty"`
	AssetKey     string `json:"asset_key,omitempty"`
}

// ResolvePlaybackResult represents the result of resolving playback.
type ResolvePlaybackResult struct {
	Primary         PlaybackEndpoint         `json:"primary"`
	Fallbacks       []PlaybackEndpoint       `json:"fallbacks,omitempty"`
	ThumbnailAssets *PlaybackThumbnailAssets `json:"thumbnail_assets,omitempty"`
	Message         string                   `json:"message"`
}

func handleResolvePlayback(ctx context.Context, args ResolvePlaybackInput, clients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if args.ContentID == "" {
		return toolError("content_id is required")
	}

	// The MCP access middleware normalizes content_id once and stashes the canonical
	// playback_id; reuse it to avoid re-resolving. Fall back to normalizing here if
	// it's absent (e.g. middleware normalization failed or was skipped).
	contentID := ctxkeys.GetPlaybackContentID(ctx)
	if contentID == "" {
		normalized, _, nErr := NormalizePlaybackContent(ctx, args.ContentID, clients)
		if nErr != nil {
			return toolError(nErr.Error())
		}
		contentID = normalized
	}

	// Call Commodore to resolve viewer endpoint
	resp, err := clients.Commodore.ResolveViewerEndpoint(ctx, contentID, args.ViewerIP, "")
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

	if assets := resp.GetMetadata().GetThumbnailAssets(); assets != nil {
		result.ThumbnailAssets = &PlaybackThumbnailAssets{
			PosterURL:    assets.GetPosterUrl(),
			SpriteVTTURL: assets.GetSpriteVttUrl(),
			SpriteJPGURL: assets.GetSpriteJpgUrl(),
			AssetKey:     assets.GetAssetKey(),
		}
	}

	return toolSuccess(result)
}
