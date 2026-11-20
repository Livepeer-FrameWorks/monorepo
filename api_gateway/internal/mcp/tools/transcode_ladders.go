package tools

import (
	"context"
	"fmt"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterTranscodeLadderTools registers the tenant transcode ladder tools. A
// stream's own ladder is set with create_stream / update_stream.
func RegisterTranscodeLadderTools(server *mcp.Server, clients *clients.ServiceClients, _ *resolvers.Resolver, logger logging.Logger) {
	addTool(server,
		&mcp.Tool{
			Name:        "get_transcode_ladder_policy",
			Description: "List the transcode ladders the tenant's plan allows (with their rendition names), the plan default, the tenant's own choice, and the ladder a stream without its own gets.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return handleTranscodeLadderPolicy(ctx, "", false, clients, logger)
		},
	)
	addTool(server,
		&mcp.Tool{
			Name:        "set_transcode_ladder_policy",
			Description: "Set the transcode ladder the tenant's streams and VOD uploads inherit, by name; it must be one get_transcode_ladder_policy lists. An empty ladder returns to the plan default. Applies to live streams from their next ingest and to later VOD uploads. More renditions use more processing capacity.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args SetTranscodeLadderPolicyInput) (*mcp.CallToolResult, any, error) {
			return handleTranscodeLadderPolicy(ctx, args.Ladder, true, clients, logger)
		},
	)
}

type SetTranscodeLadderPolicyInput struct {
	Ladder string `json:"ladder" jsonschema:"Ladder name (e.g. standard, uhd); empty returns to the plan default."`
}

type TranscodeLadderToolResult struct {
	Name           string   `json:"name"`
	DisplayName    string   `json:"display_name"`
	Description    string   `json:"description"`
	RenditionNames []string `json:"rendition_names"`
}

type TranscodeLadderPolicyResult struct {
	Ladders         []TranscodeLadderToolResult `json:"ladders"`
	PlanDefault     string                      `json:"plan_default"`
	TenantLadder    string                      `json:"tenant_ladder,omitempty"`
	EffectiveLadder string                      `json:"effective_ladder"`
}

func handleTranscodeLadderPolicy(ctx context.Context, ladder string, set bool, c *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	var resp *commodorepb.TranscodeLadderPolicy
	var err error
	if set {
		resp, err = c.Commodore.SetTenantTranscodeLadder(ctx, &commodorepb.SetTenantTranscodeLadderRequest{TenantId: tenantID, TranscodeLadder: strings.TrimSpace(ladder)})
	} else {
		resp, err = c.Commodore.GetTranscodeLadderPolicy(ctx, &commodorepb.GetTranscodeLadderPolicyRequest{TenantId: tenantID})
	}
	if err != nil {
		logger.WithError(err).Warn("transcode ladder policy call failed")
		return toolError(fmt.Sprintf("transcode ladder policy: %v", err))
	}
	out := TranscodeLadderPolicyResult{
		PlanDefault: resp.GetTierDefault(), TenantLadder: resp.GetTenantLadder(), EffectiveLadder: resp.GetEffectiveLadder(),
		Ladders: make([]TranscodeLadderToolResult, 0, len(resp.GetLadders())),
	}
	for _, l := range resp.GetLadders() {
		out.Ladders = append(out.Ladders, TranscodeLadderToolResult{
			Name: l.GetName(), DisplayName: l.GetDisplayName(), Description: l.GetDescription(),
			RenditionNames: append([]string{}, l.GetRenditionNames()...),
		})
	}
	return toolSuccess(out)
}
