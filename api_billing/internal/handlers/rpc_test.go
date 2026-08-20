package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRPCBatchCallMatchesOutOfOrderResponsesByID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var requests []map[string]any
		if err := json.NewDecoder(r.Body).Decode(&requests); err != nil {
			t.Fatal(err)
		}
		if len(requests) != 2 {
			t.Fatalf("request count = %d", len(requests))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"jsonrpc":"2.0","id":2,"result":"second"},
			{"jsonrpc":"2.0","id":1,"result":"first"}
		]`))
	}))
	defer server.Close()
	t.Setenv("TEST_BATCH_RPC_ENDPOINT", server.URL)
	network := NetworkConfig{Name: "test", RPCEndpointEnv: "TEST_BATCH_RPC_ENDPOINT"}
	var first, second string
	err := NewRPCClient().BatchCall(t.Context(), network, []RPCBatchCall{
		{Method: "one", Params: []any{}, Result: &first},
		{Method: "two", Params: []any{}, Result: &second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != "first" || second != "second" {
		t.Fatalf("decoded results = %q, %q", first, second)
	}
}

func TestRPCBatchCallRejectsMissingResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"jsonrpc":"2.0","id":1,"result":"first"}]`))
	}))
	defer server.Close()
	t.Setenv("TEST_BATCH_RPC_ENDPOINT", server.URL)
	network := NetworkConfig{Name: "test", RPCEndpointEnv: "TEST_BATCH_RPC_ENDPOINT"}
	var first, second string
	err := NewRPCClient().BatchCall(t.Context(), network, []RPCBatchCall{
		{Method: "one", Params: []any{}, Result: &first},
		{Method: "two", Params: []any{}, Result: &second},
	})
	if err == nil {
		t.Fatal("missing batch response was accepted")
	}
}
