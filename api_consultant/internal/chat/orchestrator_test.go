package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"frameworks/api_consultant/internal/knowledge"
	"frameworks/api_consultant/internal/skipper"
	"frameworks/pkg/llm"
)

type fakeKnowledgeStore struct {
	results   map[string][]knowledge.Chunk
	errTenant string
	calls     []string
}

func (f *fakeKnowledgeStore) Search(ctx context.Context, tenantID string, _ []float32, _ int) ([]knowledge.Chunk, error) {
	return f.HybridSearch(ctx, tenantID, nil, "", 0)
}

func (f *fakeKnowledgeStore) HybridSearch(_ context.Context, tenantID string, _ []float32, _ string, _ int) ([]knowledge.Chunk, error) {
	f.calls = append(f.calls, tenantID)
	if tenantID == f.errTenant {
		return nil, context.DeadlineExceeded
	}
	return f.results[tenantID], nil
}

type fakeQueryEmbedder struct {
	calls int
}

func (f *fakeQueryEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	f.calls++
	return []float32{0.1, 0.2}, nil
}

func TestSearchKnowledgeFallsBackOnTenantFailure(t *testing.T) {
	store := &fakeKnowledgeStore{
		errTenant: "tenant-a",
		results: map[string][]knowledge.Chunk{
			"global": {
				{
					SourceURL:   "https://docs.example.com",
					SourceTitle: "Global Docs",
					Text:        "Latency tuning guide",
					Similarity:  0.98,
				},
			},
		},
	}
	embedder := &fakeQueryEmbedder{}

	orchestrator := &Orchestrator{
		knowledge:      store,
		embedder:       embedder,
		searchLimit:    2,
		globalTenantID: "global",
	}

	ctx := skipper.WithTenantID(context.Background(), "tenant-a")
	outcome, err := orchestrator.searchKnowledge(ctx, `{"query":"latency","tenant_scope":"all"}`)
	if err != nil {
		t.Fatalf("expected fallback success, got error: %v", err)
	}
	if embedder.calls != 1 {
		t.Fatalf("expected embedder called once, got %d", embedder.calls)
	}
	if len(outcome.Sources) != 1 || outcome.Sources[0].URL != "https://docs.example.com" {
		t.Fatalf("expected global source, got %+v", outcome.Sources)
	}
	if !strings.Contains(outcome.Content, "Knowledge base results") {
		t.Fatalf("expected knowledge context, got %q", outcome.Content)
	}
}

type countingGateway struct {
	tools []llm.Tool
	calls int
}

func (g *countingGateway) AvailableTools() []llm.Tool { return g.tools }
func (g *countingGateway) HasTool(name string) bool {
	for _, tool := range g.tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}
func (g *countingGateway) CallTool(_ context.Context, _ string, _ json.RawMessage) (string, error) {
	g.calls++
	return "called", nil
}

func TestDocsModeBlocksMutatingToolExecution(t *testing.T) {
	gateway := &countingGateway{
		tools: []llm.Tool{
			{Name: "delete_stream"},
		},
	}
	orchestrator := &Orchestrator{gateway: gateway}

	ctx := skipper.WithMode(context.Background(), "docs")
	outcome, err := orchestrator.executeTool(ctx, llm.ToolCall{
		Name:      "delete_stream",
		Arguments: "{}",
	})
	if err != nil {
		t.Fatalf("executeTool: %v", err)
	}
	if gateway.calls != 0 {
		t.Fatalf("expected gateway to be skipped, got %d calls", gateway.calls)
	}
	if !strings.Contains(outcome.Content, "not available in documentation mode") {
		t.Fatalf("expected docs mode denial message, got %q", outcome.Content)
	}
}

func hasTool(tools []llm.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func TestMergeToolCalls_DeduplicatesByID(t *testing.T) {
	existing := []llm.ToolCall{
		{ID: "call-1", Name: "search_knowledge", Arguments: `{"query":"stream `},
	}
	incoming := []llm.ToolCall{
		{ID: "call-1", Name: "search_knowledge", Arguments: `{"query":"stream latency"}`},
	}

	result := mergeToolCalls(existing, incoming)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result))
	}
	if result[0].Arguments != `{"query":"stream latency"}` {
		t.Fatalf("expected merged arguments, got %q", result[0].Arguments)
	}
}

func TestMergeToolCalls_PreservesOrderWithOutOfOrderChunks(t *testing.T) {
	existing := []llm.ToolCall{
		{ID: "call-2", Name: "get_stream", Arguments: `{"stream_id":"a"`},
	}
	incoming := []llm.ToolCall{
		{ID: "call-1", Name: "search_knowledge", Arguments: `{"query":"srt"}`},
		{ID: "call-2", Name: "get_stream", Arguments: `{"stream_id":"a","tenant_id":"t"}`},
	}

	result := mergeToolCalls(existing, incoming)
	if len(result) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(result))
	}
	if result[0].ID != "call-2" || result[1].ID != "call-1" {
		t.Fatalf("expected order preserved by first-seen ID, got %q then %q", result[0].ID, result[1].ID)
	}
	if result[0].Arguments != `{"stream_id":"a","tenant_id":"t"}` {
		t.Fatalf("expected merged arguments for call-2, got %q", result[0].Arguments)
	}
}

