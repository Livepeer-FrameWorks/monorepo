package tools

import (
	"context"
	"fmt"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/globalid"
	"frameworks/pkg/logging"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterVODTools registers VOD upload-related MCP tools.
func RegisterVODTools(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	// create_vod_upload - Initiate multipart upload (requires balance)
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "create_vod_upload",
			Description: "Initiate a VOD asset upload. Returns presigned URLs for multipart upload. Use this to upload video files for on-demand playback.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args CreateVodUploadInput) (*mcp.CallToolResult, any, error) {
			return handleCreateVodUpload(ctx, args, resolver, checker, logger)
		},
	)

	// complete_vod_upload - Finalize upload (auth only - upload already authorized)
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "complete_vod_upload",
			Description: "Complete a VOD upload after all parts are uploaded. Triggers processing and returns the asset with playback ID.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args CompleteVodUploadInput) (*mcp.CallToolResult, any, error) {
			return handleCompleteVodUpload(ctx, args, resolver, checker, logger)
		},
	)

	// abort_vod_upload - Cancel upload (auth only)
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "abort_vod_upload",
			Description: "Abort an in-progress VOD upload. Cleans up any uploaded parts.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args AbortVodUploadInput) (*mcp.CallToolResult, any, error) {
			return handleAbortVodUpload(ctx, args, resolver, logger)
		},
	)

	// delete_vod_asset - Delete an existing VOD asset (auth only)
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "delete_vod_asset",
			Description: "Delete a VOD asset. This action cannot be undone.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args DeleteVodAssetInput) (*mcp.CallToolResult, any, error) {
			return handleDeleteVodAsset(ctx, args, resolver, checker, logger)
		},
	)
}

// CreateVodUploadInput represents input for create_vod_upload tool.
type CreateVodUploadInput struct {
	Filename    string  `json:"filename" jsonschema:"required" jsonschema_description:"Original filename of the video"`
	SizeBytes   int     `json:"size_bytes" jsonschema:"required" jsonschema_description:"File size in bytes"`
	ContentType *string `json:"content_type,omitempty" jsonschema_description:"MIME type (e.g. video/mp4)"`
	Title       *string `json:"title,omitempty" jsonschema_description:"Display title for the asset"`
	Description *string `json:"description,omitempty" jsonschema_description:"Asset description"`
}

// CreateVodUploadResult represents the result of initiating a VOD upload.
type CreateVodUploadResult struct {
	UploadID     string          `json:"upload_id"`
	PlaybackID   string          `json:"playback_id"`
	ArtifactHash string          `json:"artifact_hash"`
	ID           *string         `json:"id,omitempty"`
	PartSize     int64           `json:"part_size"`
	Parts        []VodUploadPart `json:"parts"`
	ExpiresAt    string          `json:"expires_at"`
	Message      string          `json:"message"`
}

// VodUploadPart represents a single upload part with its presigned URL.
type VodUploadPart struct {
	PartNumber   int    `json:"part_number"`
	PresignedURL string `json:"presigned_url"`
}

func handleCreateVodUpload(ctx context.Context, args CreateVodUploadInput, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
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
	if args.Filename == "" {
		return toolError("filename is required")
	}
	if args.SizeBytes <= 0 {
		return toolError("size_bytes must be positive")
	}

	// Build GraphQL input
	input := model.CreateVodUploadInput{
		Filename:    args.Filename,
		SizeBytes:   float64(args.SizeBytes),
		ContentType: args.ContentType,
		Title:       args.Title,
		Description: args.Description,
	}

	// Call resolver
	result, err := resolver.DoCreateVodUpload(ctx, input)
	if err != nil {
		logger.WithError(err).Warn("Failed to create VOD upload")
		return toolError(fmt.Sprintf("Failed to create VOD upload: %v", err))
	}

	// Handle error results
	switch r := result.(type) {
	case *model.ValidationError:
		return toolError(r.Message)
	case *model.VodUploadSession:
		parts := make([]VodUploadPart, len(r.Parts))
		for i, p := range r.Parts {
			parts[i] = VodUploadPart{
				PartNumber:   int(p.PartNumber),
				PresignedURL: p.PresignedUrl,
			}
		}

		output := CreateVodUploadResult{
			UploadID:     r.ID,
			PlaybackID:   r.PlaybackID,
			ArtifactHash: r.ArtifactHash,
			PartSize:     int64(r.PartSize),
			Parts:        parts,
			ExpiresAt:    r.ExpiresAt.Format("2006-01-02T15:04:05Z"),
			Message:      fmt.Sprintf("Upload session created. Upload %d parts using the presigned URLs, then call complete_vod_upload.", len(parts)),
		}
		if r.ArtifactHash != "" {
			relay := globalid.Encode(globalid.TypeVodAsset, r.ArtifactHash)
			output.ID = &relay
		}
		return toolSuccess(output)
	default:
		return toolError("Unexpected result type from VOD upload")
	}
}

// CompleteVodUploadInput represents input for complete_vod_upload tool.
type CompleteVodUploadInput struct {
	UploadID string               `json:"upload_id" jsonschema:"required" jsonschema_description:"Upload session ID from create_vod_upload"`
	Parts    []CompletedPartInput `json:"parts" jsonschema:"required" jsonschema_description:"Array of completed parts with ETags"`
}

// CompletedPartInput represents a completed upload part.
type CompletedPartInput struct {
	PartNumber int    `json:"part_number" jsonschema:"required" jsonschema_description:"Part number (1-based)"`
	Etag       string `json:"etag" jsonschema:"required" jsonschema_description:"ETag returned from upload"`
}

