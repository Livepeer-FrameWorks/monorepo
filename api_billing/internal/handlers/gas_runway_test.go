package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateGasRunway(t *testing.T) {
	for _, tc := range []struct {
		name      string
		balance   string
		wantError bool
	}{
		{name: "zero", balance: "0x0", wantError: true},
		{name: "insufficient", balance: "0x5207", wantError: true},
		{name: "one transaction", balance: "0x5208"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				defer request.Body.Close()
				var payload struct {
					ID     int    `json:"id"`
					Method string `json:"method"`
				}
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				result := tc.balance
				switch payload.Method {
				case "eth_gasPrice":
					result = "0x64"
				case "eth_maxPriorityFeePerGas":
					result = "0xa"
				}
				_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": payload.ID, "result": result})
			}))
			defer server.Close()
			t.Setenv("TEST_GAS_RPC_ENDPOINT", server.URL)
			network := NetworkConfig{Name: "test", RPCEndpointEnv: "TEST_GAS_RPC_ENDPOINT"}
			err := ValidateGasRunway(context.Background(), NewRPCClient(), network, "0x1111111111111111111111111111111111111111", 100)
			if (err != nil) != tc.wantError {
				t.Fatalf("ValidateGasRunway() error=%v wantError=%t", err, tc.wantError)
			}
		})
	}
}
