package resources

import (
	"context"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/logging"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// KnowledgeSource represents a curated documentation source.
type KnowledgeSource struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Index       string `json:"index"`
	Sitemap     string `json:"sitemap,omitempty"`
}

// KnowledgeSources represents the list of curated knowledge sources.
type KnowledgeSources struct {
	Sources []KnowledgeSource `json:"sources"`
}

// RegisterKnowledgeResources registers knowledge-related MCP resources.
func RegisterKnowledgeResources(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, logger logging.Logger) {
	server.AddResource(&mcp.Resource{
		URI:         "knowledge://sources",
		Name:        "Knowledge Sources",
		Description: "Curated list of video streaming documentation sites. Use these sitemaps/indexes to find relevant guides on codecs, encoding, protocols, and troubleshooting.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleKnowledgeSources()
	})
}

func handleKnowledgeSources() (*mcp.ReadResourceResult, error) {
	sources := KnowledgeSources{
		Sources: []KnowledgeSource{
			{
				Name:        "FrameWorks Docs",
				Description: "Platform documentation for streamers, operators, and hybrid setups. Covers ingest protocols, playback, API reference, and cluster deployment.",
				Index:       "https://docs.frameworks.network/",
				Sitemap:     "https://docs.frameworks.network/sitemap.xml",
			},
			{
				Name:        "MistServer Docs",
				Description: "MistServer configuration, protocols, and API reference. Covers stream triggers, push targets, and media container formats.",
				Index:       "https://docs.mistserver.org/",
				Sitemap:     "https://mistserver.org/sitemap.xml",
			},
			{
				Name:        "FFmpeg Wiki",
				Description: "Encoding guides for H.264, HEVC, VP9, AV1. Hardware acceleration, bitrate control, and codec-specific tuning.",
				Index:       "https://trac.ffmpeg.org/wiki/TitleIndex",
			},
			{
				Name:        "OBS Wiki",
				Description: "OBS Studio setup, streaming configuration, encoder settings, and troubleshooting guides.",
				Index:       "https://obsproject.com/wiki/",
			},
			{
				Name:        "SRT Alliance",
				Description: "SRT protocol specification, configuration, and latency tuning.",
				Index:       "https://www.srtalliance.org/",
			},
			{
				Name:        "HLS Specification",
				Description: "HTTP Live Streaming (RFC 8216), playlist formats, and segment encoding.",
				Index:       "https://datatracker.ietf.org/doc/html/rfc8216",
			},
			{
				Name:        "nginx-rtmp",
				Description: "nginx-rtmp-module configuration, directives, and live streaming setup.",
				Index:       "https://github.com/arut/nginx-rtmp-module/wiki",
			},
			{
				Name:        "Ecosystem",
				Description: "Livepeer network, WebRTC standards, DASH specification, and related streaming tools.",
				Index:       "https://docs.livepeer.org/",
			},
		},
	}

	return marshalResourceResult("knowledge://sources", sources)
}
