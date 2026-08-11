package resources

import (
	"context"
	"fmt"
	"strings"

	"frameworks/api_gateway/internal/catalogview"
	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterVODResources registers VOD asset-related MCP resources.
func RegisterVODResources(server *mcp.Server, clients *clients.ServiceClients, logger logging.Logger) {
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
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, mcperrors.AuthRequired()
	}

	// Read the CANONICAL catalog for VOD-kind artifacts: its derived status/duration/track summary
	// is authoritative, so ready/failed assets and their metadata are reported correctly.
	resp, err := clients.Commodore.ListStorageArtifacts(ctx, &commodorepb.ListStorageArtifactsRequest{
		TenantId: tenantID,
		Kinds:    []string{"vod"},
		Limit:    50,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to list VOD assets")
		return nil, fmt.Errorf("failed to list VOD assets: %w", err)
	}

	assets := make([]VODAssetInfo, 0, len(resp.GetArtifacts()))
	for _, a := range resp.GetArtifacts() {
		assets = append(assets, storageArtifactToVODAssetInfo(a))
	}

	total := len(assets)
	if resp.GetTotalCount() > 0 {
		total = int(resp.GetTotalCount())
	}

	return marshalResourceResult("vod://list", VODListResponse{
		Assets:  assets,
		Total:   total,
		HasMore: resp.GetHasNextPage(),
	})
}

func handleVODByID(ctx context.Context, uri string, clients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, mcperrors.AuthRequired()
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

	// Exact-hash lookup against the CANONICAL catalog (single source of truth). Restricted to
	// kind=vod: the vod:// resource is for uploaded VOD assets, and the catalog unions other kinds
	// (clip/dvr/chapter) under the same hash space, so a non-VOD hash must not resolve here.
	resp, err := clients.Commodore.ListStorageArtifacts(ctx, &commodorepb.ListStorageArtifactsRequest{
		TenantId:       tenantID,
		ArtifactHashes: []string{artifactHash},
		Kinds:          []string{"vod"},
		Limit:          1,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read VOD asset: %w", err)
	}
	arts := resp.GetArtifacts()
	if len(arts) == 0 {
		return nil, fmt.Errorf("VOD asset not found")
	}
	return marshalResourceResult(uri, storageArtifactToVODAssetInfo(arts[0]))
}

// storageArtifactToVODAssetInfo maps a canonical catalog row onto the MCP VOD resource. Status is
// the catalog's DERIVED lifecycle status (not a hardcoded default); duration/resolution/codecs come
// from the finalized track summary; filename is the catalog's secondary_label projection;
// description and error_message are projected onto the catalog row.
func storageArtifactToVODAssetInfo(a *commodorepb.StorageArtifactInfo) VODAssetInfo {
	vodID := a.GetArtifactHash()
	if vodID == "" {
		vodID = a.GetId()
	}
	info := VODAssetInfo{
		ID:              globalid.Encode(globalid.TypeVodAsset, vodID),
		ArtifactHash:    a.GetArtifactHash(),
		PlaybackID:      a.GetPlaybackId(),
		Status:          vodStatusLabel(a.GetStatus()),
		StorageLocation: a.GetStorageLocation(),
	}

	if v := a.GetTitle(); v != "" {
		info.Title = &v
	}
	if v := a.GetSecondaryLabel(); v != "" {
		info.Filename = &v
	}
	if v := a.GetDescription(); v != "" {
		info.Description = &v
	}
	if v := a.GetErrorMessage(); v != "" {
		info.ErrorMessage = &v
	}
	if a.SizeBytes != nil {
		sz := a.GetSizeBytes()
		info.SizeBytes = &sz
	}
	if a.DurationMs != nil {
		dur := int(a.GetDurationMs())
		info.DurationMs = &dur
	}
	info.Resolution, info.VideoCodec, info.AudioCodec, info.BitrateKbps = catalogview.TrackSummary(a.GetTracks())

	if a.CreatedAt != nil {
		info.CreatedAt = a.GetCreatedAt().AsTime().Format("2006-01-02T15:04:05Z")
	}
	if a.UpdatedAt != nil {
		info.UpdatedAt = a.GetUpdatedAt().AsTime().Format("2006-01-02T15:04:05Z")
	}
	if a.ExpiresAt != nil {
		exp := a.GetExpiresAt().AsTime().Format("2006-01-02T15:04:05Z")
		info.ExpiresAt = &exp
	}

	return info
}

// vodStatusLabel maps the catalog's derived lifecycle status string onto the MCP status label.
func vodStatusLabel(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ready", "completed", "complete", "done", "synced":
		return "READY"
	case "processing":
		return "PROCESSING"
	case "failed", "error":
		return "FAILED"
	case "deleted", "expired", "evicted":
		return "DELETED"
	case "uploading", "requested", "queued":
		return "UPLOADING"
	default:
		return "UNKNOWN"
	}
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
		callerTenant := ctxkeys.GetTenantID(ctx)
		if callerTenant != "" && resp.TenantId != "" && resp.TenantId != callerTenant {
			return "", fmt.Errorf("VOD asset not found")
		}
		return resp.VodHash, nil
	}
	return input, nil
}
