package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/clients/clientstest"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
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
