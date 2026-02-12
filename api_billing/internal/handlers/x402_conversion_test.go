package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"frameworks/pkg/logging"

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

func resetECBCache() {
	ecbRateCache.Lock()
	ecbRateCache.rate = 0
	ecbRateCache.fetchedAt = time.Time{}
	ecbRateCache.Unlock()
}

func setECBCache(rate float64, fetchedAt time.Time) {
	ecbRateCache.Lock()
	ecbRateCache.rate = rate
	ecbRateCache.fetchedAt = fetchedAt
	ecbRateCache.Unlock()
}

func TestRPCCall_DecodeAndErrorHandling(t *testing.T) {
	t.Setenv("TEST_RPC_ENDPOINT", "https://rpc.test")

	handler := &X402Handler{logger: logging.NewLogger()}
	network := NetworkConfig{
		Name:           "testnet",
		RPCEndpointEnv: "TEST_RPC_ENDPOINT",
	}

	t.Run("malformed json response", func(t *testing.T) {
		withDefaultHTTPClient(t, &http.Client{
			Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return newJSONResponse(http.StatusOK, `{"result":`), nil
			}),
		})

		var result string
		err := handler.rpcCall(context.Background(), network, "eth_chainId", []interface{}{}, &result)
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
		err := handler.rpcCall(context.Background(), network, "eth_chainId", []interface{}{}, &result)
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
		err := handler.rpcCall(context.Background(), network, "eth_chainId", []interface{}{}, &result)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "0xabc" {
			t.Fatalf("result: got %q, want %q", result, "0xabc")
		}
	})
}

func TestGetEurUsdRate_InlineDecodeFallbacks(t *testing.T) {
	t.Cleanup(resetECBCache)
	handler := &X402Handler{logger: logging.NewLogger()}

	t.Run("stale cache returned when decode fails", func(t *testing.T) {
		setECBCache(0.91, time.Now().Add(-48*time.Hour))
		withDefaultHTTPClient(t, &http.Client{
			Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return newJSONResponse(http.StatusOK, `{"rates":`), nil
			}),
		})

		rate, err := handler.getEurUsdRate()
		if err != nil {
			t.Fatalf("expected stale cache fallback, got error: %v", err)
		}
		if rate != 0.91 {
			t.Fatalf("rate: got %v, want %v", rate, 0.91)
		}
	})

	t.Run("decode failure without cache returns error", func(t *testing.T) {
		resetECBCache()
		withDefaultHTTPClient(t, &http.Client{
			Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return newJSONResponse(http.StatusOK, `{"rates":`), nil
			}),
		})

		_, err := handler.getEurUsdRate()
		if err == nil || !strings.Contains(err.Error(), "failed to decode ECB rate response") {
			t.Fatalf("expected decode error, got %v", err)
		}
	})

	t.Run("missing EUR rate without cache returns error", func(t *testing.T) {
		resetECBCache()
		withDefaultHTTPClient(t, &http.Client{
			Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return newJSONResponse(http.StatusOK, `{"rates":{"USD":1}}`), nil
			}),
		})

		_, err := handler.getEurUsdRate()
		if err == nil || !strings.Contains(err.Error(), "EUR rate not found in response") {
			t.Fatalf("expected missing EUR error, got %v", err)
		}
	})

	t.Run("successful decode updates cache", func(t *testing.T) {
		resetECBCache()
		withDefaultHTTPClient(t, &http.Client{
			Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return newJSONResponse(http.StatusOK, `{"rates":{"EUR":0.93}}`), nil
			}),
		})

		rate, err := handler.getEurUsdRate()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rate != 0.93 {
			t.Fatalf("rate: got %v, want %v", rate, 0.93)
		}

		ecbRateCache.RLock()
		cachedRate := ecbRateCache.rate
		fetchedAt := ecbRateCache.fetchedAt
		ecbRateCache.RUnlock()
		if cachedRate != 0.93 {
			t.Fatalf("cached rate: got %v, want %v", cachedRate, 0.93)
		}
		if fetchedAt.IsZero() {
			t.Fatal("expected fetchedAt to be set")
		}
	})
}

func TestGetEurUsdRate_FreshCacheSkipsFetch(t *testing.T) {
	t.Cleanup(resetECBCache)
	handler := &X402Handler{logger: logging.NewLogger()}

	setECBCache(0.88, time.Now().Add(-1*time.Hour))
	httpCalls := 0
	withDefaultHTTPClient(t, &http.Client{
		Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			httpCalls++
			return nil, errors.New("network should not be called for fresh cache")
		}),
	})

	rate, err := handler.getEurUsdRate()
	if err != nil {
		t.Fatalf("expected cached rate without error, got %v", err)
	}
	if rate != 0.88 {
		t.Fatalf("rate: got %v, want %v", rate, 0.88)
	}
	if httpCalls != 0 {
		t.Fatalf("expected no HTTP calls for fresh cache, got %d", httpCalls)
	}
}

func TestGetVATRateForTenant_MalformedBillingAddressFallsBack(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT tax_id, billing_address").
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"tax_id", "billing_address"}).
			AddRow(sql.NullString{Valid: false}, []byte(`{"country":`)))

	handler := &X402Handler{
		db:     db,
		logger: logging.NewLogger(),
	}

	rate, country, isB2B := handler.getVATRateForTenant("tenant-1", "")
	if country != "NL" {
		t.Fatalf("country: got %q, want %q", country, "NL")
	}
	if rate != euVATRates["NL"] {
		t.Fatalf("rate: got %d, want %d", rate, euVATRates["NL"])
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

func TestRPCCall_ErrorFieldRoundTripShape(t *testing.T) {
	t.Setenv("TEST_RPC_ENDPOINT", "https://rpc.test")

	handler := &X402Handler{logger: logging.NewLogger()}
	network := NetworkConfig{
		Name:           "testnet",
		RPCEndpointEnv: "TEST_RPC_ENDPOINT",
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
	err = handler.rpcCall(context.Background(), network, "eth_chainId", []interface{}{}, &result)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("expected propagated rpc error payload, got %v", err)
	}
}
