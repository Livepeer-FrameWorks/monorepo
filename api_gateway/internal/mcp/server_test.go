package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeSkipperAvailability struct {
	available bool
}

func (f fakeSkipperAvailability) CallTool(_ context.Context, _ string, _ json.RawMessage) (string, error) {
	return "", nil
}

func (f fakeSkipperAvailability) ToolsAvailable(_ context.Context) bool {
	return f.available
}

func TestPublicMCPWalletBootstrapDoesNotOpenAccountOrPaymentTools(t *testing.T) {
	if !isPublicMCPOperation("mcp:tools/call:request_wallet_challenge") {
		t.Error("request_wallet_challenge must be public")
	}
	for _, operation := range []string{
		"mcp:tools/call:list_linked_wallets",
		"mcp:tools/call:link_wallet",
		"mcp:tools/call:unlink_wallet",
		"mcp:tools/call:get_payment_options",
		"mcp:tools/call:submit_payment",
	} {
		if isPublicMCPOperation(operation) {
			t.Errorf("%s must require authentication", operation)
		}
	}
}

func TestAuthorizeMCPToolUsesExistingTokenScopes(t *testing.T) {
	apiToken := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	apiToken = context.WithValue(apiToken, ctxkeys.KeyPermissions, []string{"streams:read"})
	jwt := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")

	tests := []struct {
		name     string
		ctx      context.Context
		tool     string
		wantDeny bool
	}{
		{name: "matching API scope", ctx: apiToken, tool: "list_stream_keys"},
		{name: "missing API scope", ctx: apiToken, tool: "create_stream", wantDeny: true},
		{name: "interactive user", ctx: jwt, tool: "create_stream"},
		{name: "anonymous public tool", ctx: context.Background(), tool: "browse_marketplace"},
		{name: "scoped token cannot inherit public tool access", ctx: apiToken, tool: "browse_marketplace", wantDeny: true},
		{name: "anonymous private tool", ctx: context.Background(), tool: "get_tenant_settings", wantDeny: true},
		{name: "unclassified tool fails closed", ctx: jwt, tool: "not_registered", wantDeny: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := authorizeMCPTool(tc.ctx, tc.tool)
			if (err != nil) != tc.wantDeny {
				t.Fatalf("authorizeMCPTool(%q) error = %v, wantDeny=%t", tc.tool, err, tc.wantDeny)
			}
		})
	}
}

func TestAuthorizeMCPHighRiskToolRequiresExplicitAgentGrant(t *testing.T) {
	base := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	withoutGrant := context.WithValue(base, ctxkeys.KeyPermissions, []string{"streams:write"})
	withGrant := context.WithValue(base, ctxkeys.KeyPermissions, []string{"streams:write", "mcp:high-risk"})
	if err := authorizeMCPTool(withoutGrant, "delete_stream"); err == nil {
		t.Fatal("high-risk API-token call succeeded without mcp:high-risk")
	}
	if err := authorizeMCPTool(withGrant, "delete_stream"); err != nil {
		t.Fatalf("pre-authorized high-risk API-token call denied: %v", err)
	}
}

func TestFilterToolsByPolicyOnlyPublishesCallableTools(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"streams:read"})
	result := &mcp.ListToolsResult{Tools: []*mcp.Tool{
		{Name: "list_stream_keys"},
		{Name: "create_stream"},
		{Name: "browse_marketplace"},
		{Name: "not_registered"},
	}}

	filtered := filterToolsByPolicy(ctx, result).(*mcp.ListToolsResult)
	if len(filtered.Tools) != 1 || filtered.Tools[0].Name != "list_stream_keys" {
		t.Fatalf("filtered tools = %#v", filtered.Tools)
	}
}

