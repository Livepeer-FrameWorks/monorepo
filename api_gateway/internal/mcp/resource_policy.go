package mcp

import (
	"context"
	"encoding/json"
	"strings"

	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/accesspolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpResourcePolicy struct {
	Scope       string
	AccessClass accesspolicy.Class
	Public      bool
}

var mcpResourcePrefixes = []struct {
	Prefix string
	Scope  string
}{
	{Prefix: "analytics://", Scope: "analytics:read"},
	{Prefix: "billing://invoices/", Scope: "billing:read"},
	{Prefix: "billing://payments/", Scope: "billing:read"},
	{Prefix: "billing://documents/", Scope: "billing:read"},
	{Prefix: "clusters://", Scope: "infrastructure:read"},
	{Prefix: "nodes://", Scope: "infrastructure:read"},
	{Prefix: "streams://", Scope: "streams:read"},
	{Prefix: "support://conversations/", Scope: "support:read"},
	{Prefix: "vod://", Scope: "streams:read"},
}

func resourcePolicyForURI(uri string) (mcpResourcePolicy, bool) {
	switch uri {
	case "account://status":
		return mcpResourcePolicy{Scope: "account:read", AccessClass: accesspolicy.Read, Public: true}, true
	case "billing://pricing":
		return mcpResourcePolicy{Scope: "billing:read", AccessClass: accesspolicy.Read, Public: true}, true
	case "clusters://marketplace":
		return mcpResourcePolicy{Scope: "infrastructure:read", AccessClass: accesspolicy.Read, Public: true}, true
	case "billing://balance", "billing://transactions", "billing://invoices", "billing://payments", "billing://documents":
		return mcpResourcePolicy{Scope: "billing:read", AccessClass: accesspolicy.Read}, true
	case "clusters://list":
		return mcpResourcePolicy{Scope: "infrastructure:read", AccessClass: accesspolicy.Read}, true
	case "knowledge://sources":
		return mcpResourcePolicy{Scope: "consultant:use", AccessClass: accesspolicy.Read}, true
	case "schema://catalog":
		return mcpResourcePolicy{Scope: "developer:read", AccessClass: accesspolicy.Read}, true
	case "streams://list", "vod://list":
		return mcpResourcePolicy{Scope: "streams:read", AccessClass: accesspolicy.Read}, true
	case "support://conversations":
		return mcpResourcePolicy{Scope: "support:read", AccessClass: accesspolicy.Read}, true
	}

	for _, candidate := range mcpResourcePrefixes {
		if strings.HasPrefix(uri, candidate.Prefix) && len(uri) > len(candidate.Prefix) {
			return mcpResourcePolicy{Scope: candidate.Scope, AccessClass: accesspolicy.Read}, true
		}
	}

	return mcpResourcePolicy{}, false
}

func authorizeMCPResource(ctx context.Context, uri string) error {
	policy, ok := resourcePolicyForURI(uri)
	if !ok {
		mcpResourceDenialsTotal.WithLabelValues("unknown", "policy").Inc()
		return &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "resource security policy missing"}
	}
	if policy.Public && ctxkeys.GetAuthType(ctx) != "api_token" {
		return nil
	}
	if err := middleware.RequirePermission(ctx, policy.Scope); err != nil {
		mcpResourceDenialsTotal.WithLabelValues(policy.Scope, "scope").Inc()
		data, marshalErr := json.Marshal(map[string]any{"code": "INSUFFICIENT_SCOPE", "required_scope": policy.Scope})
		if marshalErr != nil {
			return &jsonrpc.Error{Code: -32003, Message: "insufficient permissions"}
		}
		return &jsonrpc.Error{Code: -32003, Message: "insufficient permissions", Data: data}
	}
	return nil
}

func canReadMCPResource(ctx context.Context, uri string) bool {
	policy, ok := resourcePolicyForURI(uri)
	if !ok {
		return false
	}
	return (policy.Public && ctxkeys.GetAuthType(ctx) != "api_token") || middleware.HasPermission(ctx, policy.Scope)
}

func filterResourcesByPolicy(ctx context.Context, result mcp.Result) mcp.Result {
	list, ok := result.(*mcp.ListResourcesResult)
	if !ok || list == nil {
		return result
	}
	filtered := make([]*mcp.Resource, 0, len(list.Resources))
	for _, resource := range list.Resources {
		if resource != nil && canReadMCPResource(ctx, resource.URI) {
			filtered = append(filtered, resource)
		}
	}
	list.Resources = filtered
	return list
}

func filterResourceTemplatesByPolicy(ctx context.Context, result mcp.Result) mcp.Result {
	list, ok := result.(*mcp.ListResourceTemplatesResult)
	if !ok || list == nil {
		return result
	}
	filtered := make([]*mcp.ResourceTemplate, 0, len(list.ResourceTemplates))
	for _, resource := range list.ResourceTemplates {
		if resource != nil && canReadMCPResource(ctx, resource.URITemplate) {
			filtered = append(filtered, resource)
		}
	}
	list.ResourceTemplates = filtered
	return list
}
