package handlers

import (
	"bytes"
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

// RPCBatchCall describes one element of a JSON-RPC batch. Result must be a
// non-nil pointer. Responses are matched by ID, not response-array order.
type RPCBatchCall struct {
	Method string
	Params any
	Result any
}

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

// BatchCall executes a bounded JSON-RPC batch in one HTTP request. EVM
// providers may return batch elements out of order, so every result is decoded
// by its request ID and a missing/duplicate response fails the whole batch.
func (c *RPCClient) BatchCall(ctx context.Context, network NetworkConfig, calls []RPCBatchCall) error {
	if len(calls) == 0 {
		return nil
	}
	if len(calls) > 100 {
		return fmt.Errorf("RPC batch exceeds 100 calls")
	}
	rpcEndpoint := network.GetRPCEndpointWithDefault()
	if rpcEndpoint == "" {
		return fmt.Errorf("no RPC endpoint configured for network %s", network.Name)
	}
	requests := make([]map[string]any, len(calls))
	for i, call := range calls {
		if call.Method == "" || call.Result == nil {
			return fmt.Errorf("RPC batch call %d has invalid method or result", i)
		}
		requests[i] = map[string]any{
			"jsonrpc": "2.0", "method": call.Method, "params": call.Params, "id": i + 1,
		}
	}
	payload, err := json.Marshal(requests)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", rpcEndpoint, bytes.NewReader(payload))
	if err != nil {
		return err
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
	var responses []struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &responses); err != nil {
		return fmt.Errorf("decode RPC batch: %w", err)
	}
	seen := make([]bool, len(calls))
	for _, item := range responses {
		index := item.ID - 1
		if index < 0 || index >= len(calls) || seen[index] {
			return fmt.Errorf("RPC batch returned invalid or duplicate id %d", item.ID)
		}
		seen[index] = true
		if len(item.Error) > 0 && string(item.Error) != "null" {
			return fmt.Errorf("RPC batch call %d error: %s", item.ID, string(item.Error))
		}
		if len(item.Result) == 0 {
			return fmt.Errorf("RPC batch call %d omitted result", item.ID)
		}
		if err := json.Unmarshal(item.Result, calls[index].Result); err != nil {
			return fmt.Errorf("decode RPC batch call %d: %w", item.ID, err)
		}
	}
	for i, ok := range seen {
		if !ok {
			return fmt.Errorf("RPC batch response missing id %d", i+1)
		}
	}
	return nil
}
