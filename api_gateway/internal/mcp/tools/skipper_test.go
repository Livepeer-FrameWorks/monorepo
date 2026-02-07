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
	mock := &mockSkipperCaller{result: `{"answer":"test","confidence":"verified"}`}
	ctx := ctxWithTenant("tenant-abc")

	args := AskConsultantInput{Question: "How does SRT work?"}
	result, _, err := proxyToSkipper(ctx, mock, "ask_consultant", args, nil)
	if err != nil {
		t.Fatalf("proxyToSkipper error: %v", err)
	}
	if result.IsError {
		t.Fatal("expected success result")
	}

	var forwarded map[string]any
	if err := json.Unmarshal(mock.lastArgs, &forwarded); err != nil {
		t.Fatalf("unmarshal forwarded args: %v", err)
	}
	if forwarded["tenant_id"] != "tenant-abc" {
		t.Fatalf("expected tenant_id=tenant-abc, got %v", forwarded["tenant_id"])
	}
	if forwarded["question"] != "How does SRT work?" {
		t.Fatalf("expected question preserved, got %v", forwarded["question"])
	}
	if mock.lastTool != "ask_consultant" {
		t.Fatalf("expected tool=ask_consultant, got %s", mock.lastTool)
	}
}

func TestProxyToSkipper_OverwritesUserTenantID(t *testing.T) {
	mock := &mockSkipperCaller{result: `{"answer":"ok"}`}
	ctx := ctxWithTenant("real-tenant")

	args := AskConsultantInput{Question: "test"}
	result, _, err := proxyToSkipper(ctx, mock, "ask_consultant", args, nil)
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
	ctx := context.Background()

	args := AskConsultantInput{Question: "test"}
	result, _, err := proxyToSkipper(ctx, mock, "ask_consultant", args, nil)
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

	args := AskConsultantInput{Question: "test"}
	result, _, err := proxyToSkipper(ctx, mock, "ask_consultant", args, nil)
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
	expected := `{"answer":"SRT uses AES-128","confidence":"verified","sources":[]}`
	mock := &mockSkipperCaller{result: expected}
	ctx := ctxWithTenant("tenant-abc")

	args := AskConsultantInput{Question: "How does SRT encryption work?"}
	result, _, err := proxyToSkipper(ctx, mock, "ask_consultant", args, nil)
	if err != nil {
		t.Fatalf("proxyToSkipper error: %v", err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}
	text := extractToolText(result)
	if text != expected {
		t.Fatalf("expected %q, got %q", expected, text)
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
