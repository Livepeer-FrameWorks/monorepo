package auth

import (
	"fmt"
	"testing"
	"time"
)

func TestIsValidChainType(t *testing.T) {
	tests := []struct {
		chain string
		want  bool
	}{
		{"ethereum", true},
		{"base", true},
		{"arbitrum", true},
		{"solana", false},
		{"bitcoin", false},
		{"", false},
		{"ETHEREUM", false}, // case-sensitive
	}

	for _, tt := range tests {
		t.Run(tt.chain, func(t *testing.T) {
			if got := IsValidChainType(tt.chain); got != tt.want {
				t.Errorf("IsValidChainType(%q) = %v, want %v", tt.chain, got, tt.want)
			}
		})
	}
}

func TestIsEVMChain(t *testing.T) {
	tests := []struct {
		chain ChainType
		want  bool
	}{
		{ChainEthereum, true},
		{ChainBase, true},
		{ChainArbitrum, true},
		{"solana", false},
		{"unknown", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.chain), func(t *testing.T) {
			if got := IsEVMChain(tt.chain); got != tt.want {
				t.Errorf("IsEVMChain(%q) = %v, want %v", tt.chain, got, tt.want)
			}
		})
	}
}

func TestNormalizeAddress(t *testing.T) {
	t.Run("EVM chain normalizes address", func(t *testing.T) {
		addr, err := NormalizeAddress(ChainEthereum, "0xd8da6bf26964af9d7eed9e03e53415d37aa96045")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if addr != "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045" {
			t.Errorf("unexpected checksum address: %s", addr)
		}
	})

	t.Run("unsupported chain returns error", func(t *testing.T) {
		_, err := NormalizeAddress("solana", "someaddress")
		if err == nil {
			t.Error("expected error for unsupported chain")
		}
	})
}

func TestNormalizeEthAddress(t *testing.T) {
	tests := []struct {
		name    string
		address string
		want    string
		wantErr bool
	}{
		{
			name:    "vitalik lowercase",
			address: "0xd8da6bf26964af9d7eed9e03e53415d37aa96045",
			want:    "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
		},
		{
			name:    "already checksummed",
			address: "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
			want:    "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
		},
		{
			name:    "uppercase",
			address: "0xD8DA6BF26964AF9D7EED9E03E53415D37AA96045",
			want:    "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
		},
		{
			name:    "without 0x prefix",
			address: "d8da6bf26964af9d7eed9e03e53415d37aa96045",
			want:    "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
		},
		{
			name:    "too short",
			address: "0xd8da6bf269",
			wantErr: true,
		},
		{
			name:    "too long",
			address: "0xd8da6bf26964af9d7eed9e03e53415d37aa96045ab",
			wantErr: true,
		},
		{
			name:    "invalid hex",
			address: "0xg8da6bf26964af9d7eed9e03e53415d37aa96045",
			wantErr: true,
		},
		{
			name:    "empty",
			address: "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeEthAddress(tt.address)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateWalletMessageTimestamp(t *testing.T) {
	t.Run("valid timestamp within window", func(t *testing.T) {
		now := time.Now().UTC()
		msg := fmt.Sprintf("FrameWorks Login\nTimestamp: %s\nNonce: abc123", now.Format(time.RFC3339))
		if err := ValidateWalletMessageTimestamp(msg); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("expired timestamp", func(t *testing.T) {
		old := time.Now().UTC().Add(-10 * time.Minute)
		msg := fmt.Sprintf("FrameWorks Login\nTimestamp: %s\nNonce: abc123", old.Format(time.RFC3339))
		if err := ValidateWalletMessageTimestamp(msg); err == nil {
			t.Error("expected error for expired timestamp")
		}
	})

	t.Run("future timestamp", func(t *testing.T) {
		future := time.Now().UTC().Add(5 * time.Minute)
		msg := fmt.Sprintf("FrameWorks Login\nTimestamp: %s\nNonce: abc123", future.Format(time.RFC3339))
		if err := ValidateWalletMessageTimestamp(msg); err == nil {
			t.Error("expected error for future timestamp")
		}
	})

	t.Run("missing timestamp", func(t *testing.T) {
		msg := "FrameWorks Login\nNonce: abc123"
		if err := ValidateWalletMessageTimestamp(msg); err == nil {
			t.Error("expected error for missing timestamp")
		}
	})

	t.Run("invalid timestamp format", func(t *testing.T) {
		msg := "FrameWorks Login\nTimestamp: not-a-date\nNonce: abc123"
		if err := ValidateWalletMessageTimestamp(msg); err == nil {
			t.Error("expected error for invalid format")
		}
	})

	t.Run("slightly in past is valid", func(t *testing.T) {
		past := time.Now().UTC().Add(-2 * time.Minute)
		msg := fmt.Sprintf("FrameWorks Login\nTimestamp: %s\nNonce: abc123", past.Format(time.RFC3339))
		if err := ValidateWalletMessageTimestamp(msg); err != nil {
			t.Errorf("unexpected error for valid past timestamp: %v", err)
		}
	})
}

func TestGenerateWalletAuthMessage(t *testing.T) {
	nonce := "test-nonce-123"
	msg := GenerateWalletAuthMessage(nonce)

	if msg == "" {
		t.Error("message should not be empty")
	}

	// Should contain the nonce
	if !containsLine(msg, "Nonce: "+nonce) {
		t.Error("message should contain nonce")
	}

	// Should contain a timestamp
	if !containsLine(msg, "Timestamp: ") {
		t.Error("message should contain timestamp")
	}

	// Should start with the expected prefix
	if msg[:16] != "FrameWorks Login" {
		t.Errorf("unexpected message prefix: %q", msg[:16])
	}

	// Generated message should pass timestamp validation
	if err := ValidateWalletMessageTimestamp(msg); err != nil {
		t.Errorf("generated message failed validation: %v", err)
	}
}

func containsLine(msg, prefix string) bool {
	for _, line := range splitLines(msg) {
		if len(line) >= len(prefix) && line[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func TestValidChainTypesContainsExpected(t *testing.T) {
	expected := []ChainType{ChainEthereum, ChainBase, ChainArbitrum}
	if len(ValidChainTypes) != len(expected) {
		t.Errorf("ValidChainTypes has %d entries, expected %d", len(ValidChainTypes), len(expected))
	}
	for _, exp := range expected {
		found := false
		for _, valid := range ValidChainTypes {
			if valid == exp {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ValidChainTypes missing %q", exp)
		}
	}
}
