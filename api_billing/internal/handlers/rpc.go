package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// RPCClient is a JSON-RPC 2.0 client over HTTP for EVM endpoints.
//
// One instance is shared across handlers that talk to the same endpoint pool
// (X402, price feeds, future on-chain readers).
type RPCClient struct{}

// NewRPCClient returns an RPCClient. Requests use http.DefaultClient so test
// helpers that swap http.DefaultClient (see x402_conversion_test.go) continue
// to intercept; per-call timeouts come from the caller's context.
func NewRPCClient() *RPCClient {
	return &RPCClient{}
}

// Call performs a JSON-RPC call against the network's configured endpoint
// (env var with the default-endpoint fallback). The result is decoded into
// `result` via JSON marshal/unmarshal — callers pass a pointer to a struct
// or string matching the method's return shape.
func (c *RPCClient) Call(ctx context.Context, network NetworkConfig, method string, params any, result any) error {
	rpcEndpoint := network.GetRPCEndpointWithDefault()
	if rpcEndpoint == "" {
		return fmt.Errorf("no RPC endpoint configured for network %s", network.Name)
	}

	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
		"id":      1,
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, reqErr := http.NewRequestWithContext(ctx, "POST", rpcEndpoint, strings.NewReader(string(reqJSON)))
	if reqErr != nil {
		return reqErr
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("RPC HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var rpcResp struct {
		Result any              `json:"result"`
		Error  *json.RawMessage `json:"error"`
	}
	if unmarshalErr := json.Unmarshal(body, &rpcResp); unmarshalErr != nil {
		return unmarshalErr
	}

	if rpcResp.Error != nil {
		return fmt.Errorf("RPC error: %s", string(*rpcResp.Error))
	}

	resultJSON, err := json.Marshal(rpcResp.Result)
	if err != nil {
		return err
	}
	return json.Unmarshal(resultJSON, result)
}
