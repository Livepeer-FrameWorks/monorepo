package tools

import (
	"context"
	"fmt"
	"math"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func nodeCapacityResult(record *quartermasterpb.NodeCapacity) *NodeCapacityResult {
	if record == nil {
		return nil
	}
	return &NodeCapacityResult{EgressBudgetBytesPerSec: record.GetEffectiveEgressBytesPerSec(), IngressBudgetBytesPerSec: record.GetEffectiveIngressBytesPerSec(), EgressSource: strings.TrimPrefix(record.GetEgressSource().String(), "CAPACITY_SOURCE_"), IngressSource: strings.TrimPrefix(record.GetIngressSource().String(), "CAPACITY_SOURCE_"), Revision: record.GetRevision()}
}
func handleSetNodeCapacity(ctx context.Context, args SetNodeCapacityInput, sc *clients.ServiceClients, _ logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return toolError("Authentication required")
	}
	if ctxkeys.IsDemoMode(ctx) {
		return toolError("Node capacity changes are unavailable in demo mode")
	}
	if strings.TrimSpace(args.NodeID) == "" {
		return toolError("node_id is required")
	}
	for _, budget := range []*uint64{args.EgressBudgetBytesPerSec, args.IngressBudgetBytesPerSec} {
		if budget != nil && (*budget == 0 || *budget > math.MaxInt64) {
			return toolError("budgets must be positive whole bytes per second within the signed 64-bit range")
		}
	}
	result, err := sc.Commodore.SetNodeCapacity(ctx, &quartermasterpb.SetNodeCapacityRequest{NodeId: strings.TrimSpace(args.NodeID), EgressBudgetBytesPerSec: args.EgressBudgetBytesPerSec, IngressBudgetBytesPerSec: args.IngressBudgetBytesPerSec, ExpectedRevision: args.ExpectedRevision})
	if err != nil {
		return toolError(fmt.Sprintf("Failed to set node capacity: %v", err))
	}
	return infraToolSuccessJSON(nodeCapacityResult(result))
}
