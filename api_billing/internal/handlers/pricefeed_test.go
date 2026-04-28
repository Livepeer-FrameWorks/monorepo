package handlers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"frameworks/pkg/logging"

	"github.com/shopspring/decimal"
)

// chainlinkResponse builds the eth_call response for one of the two methods
// pricefeed.go calls (decimals or latestRoundData).
type chainlinkResponse struct {
	decimals        uint8
	roundID         *big.Int
	answer          *big.Int
	updatedAt       time.Time
	answeredInRound *big.Int
}

func (c chainlinkResponse) decimalsHex() string {
	// uint8 right-padded into a 32-byte word.
	out := make([]byte, 32)
	out[31] = c.decimals
	return "0x" + hex.EncodeToString(out)
}

func (c chainlinkResponse) latestRoundDataHex() string {
	out := make([]byte, 160)
	c.roundID.FillBytes(out[0:32])
	// answer is signed; for tests we always use positive values that fit.
	if c.answer.Sign() < 0 {
		// Two's complement fill — mirror what an aggregator would emit.
		max := new(big.Int).Lsh(big.NewInt(1), 256)
		twos := new(big.Int).Add(max, c.answer)
		twos.FillBytes(out[32:64])
	} else {
		c.answer.FillBytes(out[32:64])
	}
	// startedAt = updatedAt for tests
	new(big.Int).SetInt64(c.updatedAt.Unix()).FillBytes(out[64:96])
	new(big.Int).SetInt64(c.updatedAt.Unix()).FillBytes(out[96:128])
	c.answeredInRound.FillBytes(out[128:160])
	return "0x" + hex.EncodeToString(out)
}

// stubChainlinkRPC stubs http.DefaultClient so eth_call to `decimals()` and
// `latestRoundData()` return the configured response.
func stubChainlinkRPC(t *testing.T, resp chainlinkResponse) {
	t.Helper()
	withDefaultHTTPClient(t, &http.Client{
		Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := decodeRPCRequest(t, req)
			data, _ := body["params"].([]any)[0].(map[string]any)["data"].(string)
			var result string
			switch data {
			case "0x313ce567":
				result = resp.decimalsHex()
			case "0xfeaf968c":
				result = resp.latestRoundDataHex()
			default:
				t.Fatalf("unexpected eth_call selector: %s", data)
			}
			return newJSONResponse(http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","result":%q}`, result)), nil
		}),
	})
}

func decodeRPCRequest(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		t.Fatalf("decode rpc request: %v", err)
	}
	return body
}

func testNetwork() NetworkConfig {
	return NetworkConfig{
		Name:           "testnet",
		RPCEndpointEnv: "TEST_PRICEFEED_RPC",
		PriceFeeds: map[string]string{
			"ETH": "0x000000000000000000000000000000000000feed",
		},
	}
}

func TestPriceFeed_USDC_OneToOne(t *testing.T) {
	pf := NewPriceFeed(NewRPCClient(), logging.NewLogger())

	got, err := pf.GetAssetUSDPrice(context.Background(), testNetwork(), "USDC")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Source != "one_to_one" {
		t.Errorf("Source = %q, want one_to_one", got.Source)
	}
	if !got.PriceUSD.Equal(decimal.NewFromInt(1)) {
		t.Errorf("PriceUSD = %s, want 1", got.PriceUSD.String())
	}
}

func TestPriceFeed_ETH_HappyPath_8Decimals(t *testing.T) {
	t.Setenv("TEST_PRICEFEED_RPC", "https://rpc.test")

	now := time.Now()
	stubChainlinkRPC(t, chainlinkResponse{
		decimals:        8,
		roundID:         big.NewInt(1000),
		answer:          big.NewInt(330_045_000_000), // $3,300.45 with 8 decimals
		updatedAt:       now,
		answeredInRound: big.NewInt(1000),
	})

	pf := NewPriceFeed(NewRPCClient(), logging.NewLogger())
	got, err := pf.GetAssetUSDPrice(context.Background(), testNetwork(), "ETH")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Source != "chainlink" {
		t.Errorf("Source = %q, want chainlink", got.Source)
	}
	if want := "3300.45"; got.PriceUSD.String() != want {
		t.Errorf("PriceUSD = %s, want %s", got.PriceUSD.String(), want)
	}
	if !got.UpdatedAt.Equal(time.Unix(now.Unix(), 0)) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, now)
	}
}

func TestPriceFeed_ETH_HappyPath_18Decimals(t *testing.T) {
	t.Setenv("TEST_PRICEFEED_RPC", "https://rpc.test")

	// 3,300.45 USD with 18 decimals = 3300_45 followed by 16 zeros, i.e.
	// 3,300.45 * 10^18 = 3300450000000000000000.
	answer, _ := new(big.Int).SetString("3300450000000000000000", 10)
	stubChainlinkRPC(t, chainlinkResponse{
		decimals:        18,
		roundID:         big.NewInt(42),
		answer:          answer,
		updatedAt:       time.Now(),
		answeredInRound: big.NewInt(42),
	})

	pf := NewPriceFeed(NewRPCClient(), logging.NewLogger())
	got, err := pf.GetAssetUSDPrice(context.Background(), testNetwork(), "ETH")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "3300.45"; got.PriceUSD.String() != want {
		t.Errorf("PriceUSD = %s, want %s", got.PriceUSD.String(), want)
	}
}

