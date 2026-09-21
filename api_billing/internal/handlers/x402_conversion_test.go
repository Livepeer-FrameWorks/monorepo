package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/DATA-DOG/go-sqlmock"
)

type testRoundTripFunc func(*http.Request) (*http.Response, error)

func (f testRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newJSONResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func withDefaultHTTPClient(t *testing.T, client *http.Client) {
	t.Helper()
	old := http.DefaultClient
	http.DefaultClient = client
	t.Cleanup(func() { http.DefaultClient = old })
}

func TestRPCCall_DecodeAndErrorHandling(t *testing.T) {
	appconfigtest.Set(t, "BASE_SEPOLIA_RPC_ENDPOINT", "https://rpc.test")

	handler := &X402Handler{rpc: NewRPCClient()}
	network := NetworkConfig{
		Name:           "testnet",
		RPCEndpointEnv: "BASE_SEPOLIA_RPC_ENDPOINT",
	}

	t.Run("malformed json response", func(t *testing.T) {
		withDefaultHTTPClient(t, &http.Client{
			Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return newJSONResponse(http.StatusOK, `{"result":`), nil
			}),
		})

		var result string
		err := handler.rpc.Call(context.Background(), network, "eth_chainId", []any{}, &result)
		if err == nil {
			t.Fatal("expected unmarshal error")
		}
	})

	t.Run("rpc error payload", func(t *testing.T) {
		withDefaultHTTPClient(t, &http.Client{
			Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return newJSONResponse(http.StatusOK, `{"jsonrpc":"2.0","error":{"code":-32000,"message":"boom"}}`), nil
			}),
		})

		var result string
		err := handler.rpc.Call(context.Background(), network, "eth_chainId", []any{}, &result)
		if err == nil || !strings.Contains(err.Error(), "RPC error:") || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("expected rpc error message, got %v", err)
		}
	})

	t.Run("successful decode into typed result", func(t *testing.T) {
		withDefaultHTTPClient(t, &http.Client{
			Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return newJSONResponse(http.StatusOK, `{"jsonrpc":"2.0","result":"0xabc"}`), nil
			}),
		})

		var result string
		err := handler.rpc.Call(context.Background(), network, "eth_chainId", []any{}, &result)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "0xabc" {
			t.Fatalf("result: got %q, want %q", result, "0xabc")
		}
	})
}

func TestGetVATRateForTenant_MalformedBillingAddressDoesNotGuessCountry(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT tax_id, COALESCE\\(billing_address").
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"tax_id", "billing_address"}).
			AddRow(sql.NullString{Valid: false}, []byte(`{"country":`)))

	handler := &X402Handler{
		db:     db,
		logger: logging.NewLogger(),
	}

	rate, country, isB2B := handler.getVATRateForTenant(context.Background(), "tenant-1", "")
	if country != "" {
		t.Fatalf("country: got %q, want empty", country)
	}
	if rate != 0 {
		t.Fatalf("rate: got %d, want 0 pending location review", rate)
	}
	if isB2B {
		t.Fatal("isB2B: got true, want false")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestIsBillingDetailsComplete_AddressDecode(t *testing.T) {
	if isBillingDetailsComplete(sql.NullString{Valid: true, String: "billing@example.com"}, []byte(`{"street":`)) {
		t.Fatal("expected false for malformed billing address json")
	}

	complete := isBillingDetailsComplete(
		sql.NullString{Valid: true, String: "billing@example.com"},
		[]byte(`{"street":"Main","city":"AMS","postal_code":"1000AA","country":"NL"}`),
	)
	if !complete {
		t.Fatal("expected true for complete billing details")
	}
}

func TestRPCCall_NonOKStatus(t *testing.T) {
	appconfigtest.Set(t, "BASE_SEPOLIA_RPC_ENDPOINT", "https://rpc.test")

	handler := &X402Handler{rpc: NewRPCClient()}
	network := NetworkConfig{
		Name:           "testnet",
		RPCEndpointEnv: "BASE_SEPOLIA_RPC_ENDPOINT",
	}

	withDefaultHTTPClient(t, &http.Client{
		Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return newJSONResponse(http.StatusBadGateway, "upstream"), nil
		}),
	})

	var result string
	err := handler.rpc.Call(context.Background(), network, "eth_chainId", []any{}, &result)
	if err == nil || !strings.Contains(err.Error(), "RPC HTTP 502") {
		t.Fatalf("expected HTTP 502 error, got %v", err)
	}
}

func TestRPCCall_ErrorFieldRoundTripShape(t *testing.T) {
	appconfigtest.Set(t, "BASE_SEPOLIA_RPC_ENDPOINT", "https://rpc.test")

	handler := &X402Handler{rpc: NewRPCClient()}
	network := NetworkConfig{
		Name:           "testnet",
		RPCEndpointEnv: "BASE_SEPOLIA_RPC_ENDPOINT",
	}

	payload := map[string]any{
		"code":    -32001,
		"message": "permission denied",
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	withDefaultHTTPClient(t, &http.Client{
		Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return newJSONResponse(http.StatusOK, `{"jsonrpc":"2.0","error":`+string(raw)+`}`), nil
		}),
	})

	var result string
	err = handler.rpc.Call(context.Background(), network, "eth_chainId", []any{}, &result)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("expected propagated rpc error payload, got %v", err)
	}
}
