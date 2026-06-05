package handlers

import (
	"math"
	"testing"
)

func floatNear(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestParseEthereumAmount(t *testing.T) {
	cm := &CryptoMonitor{}
	cases := []struct {
		name    string
		value   string
		want    float64
		wantErr bool
	}{
		{"one ether", "1000000000000000000", 1.0, false}, // 1e18 wei
		{"half ether", "500000000000000000", 0.5, false},
		{"zero", "0", 0, false},
		{"not a number", "abc", 0, true},
		{"empty", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cm.parseEthereumAmount(tc.value)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !floatNear(got, tc.want) {
				t.Errorf("parseEthereumAmount(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestParseTokenAmount(t *testing.T) {
	cm := &CryptoMonitor{}
	cases := []struct {
		name    string
		value   string
		asset   string
		want    float64
		wantErr bool
	}{
		{"USDC 6 decimals", "1000000", "USDC", 1.0, false}, // 1e6 base units
		{"USDC fractional", "2500000", "USDC", 2.5, false},
		{"LPT 18 decimals", "1000000000000000000", "LPT", 1.0, false},
		{"unknown token", "1000000", "DAI", 0, true},
		{"invalid value", "xyz", "USDC", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cm.parseTokenAmount(tc.value, tc.asset)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for value=%q asset=%q", tc.value, tc.asset)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !floatNear(got, tc.want) {
				t.Errorf("parseTokenAmount(%q, %q) = %v, want %v", tc.value, tc.asset, got, tc.want)
			}
		})
	}
}

func TestParseTransactionAmount_DispatchesByAsset(t *testing.T) {
	cm := &CryptoMonitor{}
	// ETH dispatches to the 18-decimal wei path.
	if got, err := cm.parseTransactionAmount("1000000000000000000", "ETH"); err != nil || !floatNear(got, 1.0) {
		t.Errorf("ETH: got %v, err %v; want 1.0", got, err)
	}
	// USDC dispatches to the 6-decimal token path.
	if got, err := cm.parseTransactionAmount("1000000", "USDC"); err != nil || !floatNear(got, 1.0) {
		t.Errorf("USDC: got %v, err %v; want 1.0", got, err)
	}
	// LPT dispatches to the 18-decimal token path.
	if got, err := cm.parseTransactionAmount("1000000000000000000", "LPT"); err != nil || !floatNear(got, 1.0) {
		t.Errorf("LPT: got %v, err %v; want 1.0", got, err)
	}
	// Unknown asset is rejected — never silently treated as a known unit.
	if _, err := cm.parseTransactionAmount("1000000", "BTC"); err == nil {
		t.Error("expected error for unknown asset BTC")
	}
}

func TestParseTransactionBaseUnits(t *testing.T) {
	// This is the authoritative money-comparison path (exact big.Int), so it
	// must reject anything that isn't a base-10 integer string outright rather
	// than coercing to a lossy value.
	got, err := parseTransactionBaseUnits("15000000000000000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.String() != "15000000000000000" {
		t.Errorf("got %s, want 15000000000000000", got.String())
	}

	for _, bad := range []string{"", "abc", "1.5", "0x10", "1e9"} {
		if _, err := parseTransactionBaseUnits(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}