// CompleteVodUploadResult represents the result of completing a VOD upload.
type CompleteVodUploadResult struct {
	ArtifactHash string  `json:"artifact_hash"`
	ID           string  `json:"id"`
	PlaybackID   string  `json:"playback_id"`
	Status       string  `json:"status"`
	Filename     *string `json:"filename,omitempty"`
	SizeBytes    *int64  `json:"size_bytes,omitempty"`
	Message      string  `json:"message"`
}

func handleCompleteVodUpload(ctx context.Context, args CompleteVodUploadInput, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
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
	if args.UploadID == "" {
		return toolError("upload_id is required")
	}
	if len(args.Parts) == 0 {
		return toolError("parts array is required and cannot be empty")
	}

	// Build GraphQL input
	parts := make([]*model.VodUploadCompletedPart, len(args.Parts))
	for i, p := range args.Parts {
		parts[i] = &model.VodUploadCompletedPart{
			PartNumber: p.PartNumber,
			Etag:       p.Etag,
		}
	}
	input := model.CompleteVodUploadInput{
		UploadID: args.UploadID,
		Parts:    parts,
	}

	// Call resolver
	result, err := resolver.DoCompleteVodUpload(ctx, input)
	if err != nil {
		logger.WithError(err).Warn("Failed to complete VOD upload")
		return toolError(fmt.Sprintf("Failed to complete VOD upload: %v", err))
	}

	// Handle error results
	switch r := result.(type) {
	case *model.NotFoundError:
		return toolError(r.Message)
	case *model.VodAsset:
		var sizeBytes *int64
		if r.SizeBytes != nil {
			s := int64(*r.SizeBytes)
			sizeBytes = &s
		}

		output := CompleteVodUploadResult{
			ArtifactHash: r.ArtifactHash,
			ID:           r.ID,
			PlaybackID:   r.PlaybackID,
			Status:       string(r.Status),
			Filename:     r.Filename,
			SizeBytes:    sizeBytes,
			Message:      fmt.Sprintf("VOD asset created. Status: %s. Use playback_id '%s' with resolve_playback_endpoint to get viewing URLs.", r.Status, r.PlaybackID),
		}
		return toolSuccess(output)
	default:
		return toolError("Unexpected result type from VOD upload completion")
	}
}

// AbortVodUploadInput represents input for abort_vod_upload tool.
type AbortVodUploadInput struct {
	UploadID string `json:"upload_id" jsonschema:"required" jsonschema_description:"Upload session ID to abort"`
}

// AbortVodUploadResult represents the result of aborting a VOD upload.
type AbortVodUploadResult struct {
	UploadID string `json:"upload_id"`
	Aborted  bool   `json:"aborted"`
	Message  string `json:"message"`
}

func handleAbortVodUpload(ctx context.Context, args AbortVodUploadInput, resolver *resolvers.Resolver, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}

	if args.UploadID == "" {
		return toolError("upload_id is required")
	}

	// Call resolver
	result, err := resolver.DoAbortVodUpload(ctx, args.UploadID)
	if err != nil {
		logger.WithError(err).Warn("Failed to abort VOD upload")
		return toolError(fmt.Sprintf("Failed to abort VOD upload: %v", err))
	}

	// Handle error results
	switch r := result.(type) {
	case *model.NotFoundError:
		return toolError(r.Message)
	case *model.DeleteSuccess:
		output := AbortVodUploadResult{
			UploadID: args.UploadID,
			Aborted:  r.Success,
			Message:  "Upload aborted. Any uploaded parts have been cleaned up.",
		}
		return toolSuccess(output)
	default:
		return toolError("Unexpected result type from VOD upload abort")
	}
}

// DeleteVodAssetInput represents input for delete_vod_asset tool.
type DeleteVodAssetInput struct {
	ArtifactHash string `json:"artifact_hash" jsonschema:"required" jsonschema_description:"VOD artifact hash or relay ID to delete"`
}

// DeleteVodAssetResult represents the result of deleting a VOD asset.
type DeleteVodAssetResult struct {
	ID           *string `json:"id,omitempty"`
	ArtifactHash string  `json:"artifact_hash"`
	Deleted      bool    `json:"deleted"`
	Message      string  `json:"message"`
}

func handleDeleteVodAsset(ctx context.Context, args DeleteVodAssetInput, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) (*mcp.CallToolResult, any, error) {
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

	if args.ArtifactHash == "" {
		return toolError("artifact_hash is required")
	}

	var relayID *string
	if typ, _, ok := globalid.Decode(args.ArtifactHash); ok && typ == globalid.TypeVodAsset {
		relayID = &args.ArtifactHash
	}

	artifactHash, err := resolveVodIdentifier(ctx, args.ArtifactHash, resolver.Clients)
	if err != nil {
		return toolError(err.Error())
	}

	// Call resolver
	result, err := resolver.DoDeleteVodAsset(ctx, artifactHash)
	if err != nil {
		logger.WithError(err).Warn("Failed to delete VOD asset")
		return toolError(fmt.Sprintf("Failed to delete VOD asset: %v", err))
	}

	// Handle error results
	switch r := result.(type) {
	case *model.NotFoundError:
		return toolError(r.Message)
	case *model.DeleteSuccess:
		output := DeleteVodAssetResult{
			ID:           relayID,
			ArtifactHash: artifactHash,
			Deleted:      r.Success,
			Message:      "VOD asset deleted.",
		}
		return toolSuccess(output)
	default:
		return toolError("Unexpected result type from VOD asset deletion")
	}
}
