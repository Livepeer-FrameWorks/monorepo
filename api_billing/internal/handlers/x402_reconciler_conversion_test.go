package handlers

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestX402Reconciler_GetLatestBlockNumber_DecodeAndErrorHandling(t *testing.T) {
	t.Setenv("TEST_RECONCILER_RPC_ENDPOINT", "https://rpc.test")
	network := NetworkConfig{
		Name:           "testnet",
		RPCEndpointEnv: "TEST_RECONCILER_RPC_ENDPOINT",
	}
	reconciler := &X402Reconciler{}

	t.Run("malformed json response", func(t *testing.T) {
		withDefaultHTTPClient(t, &http.Client{
			Transport: testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return newJSONResponse(http.StatusOK, `{"result":`), nil
			}),
		})

		_, err := reconciler.getLatestBlockNumber(context.Background(), network)
		if err == nil {
			t.Fatal("expected unmarshal error")
		}
	})

	t.Run("rpc error payload", func(t *testing.T) {
		withDefaultHTTPClient(t, &http.Client{
			Transport: testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return newJSONResponse(http.StatusOK, `{"jsonrpc":"2.0","error":{"code":-32000,"message":"boom"}}`), nil
			}),
		})

		_, err := reconciler.getLatestBlockNumber(context.Background(), network)
		if err == nil || !strings.Contains(err.Error(), "RPC error:") || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("expected rpc error message, got %v", err)
		}
	})

	t.Run("successful decode", func(t *testing.T) {
		withDefaultHTTPClient(t, &http.Client{
			Transport: testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return newJSONResponse(http.StatusOK, `{"jsonrpc":"2.0","result":"0x2a"}`), nil
			}),
		})

		blockNumber, err := reconciler.getLatestBlockNumber(context.Background(), network)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if blockNumber != 42 {
			t.Fatalf("block number: got %d, want 42", blockNumber)
		}
	})
}

func TestX402Reconciler_GetTransactionReceipt_DecodeAndErrorHandling(t *testing.T) {
	t.Setenv("TEST_RECONCILER_RPC_ENDPOINT", "https://rpc.test")
	network := NetworkConfig{
		Name:           "testnet",
		RPCEndpointEnv: "TEST_RECONCILER_RPC_ENDPOINT",
	}
	reconciler := &X402Reconciler{}

	t.Run("malformed json response", func(t *testing.T) {
		withDefaultHTTPClient(t, &http.Client{
			Transport: testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return newJSONResponse(http.StatusOK, `{"result":`), nil
			}),
		})

		_, err := reconciler.getTransactionReceipt(context.Background(), network, "0xhash")
		if err == nil {
			t.Fatal("expected unmarshal error")
		}
	})

	t.Run("rpc error payload", func(t *testing.T) {
		withDefaultHTTPClient(t, &http.Client{
			Transport: testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return newJSONResponse(http.StatusOK, `{"jsonrpc":"2.0","error":{"code":-32000,"message":"denied"}}`), nil
			}),
		})

		_, err := reconciler.getTransactionReceipt(context.Background(), network, "0xhash")
		if err == nil || !strings.Contains(err.Error(), "RPC error:") || !strings.Contains(err.Error(), "denied") {
			t.Fatalf("expected rpc error message, got %v", err)
		}
	})

	t.Run("successful decode", func(t *testing.T) {
		withDefaultHTTPClient(t, &http.Client{
			Transport: testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return newJSONResponse(http.StatusOK, `{
					"jsonrpc":"2.0",
					"result":{"status":"0x1","blockNumber":"0x10","gasUsed":"0x5208"}
				}`), nil
			}),
		})

		receipt, err := reconciler.getTransactionReceipt(context.Background(), network, "0xhash")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if receipt == nil {
			t.Fatal("expected receipt")
		}
		if receipt.Status != "0x1" || receipt.BlockNumber != "0x10" || receipt.GasUsed != "0x5208" {
			t.Fatalf("unexpected receipt mapping: %+v", receipt)
		}
	})
}
