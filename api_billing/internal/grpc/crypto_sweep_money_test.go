package grpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"frameworks/api_billing/internal/handlers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRequireCryptoSweepOperatorFailsClosed(t *testing.T) {
	if code := status.Code(requireCryptoSweepOperator(context.Background())); code != codes.PermissionDenied {
		t.Fatalf("anonymous code = %v", code)
	}
	userCtx := context.WithValue(context.Background(), ctxkeys.KeyPlatformOperator, false)
	if code := status.Code(requireCryptoSweepOperator(userCtx)); code != codes.PermissionDenied {
		t.Fatalf("ordinary user code = %v", code)
	}
	operatorCtx := context.WithValue(context.Background(), ctxkeys.KeyPlatformOperator, true)
	if err := requireCryptoSweepOperator(operatorCtx); err != nil {
		t.Fatalf("operator rejected: %v", err)
	}
	if err := requireCryptoSweepOperator(serviceTestContext()); err != nil {
		t.Fatalf("authenticated service rejected: %v", err)
	}
}

func TestSweepIdentifiersAreCanonicalAndNonSecret(t *testing.T) {
	if got := xpubFingerprint(" \t"); got != "" {
		t.Fatalf("blank xpub fingerprint = %q", got)
	}
	fingerprint := xpubFingerprint(" xpub-sensitive-material ")
	if fingerprint != xpubFingerprint("xpub-sensitive-material") || len(fingerprint) != 16 || strings.Contains(fingerprint, "xpub") {
		t.Fatalf("fingerprint = %q", fingerprint)
	}
	if _, err := hex.DecodeString(fingerprint); err != nil {
		t.Fatalf("fingerprint is not hex: %v", err)
	}
	if fingerprint == xpubFingerprint("different-xpub") {
		t.Fatal("distinct keys have the same test fingerprint")
	}

	nonce1, err := randomAuthorizationNonce()
	if err != nil {
		t.Fatal(err)
	}
	nonce2, err := randomAuthorizationNonce()
	if err != nil {
		t.Fatal(err)
	}
	if len(nonce1) != 66 || !strings.HasPrefix(nonce1, "0x") || nonce1 == nonce2 {
		t.Fatalf("nonces = %q, %q", nonce1, nonce2)
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(nonce1, "0x")); err != nil {
		t.Fatalf("nonce is not 32-byte hex: %v", err)
	}
}

func TestSweepNetworkAndTreasuryValidation(t *testing.T) {
	t.Setenv("X402_INCLUDE_TESTNETS", "false")
	mainnet, err := sweepNetwork(" BASE ")
	if err != nil || mainnet.Name != "base" || mainnet.ChainID != 8453 {
		t.Fatalf("mainnet = %+v, %v", mainnet, err)
	}
	if got := caip2ForSweep(mainnet); got != "eip155:8453" {
		t.Fatalf("caip2 = %q", got)
	}
	if _, err := sweepNetwork("unknown-chain"); err == nil {
		t.Fatal("unsupported network accepted")
	}
	if _, err := sweepNetwork("base-sepolia"); err == nil || !strings.Contains(err.Error(), "testnet") {
		t.Fatalf("disabled testnet error = %v", err)
	}
	t.Setenv("X402_INCLUDE_TESTNETS", "true")
	if network, err := sweepNetwork("base-sepolia"); err != nil || !network.IsTestnet {
		t.Fatalf("enabled testnet = %+v, %v", network, err)
	}

	t.Setenv("CRYPTO_TREASURY_BASE", "")
	if _, err := sweepTreasury("base"); err == nil {
		t.Fatal("empty treasury accepted")
	}
	t.Setenv("CRYPTO_TREASURY_BASE", "0x0000000000000000000000000000000000000000")
	if _, err := sweepTreasury("base"); err == nil {
		t.Fatal("zero treasury accepted")
	}
	t.Setenv("CRYPTO_TREASURY_BASE", " 0x1111111111111111111111111111111111111111 ")
	if got, err := sweepTreasury("base"); err != nil || got != "0x1111111111111111111111111111111111111111" {
		t.Fatalf("treasury = %q, %v", got, err)
	}
}

func TestSweepFeeCalculationUsesProviderTipAndBoundedFallback(t *testing.T) {
	tests := []struct {
		name     string
		response map[string]any
		baseFee  string
		wantTip  *big.Int
		wantMax  *big.Int
		wantErr  bool
	}{
		{name: "provider tip", response: map[string]any{"result": "0x3"}, baseFee: "0xa", wantTip: big.NewInt(3), wantMax: big.NewInt(23)},
		{name: "rpc failure falls back", response: map[string]any{"error": map[string]any{"code": -32000, "message": "unsupported"}}, baseFee: "0x1", wantTip: big.NewInt(2_000_000_000), wantMax: big.NewInt(2_000_000_002)},
		{name: "malformed provider tip", response: map[string]any{"result": "not-hex"}, baseFee: "0x1", wantErr: true},
		{name: "malformed base fee", response: map[string]any{"result": "0x1"}, baseFee: "bad", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				payload := map[string]any{"jsonrpc": "2.0", "id": 1}
				for key, value := range test.response {
					payload[key] = value
				}
				_ = json.NewEncoder(w).Encode(payload)
			}))
			defer httpServer.Close()
			t.Setenv("BASE_RPC_ENDPOINT", httpServer.URL)
			server := &PurserServer{rpcClient: handlers.NewRPCClient()}
			tip, maxFee, err := server.sweepFees(context.Background(), handlers.Networks["base"], sweepRPCBlock{BaseFeePerGas: test.baseFee})
			if (err != nil) != test.wantErr {
				t.Fatalf("err = %v, wantErr=%v", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if tip.Cmp(test.wantTip) != 0 || maxFee.Cmp(test.wantMax) != 0 {
				t.Fatalf("tip/max = %s/%s, want %s/%s", tip, maxFee, test.wantTip, test.wantMax)
			}
		})
	}
}

func TestX402ResourceURLNeverForwardsArbitrarySchemes(t *testing.T) {
	t.Setenv("GATEWAY_PUBLIC_URL", " https://gateway.example.test/ ")
	tests := map[string]string{
		"https://merchant.example/pay": "https://merchant.example/pay",
		"http://merchant.example/pay":  "http://merchant.example/pay",
		"/api/viewer/resolve":          "https://gateway.example.test/api/viewer/resolve",
		"viewer://stream/abc":          "https://gateway.example.test/api/viewer/resolve",
		"graphql://operation":          "https://gateway.example.test/graphql",
		"javascript:alert(1)":          "https://gateway.example.test/graphql",
		"":                             "https://gateway.example.test/graphql",
	}
	for resource, want := range tests {
		if got := x402ResourceURL(resource); got != want {
			t.Errorf("x402ResourceURL(%q) = %q, want %q", resource, got, want)
		}
	}
	t.Setenv("GATEWAY_PUBLIC_URL", "")
	if got := x402ResourceURL("/pay"); got != "http://localhost:18005/pay" {
		t.Fatalf("local fallback = %q", got)
	}
}

func TestDecimalFloatPreservesBillingValues(t *testing.T) {
	for input, want := range map[string]float64{"0": 0, "12.34": 12.34, "-0.01": -0.01, "999999.99": 999999.99} {
		value, err := decimal.NewFromString(input)
		if err != nil {
			t.Fatal(err)
		}
		if got := decimalFloat(value); got != want {
			t.Errorf("decimalFloat(%s) = %v, want %v", input, got, want)
		}
	}
}
