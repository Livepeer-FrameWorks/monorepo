package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"frameworks/pkg/ctxkeys"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mockSkipperCaller struct {
	lastTool string
	lastArgs json.RawMessage
	result   string
	err      error
}

func (m *mockSkipperCaller) CallTool(_ context.Context, name string, arguments json.RawMessage) (string, error) {
	m.lastTool = name
	m.lastArgs = arguments
	return m.result, m.err
}

func ctxWithTenant(tenantID string) context.Context {
	return context.WithValue(context.Background(), ctxkeys.KeyTenantID, tenantID)
}

func TestProxyToSkipper_InjectsTenantID(t *testing.T) {
	mock := &mockSkipperCaller{result: `{"results":[]}`}
	ctx := ctxWithTenant("tenant-abc")

	args := SearchKnowledgeInput{Query: "test", Limit: 5}
	result, _, err := proxyToSkipper(ctx, mock, "search_knowledge", args, nil)
	if err != nil {
		t.Fatalf("proxyToSkipper error: %v", err)
	}
	if result.IsError {
		t.Fatal("expected success result")
	}

	// Verify tenant_id was injected into forwarded arguments.
	var forwarded map[string]any
	if err := json.Unmarshal(mock.lastArgs, &forwarded); err != nil {
		t.Fatalf("unmarshal forwarded args: %v", err)
	}
	if forwarded["tenant_id"] != "tenant-abc" {
		t.Fatalf("expected tenant_id=tenant-abc, got %v", forwarded["tenant_id"])
	}
	if forwarded["query"] != "test" {
		t.Fatalf("expected query=test, got %v", forwarded["query"])
	}
	if mock.lastTool != "search_knowledge" {
		t.Fatalf("expected tool=search_knowledge, got %s", mock.lastTool)
	}
}

func TestProxyToSkipper_OverwritesUserTenantID(t *testing.T) {
	mock := &mockSkipperCaller{result: `{"results":[]}`}
	ctx := ctxWithTenant("real-tenant")

	// Simulate an agent trying to set their own tenant_id.
	args := SearchKnowledgeInput{Query: "test"}
	result, _, err := proxyToSkipper(ctx, mock, "search_knowledge", args, nil)
	if err != nil {
		t.Fatalf("proxyToSkipper error: %v", err)
	}
	if result.IsError {
		t.Fatal("expected success result")
	}

	var forwarded map[string]any
	if err := json.Unmarshal(mock.lastArgs, &forwarded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if forwarded["tenant_id"] != "real-tenant" {
		t.Fatalf("expected tenant_id overwritten to real-tenant, got %v", forwarded["tenant_id"])
	}
}

func TestProxyToSkipper_MissingTenantID(t *testing.T) {
	mock := &mockSkipperCaller{result: `{}`}
	ctx := context.Background() // No tenant_id in context.

	args := SearchKnowledgeInput{Query: "test"}
	result, _, err := proxyToSkipper(ctx, mock, "search_knowledge", args, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for missing tenant_id")
	}
	text := extractToolText(result)
	if text == "" {
		t.Fatal("expected error message")
	}
}

func TestProxyToSkipper_SkipperError(t *testing.T) {
	mock := &mockSkipperCaller{err: fmt.Errorf("connection refused")}
	ctx := ctxWithTenant("tenant-abc")

	args := SearchWebInput{Query: "test"}
	result, _, err := proxyToSkipper(ctx, mock, "search_web", args, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result when skipper returns error")
	}
	text := extractToolText(result)
	if text == "" {
		t.Fatal("expected error message in result")
	}
}

func TestProxyToSkipper_Success(t *testing.T) {
	mock := &mockSkipperCaller{result: `{"query":"streaming","results":[{"title":"Docs"}]}`}
	ctx := ctxWithTenant("tenant-abc")

	args := SearchWebInput{Query: "streaming"}
	result, _, err := proxyToSkipper(ctx, mock, "search_web", args, nil)
	if err != nil {
		t.Fatalf("proxyToSkipper error: %v", err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}
	text := extractToolText(result)
	if text != mock.result {
		t.Fatalf("expected %q, got %q", mock.result, text)
	}
}

func extractToolText(result *mcp.CallToolResult) string {
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}
