package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestMoneyAuditResourceClassification(t *testing.T) {
	tests := map[string]string{
		" viewer://stream/1 ": "viewer",
		"GRAPHQL://query":     "graphql",
		"mcp://tool":          "mcp",
		"/api/playback":       "http",
		"invoice:pay":         "api",
	}
	for input, want := range tests {
		if got := classifyX402Resource(input); got != want {
			t.Errorf("classifyX402Resource(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMoneyAuditNetworkIdentifiers(t *testing.T) {
	if got := x402USDCDomainName(NetworkConfig{}); got != "USD Coin" {
		t.Fatalf("default USDC domain = %q", got)
	}
	if got := x402USDCDomainName(NetworkConfig{USDCDomainName: "USDC"}); got != "USDC" {
		t.Fatalf("configured USDC domain = %q", got)
	}
	if got, err := x402CAIP2Network(NetworkConfig{Name: "base", ChainID: 8453}); err != nil || got != "eip155:8453" {
		t.Fatalf("CAIP-2 = %q, %v", got, err)
	}
	if _, err := x402CAIP2Network(NetworkConfig{Name: "broken"}); err == nil {
		t.Fatal("zero chain id was accepted")
	}
}

func TestMoneyAuditInvoiceAndClaimIdentifiers(t *testing.T) {
	t.Setenv("WEBAPP_PUBLIC_URL", " https://app.example.test/ ")
	if got := invoiceBillingURL("invoice / 1"); got != "https://app.example.test/account/billing?invoice=invoice+%2F+1" {
		t.Fatalf("invoice URL = %q", got)
	}
	claim := invoiceEmailClaimID("tenant-1", "message-1")
	tenantID, messageID, err := parseInvoiceEmailClaimID(claim)
	if err != nil || tenantID != "tenant-1" || messageID != "message-1" {
		t.Fatalf("claim round trip = %q/%q, %v", tenantID, messageID, err)
	}
	for _, invalid := range []string{"", "tenant", "/message", "tenant/"} {
		if _, _, err := parseInvoiceEmailClaimID(invalid); err == nil {
			t.Errorf("invalid claim %q was accepted", invalid)
		}
	}
}

func TestMoneyAuditStateAndAmountHelpers(t *testing.T) {
	if finalizedInvoiceStatus(decimal.Zero) != "paid" || finalizedInvoiceStatus(decimal.NewFromInt(1)) != "pending" {
		t.Fatal("invoice final status does not distinguish zero from collectible value")
	}
	for _, value := range []string{"0x0", "0x00", "garbage"} {
		if !isZeroHex(value) {
			t.Errorf("isZeroHex(%q) = false", value)
		}
	}
	if isZeroHex("0x1") {
		t.Fatal("non-zero quantity classified as zero")
	}
	if !legacyHasJointProcessingKeys(map[string]float64{"video:h264": 1}) || legacyHasJointProcessingKeys(map[string]float64{"video": 1}) {
		t.Fatal("legacy joint-processing key detection is inconsistent")
	}
	if !legacyIsJointProcessingKey("video:h264") || legacyIsJointProcessingKey("video") {
		t.Fatal("legacy processing key classification is inconsistent")
	}
}

func TestUsageDimensionKeyIsStableAndCollisionSensitive(t *testing.T) {
	a := usageDimensionKey([]byte(`{"cluster":"a"}`))
	b := usageDimensionKey([]byte(`{"cluster":"a"}`))
	c := usageDimensionKey([]byte(`{"cluster":"b"}`))
	if a != b || a == c || len(a) != 64 {
		t.Fatalf("dimension hashes = %q %q %q", a, b, c)
	}
}

func TestNextUTCStartIsFutureAtRequestedHour(t *testing.T) {
	for _, hour := range []int{0, 9, 23} {
		next := nextUTCStart(hour)
		if !next.After(time.Now().UTC()) || next.Location() != time.UTC || next.Hour() != hour || next.Minute() != 0 {
			t.Fatalf("nextUTCStart(%d) = %v", hour, next)
		}
	}
}

func TestNetworkRPCEndpointEnvironmentOverride(t *testing.T) {
	const key = "PURSER_TEST_RPC_ENDPOINT"
	t.Setenv(key, "")
	network := NetworkConfig{RPCEndpointEnv: key}
	if got := network.GetRPCEndpoint(); got != "" {
		t.Fatalf("unset endpoint = %q", got)
	}
	t.Setenv(key, " https://rpc.example.test ")
	if got := network.GetRPCEndpoint(); strings.TrimSpace(got) != "https://rpc.example.test" {
		t.Fatalf("environment endpoint = %q", got)
	}
}