func TestValidMCPOrigin(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		origin  string
		allowed func(string) bool
		want    bool
	}{
		{name: "non-browser client", host: "api.example", want: true},
		{name: "same origin fallback", host: "api.example", origin: "https://api.example", want: true},
		{name: "cross origin fallback", host: "api.example", origin: "https://evil.example"},
		{name: "configured origin", host: "api.example", origin: "https://app.example", allowed: func(value string) bool { return value == "https://app.example" }, want: true},
		{name: "malformed origin", host: "api.example", origin: "://bad"},
		{name: "configured callback cannot allow origin path", host: "api.example", origin: "https://evil.example/path", allowed: func(string) bool { return true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://"+tc.host+"/mcp", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Origin", tc.origin)
			if got := validMCPOrigin(req, tc.allowed); got != tc.want {
				t.Fatalf("validMCPOrigin() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestAuthorizeMCPResourceUsesExistingTokenScopes(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"streams:read"})

	for _, uri := range []string{"streams://list", "streams://stream-1", "vod://asset-1"} {
		if err := authorizeMCPResource(ctx, uri); err != nil {
			t.Errorf("authorizeMCPResource(%q): %v", uri, err)
		}
	}
	for _, uri := range []string{"account://status", "billing://balance", "support://conversations", "unknown://resource"} {
		if err := authorizeMCPResource(ctx, uri); err == nil {
			t.Errorf("authorizeMCPResource(%q) unexpectedly succeeded", uri)
		}
	}
	if err := authorizeMCPResource(context.Background(), "account://status"); err != nil {
		t.Fatalf("anonymous public resource denied: %v", err)
	}
}

func TestFilterMCPResourcesByPolicy(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"billing:read"})
	resources := &mcp.ListResourcesResult{Resources: []*mcp.Resource{
		{URI: "billing://balance"},
		{URI: "streams://list"},
		{URI: "account://status"},
		{URI: "unknown://resource"},
	}}
	filtered := filterResourcesByPolicy(ctx, resources).(*mcp.ListResourcesResult)
	if len(filtered.Resources) != 1 || filtered.Resources[0].URI != "billing://balance" {
		t.Fatalf("filtered resources = %#v", filtered.Resources)
	}

	templates := &mcp.ListResourceTemplatesResult{ResourceTemplates: []*mcp.ResourceTemplate{
		{URITemplate: "billing://invoices/{invoice_id}"},
		{URITemplate: "streams://{id}"},
	}}
	filteredTemplates := filterResourceTemplatesByPolicy(ctx, templates).(*mcp.ListResourceTemplatesResult)
	if len(filteredTemplates.ResourceTemplates) != 1 || filteredTemplates.ResourceTemplates[0].URITemplate != "billing://invoices/{invoice_id}" {
		t.Fatalf("filtered resource templates = %#v", filteredTemplates.ResourceTemplates)
	}
}

func TestMCPResourceResultLimitReturnsProtocolError(t *testing.T) {
	err := mcpResultTooLargeError()
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32016 || len(rpcErr.Data) == 0 {
		t.Fatalf("result limit error = %#v", err)
	}
}

func TestEnforceMCPResultLimitReturnsValidStructuredError(t *testing.T) {
	oversized := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", maxMCPResultBytes)}}}
	result := enforceMCPResultLimit("test_tool", oversized).(*mcp.CallToolResult)
	if !result.IsError {
		t.Fatal("oversized result must be a tool error")
	}
	if result.StructuredContent == nil || len(result.Content) != 1 {
		t.Fatalf("oversized result is not structured: %#v", result)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("oversized error text is not JSON: %v", err)
	}
	if payload["code"] != "RESULT_TOO_LARGE" {
		t.Fatalf("code = %v", payload["code"])
	}
}

func TestFilterSkipperToolsWhenUnavailable(t *testing.T) {
	result := &mcp.ListToolsResult{
		Tools: []*mcp.Tool{
			{Name: "search_knowledge"},
			{Name: "search_web"},
			{Name: "get_stream"},
		},
	}
	filtered := filterSkipperTools(context.Background(), result, fakeSkipperAvailability{available: false})
	listResult, ok := filtered.(*mcp.ListToolsResult)
	if !ok {
		t.Fatalf("expected ListToolsResult, got %T", filtered)
	}
	if len(listResult.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(listResult.Tools))
	}
	if listResult.Tools[0].Name != "get_stream" {
		t.Fatalf("unexpected remaining tool: %s", listResult.Tools[0].Name)
	}
}

func TestFilterSkipperToolsWhenAvailable(t *testing.T) {
	result := &mcp.ListToolsResult{
		Tools: []*mcp.Tool{
			{Name: "search_knowledge"},
			{Name: "search_web"},
			{Name: "get_stream"},
		},
	}
	filtered := filterSkipperTools(context.Background(), result, fakeSkipperAvailability{available: true})
	listResult, ok := filtered.(*mcp.ListToolsResult)
	if !ok {
		t.Fatalf("expected ListToolsResult, got %T", filtered)
	}
	if len(listResult.Tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(listResult.Tools))
	}
}

