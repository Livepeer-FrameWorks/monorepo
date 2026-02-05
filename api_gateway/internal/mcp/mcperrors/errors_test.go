package mcperrors

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

func TestAuthRequired(t *testing.T) {
	err := AuthRequired()
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	var jErr *jsonrpc.Error
	if !errors.As(err, &jErr) {
		t.Fatalf("expected *jsonrpc.Error, got %T", err)
	}

	if jErr.Code != -32001 {
		t.Errorf("expected code -32001, got %d", jErr.Code)
	}
	if jErr.Message != "not authenticated" {
		t.Errorf("expected message 'not authenticated', got %q", jErr.Message)
	}

	var data map[string]any
	if err := json.Unmarshal(jErr.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal error data: %v", err)
	}

	rm, ok := data["resource_metadata"].(string)
	if !ok || rm == "" {
		t.Error("expected resource_metadata URL in error data")
	}
	if rm != ResourceMetadataURL {
		t.Errorf("expected %q, got %q", ResourceMetadataURL, rm)
	}
}