func TestParseDiagnosticMetrics(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    map[string]float64
	}{
		{
			name:    "top-level keys",
			content: `{"avg_buffer_health":2.5,"avg_fps":28}`,
			want:    map[string]float64{"avg_buffer_health": 2.5, "avg_fps": 28},
		},
		{
			name:    "nested metrics object (gateway DiagnosticResult)",
			content: `{"status":"warning","metrics":{"avg_buffer_health":0.6,"avg_packet_loss_rate":0.03,"avg_bandwidth_out_bps":5000000},"analysis":"Buffer health degraded."}`,
			want: map[string]float64{
				"avg_buffer_health": 0.6,
				"avg_packet_loss":   0.03,
				"avg_bandwidth_out": 5000000,
			},
		},
		{
			name:    "mixed: top-level and nested",
			content: `{"bitrate":5000000,"metrics":{"avg_buffer_health":2.0}}`,
			want: map[string]float64{
				"avg_bitrate":       5000000,
				"avg_buffer_health": 2.0,
			},
		},
		{
			name:    "invalid JSON",
			content: `not json at all`,
			want:    map[string]float64{},
		},
		{
			name:    "correlator-relevant metrics (issue count, bandwidth, sessions)",
			content: `{"metrics":{"issue_count":5,"avg_bandwidth_out_bps":8000000,"total_issues":3,"avg_bitrate_kbps":4500,"avg_fps":29}}`,
			want: map[string]float64{
				"total_issue_count": 5,
				"avg_bandwidth_out": 8000000,
				"avg_bitrate":       4500,
				"avg_fps":           29,
			},
		},
		{
			name:    "no known metrics",
			content: `{"status":"ok","analysis":"all good"}`,
			want:    map[string]float64{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDiagnosticMetrics(tt.content)
			for k, wantV := range tt.want {
				gotV, ok := got[k]
				if !ok {
					t.Errorf("missing key %q", k)
					continue
				}
				if gotV != wantV {
					t.Errorf("%s = %v, want %v", k, gotV, wantV)
				}
			}
			for k := range got {
				if _, ok := tt.want[k]; !ok {
					t.Errorf("unexpected key %q = %v", k, got[k])
				}
			}
		})
	}
}

func TestModeAllowsTool(t *testing.T) {
	o := &Orchestrator{}

	tests := []struct {
		mode    string
		tool    string
		allowed bool
	}{
		// docs mode: allowlist-based
		{"docs", "search_knowledge", true},
		{"docs", "search_web", true},
		{"docs", "execute_query", true},
		{"docs", "search_support_history", true},
		{"docs", "diagnose_rebuffering", true},
		{"docs", "create_stream", false},
		{"docs", "delete_stream", false},
		{"docs", "start_dvr", false},

		// spoke mode: blocklist-based (mutations blocked)
		{"spoke", "search_knowledge", true},
		{"spoke", "search_web", true},
		{"spoke", "diagnose_rebuffering", true},
		{"spoke", "get_stream", true},
		{"spoke", "execute_query", true},
		{"spoke", "create_stream", false},
		{"spoke", "delete_stream", false},
		{"spoke", "update_stream", false},
		{"spoke", "refresh_stream_key", false},
		{"spoke", "create_clip", false},
		{"spoke", "delete_clip", false},
		{"spoke", "start_dvr", false},
		{"spoke", "stop_dvr", false},
		{"spoke", "create_vod_upload", false},
		{"spoke", "complete_vod_upload", false},
		{"spoke", "abort_vod_upload", false},
		{"spoke", "delete_vod_asset", false},
		{"spoke", "topup_balance", false},
		{"spoke", "submit_payment", false},
		{"spoke", "update_billing_details", false},

		// heartbeat mode: only search_knowledge
		{"heartbeat", "search_knowledge", true},
		{"heartbeat", "search_web", false},
		{"heartbeat", "create_stream", false},

		// default (empty): everything allowed
		{"", "create_stream", true},
		{"", "search_knowledge", true},
	}
	for _, tt := range tests {
		name := tt.mode + "/" + tt.tool
		if tt.mode == "" {
			name = "default/" + tt.tool
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if tt.mode != "" {
				ctx = skipper.WithMode(ctx, tt.mode)
			}
			got := o.modeAllowsTool(ctx, tt.tool)
			if got != tt.allowed {
				t.Errorf("modeAllowsTool(%q, %q) = %v, want %v", tt.mode, tt.tool, got, tt.allowed)
			}
		})
	}
}