func TestPriceFeed_RejectsStaleUpdate(t *testing.T) {
	t.Setenv("TEST_PRICEFEED_RPC", "https://rpc.test")

	stubChainlinkRPC(t, chainlinkResponse{
		decimals:        8,
		roundID:         big.NewInt(1),
		answer:          big.NewInt(330_000_000_000),
		updatedAt:       time.Now().Add(-2 * time.Hour), // > 1h staleness threshold
		answeredInRound: big.NewInt(1),
	})

	pf := NewPriceFeed(NewRPCClient(), logging.NewLogger())
	_, err := pf.GetAssetUSDPrice(context.Background(), testNetwork(), "ETH")
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("expected stale error, got %v", err)
	}
}

func TestPriceFeed_RejectsNonPositiveAnswer(t *testing.T) {
	t.Setenv("TEST_PRICEFEED_RPC", "https://rpc.test")

	stubChainlinkRPC(t, chainlinkResponse{
		decimals:        8,
		roundID:         big.NewInt(1),
		answer:          big.NewInt(0),
		updatedAt:       time.Now(),
		answeredInRound: big.NewInt(1),
	})

	pf := NewPriceFeed(NewRPCClient(), logging.NewLogger())
	_, err := pf.GetAssetUSDPrice(context.Background(), testNetwork(), "ETH")
	if err == nil || !strings.Contains(err.Error(), "non-positive") {
		t.Fatalf("expected non-positive error, got %v", err)
	}
}

func TestPriceFeed_RejectsIncompleteRound(t *testing.T) {
	t.Setenv("TEST_PRICEFEED_RPC", "https://rpc.test")

	stubChainlinkRPC(t, chainlinkResponse{
		decimals:        8,
		roundID:         big.NewInt(1000),
		answer:          big.NewInt(330_000_000_000),
		updatedAt:       time.Now(),
		answeredInRound: big.NewInt(999), // < roundID
	})

	pf := NewPriceFeed(NewRPCClient(), logging.NewLogger())
	_, err := pf.GetAssetUSDPrice(context.Background(), testNetwork(), "ETH")
	if err == nil || !strings.Contains(err.Error(), "incomplete round") {
		t.Fatalf("expected incomplete-round error, got %v", err)
	}
}

func TestPriceFeed_RejectsLPT_NoFeedConfigured(t *testing.T) {
	pf := NewPriceFeed(NewRPCClient(), logging.NewLogger())

	_, err := pf.GetAssetUSDPrice(context.Background(), testNetwork(), "LPT")
	if err == nil || !strings.Contains(err.Error(), "no Chainlink price feed") {
		t.Fatalf("expected no-feed error, got %v", err)
	}
}

func TestPriceFeed_CachesSuccessfulRead(t *testing.T) {
	t.Setenv("TEST_PRICEFEED_RPC", "https://rpc.test")

	calls := 0
	withDefaultHTTPClient(t, &http.Client{
		Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			body := decodeRPCRequest(t, req)
			data, _ := body["params"].([]any)[0].(map[string]any)["data"].(string)
			resp := chainlinkResponse{
				decimals:        8,
				roundID:         big.NewInt(1),
				answer:          big.NewInt(330_000_000_000),
				updatedAt:       time.Now(),
				answeredInRound: big.NewInt(1),
			}
			var result string
			switch data {
			case "0x313ce567":
				result = resp.decimalsHex()
			case "0xfeaf968c":
				result = resp.latestRoundDataHex()
			}
			return newJSONResponse(http.StatusOK, fmt.Sprintf(`{"jsonrpc":"2.0","result":%q}`, result)), nil
		}),
	})

	pf := NewPriceFeed(NewRPCClient(), logging.NewLogger())
	for i := range 3 {
		if _, err := pf.GetAssetUSDPrice(context.Background(), testNetwork(), "ETH"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	// Three calls; the first does decimals + latestRoundData (2 RPC calls);
	// subsequent reads serve from cache (0 RPC calls).
	if calls != 2 {
		t.Errorf("RPC calls = %d, want 2 (cached after first)", calls)
	}
}

func TestPriceFeed_RPCError(t *testing.T) {
	t.Setenv("TEST_PRICEFEED_RPC", "https://rpc.test")

	withDefaultHTTPClient(t, &http.Client{
		Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return newJSONResponse(http.StatusOK, `{"jsonrpc":"2.0","error":{"code":-32000,"message":"boom"}}`), nil
		}),
	})

	pf := NewPriceFeed(NewRPCClient(), logging.NewLogger())
	_, err := pf.GetAssetUSDPrice(context.Background(), testNetwork(), "ETH")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected RPC error to propagate, got %v", err)
	}
}
