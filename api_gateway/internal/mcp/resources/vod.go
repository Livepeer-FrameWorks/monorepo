package resources

import (
	"context"
	"fmt"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"frameworks/pkg/globalid"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterVODResources registers VOD asset-related MCP resources.
func RegisterVODResources(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, logger logging.Logger) {
	// vod://list - List all VOD assets
	server.AddResource(&mcp.Resource{
		URI:         "vod://list",
		Name:        "VOD Asset List",
		Description: "List all VOD assets (uploaded videos) in the account.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleVODList(ctx, clients, logger)
	})

	// vod://{artifact_hash} - VOD asset details
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "vod://{artifact_hash}",
		Name:        "VOD Asset Details",
		Description: "Details for a specific VOD asset by relay ID or artifact hash.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleVODByID(ctx, req.Params.URI, clients, logger)
	})
}

// VODAssetInfo represents a VOD asset in the list.
type VODAssetInfo struct {
	ID              string  `json:"id"`
	ArtifactHash    string  `json:"artifact_hash"`
	PlaybackID      string  `json:"playback_id"`
	Status          string  `json:"status"`
	Title           *string `json:"title,omitempty"`
	Description     *string `json:"description,omitempty"`
	Filename        *string `json:"filename,omitempty"`
	SizeBytes       *int64  `json:"size_bytes,omitempty"`
	DurationMs      *int    `json:"duration_ms,omitempty"`
	Resolution      *string `json:"resolution,omitempty"`
	VideoCodec      *string `json:"video_codec,omitempty"`
	AudioCodec      *string `json:"audio_codec,omitempty"`
	BitrateKbps     *int    `json:"bitrate_kbps,omitempty"`
	StorageLocation string  `json:"storage_location,omitempty"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	ExpiresAt       *string `json:"expires_at,omitempty"`
	ErrorMessage    *string `json:"error_message,omitempty"`
}

// VODListResponse represents the vod://list response.
type VODListResponse struct {
	Assets  []VODAssetInfo `json:"assets"`
	Total   int            `json:"total"`
	HasMore bool           `json:"has_more"`
}

func handleVODList(ctx context.Context, clients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("not authenticated")
	}

	// Build pagination request
	pagination := &pb.CursorPaginationRequest{
		First: 50,
	}

	// Get VOD assets from Commodore
	resp, err := clients.Commodore.ListVodAssets(ctx, tenantID, pagination)
	if err != nil {
		logger.WithError(err).Warn("Failed to list VOD assets")
		return nil, fmt.Errorf("failed to list VOD assets: %w", err)
	}

	assets := make([]VODAssetInfo, 0, len(resp.Assets))
	for _, a := range resp.Assets {
		info := protoToVODAssetInfo(a)
		assets = append(assets, info)
	}

	hasMore := resp.Pagination != nil && resp.Pagination.HasNextPage
	total := len(assets)
	if resp.Pagination != nil {
		total = int(resp.Pagination.TotalCount)
	}

	return marshalResourceResult("vod://list", VODListResponse{
		Assets:  assets,
		Total:   total,
		HasMore: hasMore,
	})
}

func handleVODByID(ctx context.Context, uri string, clients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("not authenticated")
	}

	// Extract asset ID from URI: vod://{id}
	rawID := strings.TrimPrefix(uri, "vod://")
	if rawID == "" || rawID == "list" {
		return nil, fmt.Errorf("invalid artifact hash")
	}

	artifactHash, err := resolveVodIdentifier(ctx, rawID, clients)
	if err != nil {
		return nil, err
	}

	// Get VOD asset from Commodore
	asset, err := clients.Commodore.GetVodAsset(ctx, tenantID, artifactHash)
	if err != nil {
		return nil, fmt.Errorf("VOD asset not found: %w", err)
	}

	info := protoToVODAssetInfo(asset)
	return marshalResourceResult(uri, info)
}

func protoToVODAssetInfo(p *pb.VodAssetInfo) VODAssetInfo {
	vodID := p.ArtifactHash
	if vodID == "" {
		vodID = p.Id
	}
	info := VODAssetInfo{
		ID:              globalid.Encode(globalid.TypeVodAsset, vodID),
		ArtifactHash:    p.ArtifactHash,
		StorageLocation: p.StorageLocation,
	}

	// Map status
	switch p.Status {
	case pb.VodStatus_VOD_STATUS_UPLOADING:
		info.Status = "UPLOADING"
	case pb.VodStatus_VOD_STATUS_PROCESSING:
		info.Status = "PROCESSING"
	case pb.VodStatus_VOD_STATUS_READY:
		info.Status = "READY"
	case pb.VodStatus_VOD_STATUS_FAILED:
		info.Status = "FAILED"
	case pb.VodStatus_VOD_STATUS_DELETED:
		info.Status = "DELETED"
	default:
		info.Status = "UNKNOWN"
	}

	// Optional fields
	if p.PlaybackId != nil && *p.PlaybackId != "" {
		info.PlaybackID = *p.PlaybackId
	}
	if p.Title != "" {
		info.Title = &p.Title
	}
	if p.Description != "" {
		info.Description = &p.Description
	}
	if p.Filename != "" {
		info.Filename = &p.Filename
	}
	if p.SizeBytes != nil {
		info.SizeBytes = p.SizeBytes
	}
	if p.DurationMs != nil {
		dur := int(*p.DurationMs)
		info.DurationMs = &dur
	}
	if p.Resolution != nil {
		info.Resolution = p.Resolution
	}
	if p.VideoCodec != nil {
		info.VideoCodec = p.VideoCodec
	}
	if p.AudioCodec != nil {
		info.AudioCodec = p.AudioCodec
	}
	if p.BitrateKbps != nil {
		br := int(*p.BitrateKbps)
		info.BitrateKbps = &br
	}
	if p.ErrorMessage != nil {
		info.ErrorMessage = p.ErrorMessage
	}

	// Timestamps
	if p.CreatedAt != nil {
		info.CreatedAt = p.CreatedAt.AsTime().Format("2006-01-02T15:04:05Z")
	}
	if p.UpdatedAt != nil {
		info.UpdatedAt = p.UpdatedAt.AsTime().Format("2006-01-02T15:04:05Z")
	}
	if p.ExpiresAt != nil {
		exp := p.ExpiresAt.AsTime().Format("2006-01-02T15:04:05Z")
		info.ExpiresAt = &exp
	}

	return info
}

func resolveVodIdentifier(ctx context.Context, input string, clients *clients.ServiceClients) (string, error) {
	if input == "" {
		return "", fmt.Errorf("invalid artifact hash")
	}
	if typ, id, ok := globalid.Decode(input); ok {
		if typ != globalid.TypeVodAsset {
			return "", fmt.Errorf("invalid VOD relay ID type: %s", typ)
		}
		resp, err := clients.Commodore.ResolveVodID(ctx, id)
		if err != nil {
			return "", fmt.Errorf("failed to resolve VOD relay ID: %w", err)
		}
		if resp == nil || !resp.Found {
			return "", fmt.Errorf("VOD asset not found")
		}
		return resp.VodHash, nil
	}
	return input, nil
}