func TestIsMutationQuery(t *testing.T) {
	tests := []struct {
		name string
		args string
		want bool
	}{
		{"mutation keyword", `{"query":"mutation { createStream(input: {}) { id } }"}`, true},
		{"mutation uppercase", `{"query":"MUTATION { foo }"}`, true},
		{"mutation with whitespace", `{"query":"  mutation { bar }"}`, true},
		{"comment then mutation", `{"query":"# bypass\nmutation { createStream { id } }"}`, true},
		{"multiple comments then mutation", `{"query":"# line1\n# line2\nmutation { foo }"}`, true},
		{"blank lines then mutation", `{"query":"\n\n  # note\n  mutation { bar }"}`, true},
		{"plain query (implicit)", `{"query":"{ streams { id } }"}`, false},
		{"query keyword", `{"query":"query { streams { id } }"}`, false},
		{"comment only", `{"query":"# just a comment"}`, false},
		{"query then trailing comment", `{"query":"query { streams { id } }\n# trailing"}`, false},
		{"empty query", `{"query":""}`, false},
		{"missing query field", `{"limit":10}`, false},
		{"invalid JSON", `not json`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isMutationQuery(tt.args)
			if got != tt.want {
				t.Errorf("isMutationQuery(%q) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestExecuteQueryMutationBlocked(t *testing.T) {
	gateway := &countingGateway{
		tools: []llm.Tool{{Name: "execute_query"}},
	}
	o := &Orchestrator{gateway: gateway}

	mutationArgs := `{"query":"mutation { createStream(input: {}) { id } }"}`
	queryArgs := `{"query":"{ streams { id } }"}`

	// Docs mode: mutation blocked
	ctx := skipper.WithMode(context.Background(), "docs")
	outcome, err := o.callGatewayTool(ctx, llm.ToolCall{Name: "execute_query", Arguments: mutationArgs})
	if err != nil {
		t.Fatalf("callGatewayTool: %v", err)
	}
	if gateway.calls != 0 {
		t.Fatal("expected mutation to be blocked, but gateway was called")
	}
	if !strings.Contains(outcome.Content, "Mutations are not allowed") {
		t.Fatalf("expected mutation block message, got %q", outcome.Content)
	}

	// Docs mode: read query passes through
	outcome, err = o.callGatewayTool(ctx, llm.ToolCall{Name: "execute_query", Arguments: queryArgs})
	if err != nil {
		t.Fatalf("callGatewayTool: %v", err)
	}
	if gateway.calls != 1 {
		t.Fatalf("expected query to pass through, gateway calls = %d", gateway.calls)
	}

	// Spoke mode: mutation blocked
	gateway.calls = 0
	ctx = skipper.WithMode(context.Background(), "spoke")
	outcome, err = o.callGatewayTool(ctx, llm.ToolCall{Name: "execute_query", Arguments: mutationArgs})
	if err != nil {
		t.Fatalf("callGatewayTool: %v", err)
	}
	if gateway.calls != 0 {
		t.Fatal("expected mutation blocked in spoke mode")
	}
	if !strings.Contains(outcome.Content, "Mutations are not allowed") {
		t.Fatalf("expected mutation block message, got %q", outcome.Content)
	}

	// Default mode: mutation passes through
	gateway.calls = 0
	ctx = context.Background()
	_, err = o.callGatewayTool(ctx, llm.ToolCall{Name: "execute_query", Arguments: mutationArgs})
	if err != nil {
		t.Fatalf("callGatewayTool: %v", err)
	}
	if gateway.calls != 1 {
		t.Fatalf("expected mutation to pass in default mode, gateway calls = %d", gateway.calls)
	}
}

func TestResolveKnowledgeTenants(t *testing.T) {
	o := &Orchestrator{globalTenantID: "global-1"}

	tests := []struct {
		name     string
		tenantID string
		scope    string
		want     []string
	}{
		{"empty scope returns all", "t-1", "", []string{"t-1", "global-1"}},
		{"all scope", "t-1", "all", []string{"t-1", "global-1"}},
		{"tenant scope", "t-1", "tenant", []string{"t-1"}},
		{"global scope", "t-1", "global", []string{"global-1"}},
		{"empty scope no tenant", "", "", []string{"global-1"}},
		{"tenant scope no tenant", "", "tenant", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.tenantID != "" {
				ctx = skipper.WithTenantID(ctx, tt.tenantID)
			}
			got := o.resolveKnowledgeTenants(ctx, tt.scope)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSearchLimitCap(t *testing.T) {
	tests := []struct {
		name     string
		input    int
		defLimit int
		want     int
	}{
		{"zero uses default", 0, 8, 8},
		{"negative uses default", -5, 8, 8},
		{"within limit", 5, 8, 5},
		{"at max", 20, 8, 20},
		{"exceeds max", 100, 8, 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewSearchWebTool(nil)
			tool.SetSearchLimit(tt.defLimit)

			limit := tt.input
			if limit <= 0 {
				limit = tool.searchLimit
			}
			if limit > maxSearchLimit {
				limit = maxSearchLimit
			}
			if limit != tt.want {
				t.Errorf("limit = %d, want %d", limit, tt.want)
			}
		})
	}
}
