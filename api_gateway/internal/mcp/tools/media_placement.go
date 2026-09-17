package tools

import (
	"context"
	"encoding/json"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Inputs share the GraphQL contract, including decimal-string revisions and
// explicit SET/CLEAR updates. Resolver authorization and owner validation run
// for every call; MCP discovery annotations do not grant permission.
func RegisterMediaPlacementTools(server *mcp.Server, resolver *resolvers.Resolver) {
	addPlacementTool(server, &mcp.Tool{Name: "get_media_placement_policy", Description: "Read saved and effective ingest/viewer policy, permissions and rollout state. Saved is not necessarily active."},
		func(ctx context.Context, input placementScopeArgs) (model.MediaPlacementPolicyResult, error) {
			return resolver.DoMediaPlacementPolicy(ctx, input.Scope)
		})
	addPlacementTool(server, &mcp.Tool{Name: "get_media_placement_options", Description: "List tenant-visible placement selectors with bounded cursor pagination."},
		func(ctx context.Context, input placementOptionsArgs) (model.MediaPlacementOptionsResult, error) {
			return resolver.DoMediaPlacementOptions(ctx, input.Scope, input.Filter, input.After, input.First)
		})
	addPlacementTool(server, &mcp.Tool{Name: "preview_media_placement", Description: "Evaluate saved or draft ingest/viewer placement for a location and protocol. This is not a reservation or admission guarantee."}, resolver.DoPreviewMediaPlacement)
	addPlacementTool(server, &mcp.Tool{Name: "review_media_placement_change", Description: "Review exact policy updates and expected revisions. Returns an expiring review token and warnings; does not apply the change."}, resolver.DoReviewMediaPlacementChange)
	addPlacementTool(server, &mcp.Tool{Name: "apply_media_placement_change", Description: "Apply the reviewed policy with its review token, exact revisions, warning acknowledgements and a stable idempotency key. On an uncertain response, recover the same key before retrying. Saved does not imply active enforcement."}, resolver.DoApplyMediaPlacementChange)
	addPlacementTool(server, &mcp.Tool{Name: "get_media_placement_change", Description: "Recover a placement change by scope and its original idempotency key, including rollout progress."},
		func(ctx context.Context, input placementChangeArgs) (model.MediaPlacementChangeResult, error) {
			return resolver.DoMediaPlacementChange(ctx, input.Scope, input.IdempotencyKey)
		})
	addPlacementTool(server, &mcp.Tool{Name: "get_cluster_media_consent", Description: "Read cluster-owner consent for ingest, serving and external media sources."},
		func(ctx context.Context, input placementClusterArgs) (model.MediaCapacityConsentResult, error) {
			return resolver.DoClusterMediaConsent(ctx, input.ClusterID)
		})
	addPlacementTool(server, &mcp.Tool{Name: "review_cluster_media_consent_change", Description: "Review cluster-owner media consent changes and warnings without applying them."}, resolver.DoReviewClusterMediaConsentChange)
	addPlacementTool(server, &mcp.Tool{Name: "apply_cluster_media_consent_change", Description: "Apply reviewed cluster-owner consent using its review token, exact revision, warning acknowledgements and stable idempotency key. Recover that key after an uncertain response."}, resolver.DoApplyClusterMediaConsentChange)
	addPlacementTool(server, &mcp.Tool{Name: "get_cluster_media_consent_change", Description: "Recover cluster-owner consent changes by cluster and original idempotency key."},
		func(ctx context.Context, input placementConsentChangeArgs) (model.MediaCapacityConsentChangeResult, error) {
			return resolver.DoClusterMediaConsentChange(ctx, input.ClusterID, input.IdempotencyKey)
		})
}

type placementScopeArgs struct {
	Scope model.MediaPlacementScopeInput `json:"scope"`
}

type placementOptionsArgs struct {
	Scope  model.MediaPlacementScopeInput     `json:"scope"`
	Filter *model.MediaPlacementOptionsFilter `json:"filter,omitempty"`
	After  *string                            `json:"after,omitempty"`
	First  *int                               `json:"first,omitempty" jsonschema:"Page size from 1 through 100"`
}

type placementChangeArgs struct {
	Scope          model.MediaPlacementScopeInput `json:"scope"`
	IdempotencyKey string                         `json:"idempotencyKey"`
}

type placementClusterArgs struct {
	ClusterID string `json:"clusterId"`
}

type placementConsentChangeArgs struct {
	ClusterID      string `json:"clusterId"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type placementToolResult struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

func addPlacementTool[In, Out any](server *mcp.Server, tool *mcp.Tool, resolve func(context.Context, In) (Out, error)) {
	addTool(server, tool,
		func(ctx context.Context, _ *mcp.CallToolRequest, input In) (*mcp.CallToolResult, any, error) {
			value, err := resolve(ctx, input)
			if err != nil {
				return toolError("Placement request failed. Recover the same idempotency key before retrying a write.")
			}
			return placementToolOutput(value)
		})
}

func placementToolOutput(value any) (*mcp.CallToolResult, any, error) {
	out := placementToolResult{Value: value}
	failed := false
	switch value.(type) {
	case *model.AuthError:
		out.Type, failed = "AuthError", true
	case *model.NotFoundError:
		out.Type, failed = "NotFoundError", true
	case *model.MediaPlacementError:
		out.Type, failed = "MediaPlacementError", true
	case *model.MediaPlacementPolicyState:
		out.Type = "MediaPlacementPolicyState"
	case *model.MediaPlacementOptionsConnection:
		out.Type = "MediaPlacementOptionsConnection"
	case *model.MediaPlacementPreview:
		out.Type = "MediaPlacementPreview"
	case *model.MediaPlacementReview:
		out.Type = "MediaPlacementReview"
	case *model.MediaPlacementChange:
		out.Type = "MediaPlacementChange"
	case *model.MediaCapacityConsent:
		out.Type = "MediaCapacityConsent"
	case *model.MediaCapacityConsentChange:
		out.Type = "MediaCapacityConsentChange"
	default:
		return toolError("Placement returned an unsupported result. Recover the same change before retrying a write.")
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return toolError("Placement result could not be encoded. Recover the same change before retrying a write.")
	}
	// Error results need explicit structured content: the SDK's success-output
	// conversion does not preserve typed recovery fields on an error result.
	return &mcp.CallToolResult{IsError: failed, StructuredContent: out, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil, nil
}
