package tools

import (
	"encoding/json"
	"fmt"
	gatewayerrors "frameworks/api_gateway/internal/errors"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func toolRPCError(err error, operation string) (*mcp.CallToolResult, any, error) {
	if used, additional, limit, ok := grpcutil.StorageLimitState(err); ok {
		quota := map[string]any{"code": grpcutil.StorageLimitReachedReason, "message": "storage limit reached", "used_bytes": used, "additional_bytes": additional, "limit_bytes": limit}
		encoded, marshalErr := json.Marshal(quota)
		if marshalErr != nil {
			return nil, nil, marshalErr
		}
		return &mcp.CallToolResult{IsError: true, StructuredContent: quota, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil, nil
	}
	if message, details, ok := gatewayerrors.PublicGRPCError(err); ok {
		details["message"] = message
		encoded, marshalErr := json.Marshal(details)
		if marshalErr != nil {
			return nil, nil, marshalErr
		}
		return &mcp.CallToolResult{IsError: true, StructuredContent: details, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil, nil
	}
	return toolError(fmt.Sprintf("%s: %s", operation, gatewayerrors.SanitizeErrorMessage(err, "request failed")))
}