func TestGetMcpArgString(t *testing.T) {
	tests := []struct {
		name   string
		params mcp.Params
		keys   []string
		want   string
	}{
		{
			name: "returns first matching string key",
			params: &mcp.CallToolParamsRaw{
				Name:      "update_stream",
				Arguments: json.RawMessage(`{"streamId":"s1","stream_id":"s2"}`),
			},
			keys: []string{"stream_id", "streamId"},
			want: "s2",
		},
		{
			name: "returns empty on malformed json",
			params: &mcp.CallToolParamsRaw{
				Name:      "update_stream",
				Arguments: json.RawMessage(`{"stream_id":`),
			},
			keys: []string{"stream_id"},
			want: "",
		},
		{
			name: "returns empty on non-string value",
			params: &mcp.CallToolParamsRaw{
				Name:      "update_stream",
				Arguments: json.RawMessage(`{"stream_id":123}`),
			},
			keys: []string{"stream_id"},
			want: "",
		},
		{
			name: "returns empty on missing args",
			params: &mcp.CallToolParamsRaw{
				Name: "update_stream",
			},
			keys: []string{"stream_id"},
			want: "",
		},
		{
			name:   "returns empty for wrong params type",
			params: &mcp.ListToolsParams{},
			keys:   []string{"stream_id"},
			want:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := getMcpArgString(tc.params, tc.keys...)
			if got != tc.want {
				t.Fatalf("getMcpArgString() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractPlaybackContentID(t *testing.T) {
	tests := []struct {
		name   string
		params mcp.Params
		want   string
	}{
		{
			name: "extracts content_id",
			params: &mcp.CallToolParamsRaw{
				Name:      "resolve_playback_endpoint",
				Arguments: json.RawMessage(`{"content_id":"abc123"}`),
			},
			want: "abc123",
		},
		{
			name: "extracts camelCase fallback",
			params: &mcp.CallToolParamsRaw{
				Name:      "resolve_playback_endpoint",
				Arguments: json.RawMessage(`{"contentId":"abc456"}`),
			},
			want: "abc456",
		},
		{
			name: "returns empty for wrong tool",
			params: &mcp.CallToolParamsRaw{
				Name:      "update_stream",
				Arguments: json.RawMessage(`{"content_id":"abc123"}`),
			},
			want: "",
		},
		{
			name: "returns empty on malformed json",
			params: &mcp.CallToolParamsRaw{
				Name:      "resolve_playback_endpoint",
				Arguments: json.RawMessage(`{"content_id":`),
			},
			want: "",
		},
		{
			name: "returns empty on non-string value",
			params: &mcp.CallToolParamsRaw{
				Name:      "resolve_playback_endpoint",
				Arguments: json.RawMessage(`{"content_id":123}`),
			},
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractPlaybackContentID(tc.params)
			if got != tc.want {
				t.Fatalf("extractPlaybackContentID() = %q, want %q", got, tc.want)
			}
		})
	}
}

// mcpAccessIdentity is the invariant that prevents an anonymous playback resolve
// from being throttled on (and exhausting) the stream owner's rate-limit bucket:
// billing follows the owner while the rate-limit identity stays the caller.
func TestMCPAccessIdentity(t *testing.T) {
	// Anonymous viewer resolving an owned stream: bill the owner, rate-limit the
	// caller — the empty caller identity means per-IP public bucket downstream.
	if billing, rl := mcpAccessIdentity("", "owner-tenant"); billing != "owner-tenant" || rl == nil || *rl != "" {
		t.Errorf("anon caller: got billing=%q rl=%v, want billing=owner-tenant rl=&\"\"", billing, ptrStr(rl))
	}

	// Authenticated non-owner viewer: bill the owner, rate-limit the caller's tenant.
	if billing, rl := mcpAccessIdentity("tenant-b", "tenant-a"); billing != "tenant-a" || rl == nil || *rl != "tenant-b" {
		t.Errorf("authed non-owner: got billing=%q rl=%v, want billing=tenant-a rl=&\"tenant-b\"", billing, ptrStr(rl))
	}

	// No owner resolved: caller stays the billing tenant, no decoupling.
	if billing, rl := mcpAccessIdentity("tenant-c", ""); billing != "tenant-c" || rl != nil {
		t.Errorf("no owner: got billing=%q rl=%v, want billing=tenant-c rl=nil", billing, ptrStr(rl))
	}
}

func ptrStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return "&" + *p
}

func TestPaidMCPMutationResultIsReplayedWithoutExecutingAgain(t *testing.T) {
	var mu sync.Mutex
	var fingerprint string
	var stored []byte
	var executions int
	purser := &clientstest.FakePurser{
		ClaimX402MutationResultFn: func(_ context.Context, req *purserpb.ClaimX402MutationResultRequest) (*purserpb.ClaimX402MutationResultResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			if fingerprint == "" {
				fingerprint = req.GetRequestFingerprint()
				return &purserpb.ClaimX402MutationResultResponse{State: "claimed"}, nil
			}
			return &purserpb.ClaimX402MutationResultResponse{State: "completed", Result: append([]byte(nil), stored...)}, nil
		},
		CompleteX402MutationResultFn: func(_ context.Context, req *purserpb.CompleteX402MutationResultRequest) (*purserpb.CompleteX402MutationResultResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			stored = append([]byte(nil), req.GetResult()...)
			return &purserpb.CompleteX402MutationResultResponse{Completed: true}, nil
		},
	}
	server := &Server{serviceClients: &clients.ServiceClients{Purser: purser}, logger: clientstest.DiscardLogger()}
	paymentJSON := `{"x402Version":2,"scheme":"exact","network":"eip155:8453","accepted":{"scheme":"exact","network":"eip155:8453","asset":"0x0000000000000000000000000000000000000001","amount":"5000000","payTo":"0x0000000000000000000000000000000000000002","maxTimeoutSeconds":60,"extra":{"frameworks":{"quoteId":"22222222-2222-2222-2222-222222222222"}}},"payload":{"signature":"0x00","authorization":{"from":"0x0000000000000000000000000000000000000003","to":"0x0000000000000000000000000000000000000002","value":"5000000","validAfter":"0","validBefore":"9999999999","nonce":"0x0000000000000000000000000000000000000000000000000000000000000001"}}}`
	payment := base64.StdEncoding.EncodeToString([]byte(paymentJSON))
	params := &mcp.CallToolParamsRaw{
		Meta: mcp.Meta{"idempotencyKey": "mutation-123"},
		Name: "create_stream", Arguments: json.RawMessage(`{"name":"camera"}`),
	}
	req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{
		Params: params,
		Extra:  &mcp.RequestExtra{Header: http.Header{"Idempotency-Key": []string{"mutation-123"}}},
	}
	next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		executions++
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"id":"stream-1"}`}}}, nil
	}
	first, err := server.executePaidMCPMutation(context.Background(), "tools/call", "mcp:tools/call:create_stream", "11111111-1111-1111-1111-111111111111", payment, req, next)
	if err != nil {
		t.Fatal(err)
	}
	second, err := server.executePaidMCPMutation(context.Background(), "tools/call", "mcp:tools/call:create_stream", "11111111-1111-1111-1111-111111111111", payment, req, next)
	if err != nil {
		t.Fatal(err)
	}
	if executions != 1 {
		t.Fatalf("MCP mutation executed %d times, want exactly once", executions)
	}
	if mcpResultTextBytes(first) != mcpResultTextBytes(second) || mcpTextResult(first.(*mcp.CallToolResult)) != mcpTextResult(second.(*mcp.CallToolResult)) {
		t.Fatalf("replayed MCP result differs: first=%v second=%v", first, second)
	}
}

func TestPaidMCPMutationInProgressDoesNotExecute(t *testing.T) {
	purser := &clientstest.FakePurser{
		ClaimX402MutationResultFn: func(context.Context, *purserpb.ClaimX402MutationResultRequest) (*purserpb.ClaimX402MutationResultResponse, error) {
			return &purserpb.ClaimX402MutationResultResponse{State: "in_progress"}, nil
		},
	}
	server := &Server{serviceClients: &clients.ServiceClients{Purser: purser}, logger: clientstest.DiscardLogger()}
	paymentJSON := `{"x402Version":2,"scheme":"exact","network":"eip155:8453","accepted":{"scheme":"exact","network":"eip155:8453","asset":"0x0000000000000000000000000000000000000001","amount":"5000000","payTo":"0x0000000000000000000000000000000000000002","maxTimeoutSeconds":60,"extra":{"frameworks":{"quoteId":"22222222-2222-2222-2222-222222222222"}}},"payload":{"signature":"0x00","authorization":{"from":"0x0000000000000000000000000000000000000003","to":"0x0000000000000000000000000000000000000002","value":"5000000","validAfter":"0","validBefore":"9999999999","nonce":"0x0000000000000000000000000000000000000000000000000000000000000001"}}}`
	payment := base64.StdEncoding.EncodeToString([]byte(paymentJSON))
	req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{
		Params: &mcp.CallToolParamsRaw{
			Meta: mcp.Meta{"idempotencyKey": "mutation-123"},
			Name: "create_stream", Arguments: json.RawMessage(`{"name":"camera"}`),
		},
		Extra: &mcp.RequestExtra{Header: http.Header{"Idempotency-Key": []string{"mutation-123"}}},
	}
	executions := 0
	next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		executions++
		return &mcp.CallToolResult{}, nil
	}

	_, err := server.executePaidMCPMutation(context.Background(), "tools/call", "mcp:tools/call:create_stream", "11111111-1111-1111-1111-111111111111", payment, req, next)
	if err == nil || !strings.Contains(err.Error(), "paid mutation in progress") {
		t.Fatalf("error=%v, want paid mutation in progress", err)
	}
	if executions != 0 {
		t.Fatalf("MCP mutation executed %d times while the durable claim was in progress", executions)
	}
}

func TestPaidMCPMutationOversizedResultBecomesReplayableTerminalEnvelope(t *testing.T) {
	var stored []byte
	claimed := false
	executions := 0
	purser := &clientstest.FakePurser{
		ClaimX402MutationResultFn: func(context.Context, *purserpb.ClaimX402MutationResultRequest) (*purserpb.ClaimX402MutationResultResponse, error) {
			if !claimed {
				claimed = true
				return &purserpb.ClaimX402MutationResultResponse{State: "claimed"}, nil
			}
			return &purserpb.ClaimX402MutationResultResponse{State: "completed", Result: append([]byte(nil), stored...)}, nil
		},
		CompleteX402MutationResultFn: func(_ context.Context, req *purserpb.CompleteX402MutationResultRequest) (*purserpb.CompleteX402MutationResultResponse, error) {
			stored = append([]byte(nil), req.GetResult()...)
			return &purserpb.CompleteX402MutationResultResponse{Completed: true}, nil
		},
	}
	server := &Server{serviceClients: &clients.ServiceClients{Purser: purser}, logger: clientstest.DiscardLogger()}
	paymentJSON := `{"x402Version":2,"scheme":"exact","network":"eip155:8453","accepted":{"scheme":"exact","network":"eip155:8453","asset":"0x0000000000000000000000000000000000000001","amount":"5000000","payTo":"0x0000000000000000000000000000000000000002","maxTimeoutSeconds":60,"extra":{"frameworks":{"quoteId":"22222222-2222-2222-2222-222222222222"}}},"payload":{"signature":"0x00","authorization":{"from":"0x0000000000000000000000000000000000000003","to":"0x0000000000000000000000000000000000000002","value":"5000000","validAfter":"0","validBefore":"9999999999","nonce":"0x0000000000000000000000000000000000000000000000000000000000000001"}}}`
	payment := base64.StdEncoding.EncodeToString([]byte(paymentJSON))
	req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{
		Params: &mcp.CallToolParamsRaw{
			Meta: mcp.Meta{"idempotencyKey": "mutation-oversized"},
			Name: "create_stream", Arguments: json.RawMessage(`{"name":"camera"}`),
		},
		Extra: &mcp.RequestExtra{Header: http.Header{"Idempotency-Key": []string{"mutation-oversized"}}},
	}
	next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		executions++
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", maxMCPResultBytes+1)}}}, nil
	}
	first, err := server.executePaidMCPMutation(context.Background(), "tools/call", "mcp:tools/call:create_stream", "11111111-1111-1111-1111-111111111111", payment, req, next)
	if err != nil || first == nil {
		t.Fatalf("first result=%v error=%v", first, err)
	}
	if len(stored) >= maxMCPResultBytes || !bytes.Contains(stored, []byte("exceeded the durable replay limit")) {
		t.Fatalf("stored terminal envelope length=%d body=%q", len(stored), stored)
	}
	second, err := server.executePaidMCPMutation(context.Background(), "tools/call", "mcp:tools/call:create_stream", "11111111-1111-1111-1111-111111111111", payment, req, next)
	if err != nil || executions != 1 {
		t.Fatalf("replay result=%v error=%v executions=%d", second, err, executions)
	}
	if !strings.Contains(mcpTextResult(second.(*mcp.CallToolResult)), "exceeded the durable replay limit") {
		t.Fatalf("unexpected replay result: %v", second)
	}
}
