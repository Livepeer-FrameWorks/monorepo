package auth

import (
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
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

	t.Run("exactly 1 minute in future fails", func(t *testing.T) {
		future := time.Now().UTC().Add(61 * time.Second)
		msg := fmt.Sprintf("FrameWorks Login\nTimestamp: %s\nNonce: abc123", future.Format(time.RFC3339))
		if err := ValidateWalletMessageTimestamp(msg); err == nil {
			t.Error("expected error for timestamp 1+ minute in future")
		}
	})

	t.Run("just under 1 minute in future passes", func(t *testing.T) {
		future := time.Now().UTC().Add(59 * time.Second)
		msg := fmt.Sprintf("FrameWorks Login\nTimestamp: %s\nNonce: abc123", future.Format(time.RFC3339))
		if err := ValidateWalletMessageTimestamp(msg); err != nil {
			t.Errorf("unexpected error for timestamp under 1 minute in future: %v", err)
		}
	})

	t.Run("just under 5 minutes in past passes", func(t *testing.T) {
		past := time.Now().UTC().Add(-4*time.Minute - 58*time.Second)
		msg := fmt.Sprintf("FrameWorks Login\nTimestamp: %s\nNonce: abc123", past.Format(time.RFC3339))
		if err := ValidateWalletMessageTimestamp(msg); err != nil {
			t.Errorf("unexpected error for timestamp under 5 minutes old: %v", err)
		}
	})

	t.Run("just over 5 minutes in past fails", func(t *testing.T) {
		past := time.Now().UTC().Add(-5*time.Minute - 1*time.Second)
		msg := fmt.Sprintf("FrameWorks Login\nTimestamp: %s\nNonce: abc123", past.Format(time.RFC3339))
		if err := ValidateWalletMessageTimestamp(msg); err == nil {
			t.Error("expected error for timestamp over 5 minutes old")
		}
	})
}

func TestValidateWalletMessageTimestampBoundaries(t *testing.T) {
	t.Run("just under 1 minute in the future is allowed", func(t *testing.T) {
		future := time.Now().UTC().Add(59 * time.Second)
		msg := fmt.Sprintf("FrameWorks Login\nTimestamp: %s\nNonce: abc123", future.Format(time.RFC3339))
		if err := ValidateWalletMessageTimestamp(msg); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("one minute and a second in the future is rejected", func(t *testing.T) {
		future := time.Now().UTC().Add(1*time.Minute + time.Second)
		msg := fmt.Sprintf("FrameWorks Login\nTimestamp: %s\nNonce: abc123", future.Format(time.RFC3339))
		if err := ValidateWalletMessageTimestamp(msg); err == nil {
			t.Error("expected error for future timestamp beyond 1 minute")
		}
	})

	t.Run("just under 5 minutes old is allowed", func(t *testing.T) {
		past := time.Now().UTC().Add(-5*time.Minute + time.Second)
		msg := fmt.Sprintf("FrameWorks Login\nTimestamp: %s\nNonce: abc123", past.Format(time.RFC3339))
		if err := ValidateWalletMessageTimestamp(msg); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("five minutes and one second old is rejected", func(t *testing.T) {
		past := time.Now().UTC().Add(-5*time.Minute - time.Second)
		msg := fmt.Sprintf("FrameWorks Login\nTimestamp: %s\nNonce: abc123", past.Format(time.RFC3339))
		if err := ValidateWalletMessageTimestamp(msg); err == nil {
			t.Error("expected error for expired timestamp beyond 5 minutes")
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

func TestVerifyEthSignature(t *testing.T) {
	privateKeyHex := "4c0883a69102937d6231471b5dbb6204fe51296170827922b7a56c91b8b56d09"

	t.Run("valid signature with v=27", func(t *testing.T) {
		address, message, signature := signatureForRecoveryID(t, privateKeyHex, 27)
		ok, err := VerifyEthSignature(address, message, signature)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Fatal("expected signature to verify")
		}
	})

	t.Run("valid signature with v=28", func(t *testing.T) {
		address, message, signature := signatureForRecoveryID(t, privateKeyHex, 28)
		ok, err := VerifyEthSignature(address, message, signature)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Fatal("expected signature to verify")
		}
	})

	t.Run("signature from wrong address", func(t *testing.T) {
		_, message, signature := signatureForRecoveryID(t, privateKeyHex, 27)
		otherAddress, _, _ := signatureForRecoveryID(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 27)
		ok, err := VerifyEthSignature(otherAddress, message, signature)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ok {
			t.Fatal("expected signature mismatch")
		}
	})

	t.Run("invalid signature length", func(t *testing.T) {
		_, err := VerifyEthSignature("0x0000000000000000000000000000000000000000", "msg", "0xdeadbeef")
		if err == nil {
			t.Fatal("expected error for invalid signature length")
		}
	})

	t.Run("invalid V value", func(t *testing.T) {
		sig := make([]byte, 65)
		sig[64] = 29
		_, err := VerifyEthSignature("0x0000000000000000000000000000000000000000", "msg", "0x"+hex.EncodeToString(sig))
		if err == nil {
			t.Fatal("expected error for invalid recovery id")
		}
	})

	t.Run("malformed hex signature", func(t *testing.T) {
		_, err := VerifyEthSignature("0x0000000000000000000000000000000000000000", "msg", "0xnot-hex")
		if err == nil {
			t.Fatal("expected error for malformed signature")
		}
	})
}

func TestVerifyWalletAuth(t *testing.T) {
	privateKeyHex := "4c0883a69102937d6231471b5dbb6204fe51296170827922b7a56c91b8b56d09"

	t.Run("invalid address format", func(t *testing.T) {
		msg := WalletMessage{
			Address:   "not-an-address",
			Message:   "FrameWorks Login\nTimestamp: 2025-01-01T00:00:00Z\nNonce: abc123",
			Signature: "0x",
		}
		if _, err := VerifyWalletAuth(msg); err == nil {
			t.Fatal("expected error for invalid address")
		}
	})

	t.Run("expired message", func(t *testing.T) {
		address, _, _ := signatureForRecoveryID(t, privateKeyHex, 27)
		expired := time.Now().UTC().Add(-10 * time.Minute)
		message := fmt.Sprintf("FrameWorks Login\nTimestamp: %s\nNonce: abc123", expired.Format(time.RFC3339))
		_, signature := signWalletMessage(t, privateKeyHex, message)
		msg := WalletMessage{
			Address:   address,
			Message:   message,
			Signature: signature,
		}
		if _, err := VerifyWalletAuth(msg); err == nil {
			t.Fatal("expected error for expired message")
		}
	})

	t.Run("valid auth flow", func(t *testing.T) {
		message := messageWithTimestamp(time.Now().UTC())
		address, signature := signWalletMessage(t, privateKeyHex, message)
		msg := WalletMessage{
			Address:   address,
			Message:   message,
			Signature: signature,
		}
		ok, err := VerifyWalletAuth(msg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Fatal("expected wallet auth to verify")
		}
	})
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

func messageWithTimestamp(ts time.Time) string {
	return fmt.Sprintf("FrameWorks Login\nTimestamp: %s\nNonce: abc123", ts.Format(time.RFC3339))
}

func signatureForRecoveryID(t *testing.T, privateKeyHex string, want byte) (string, string, string) {
	t.Helper()

	const maxAttempts = 200
	for i := 0; i < maxAttempts; i++ {
		message := fmt.Sprintf("FrameWorks Login\nTimestamp: 2025-01-01T00:00:00Z\nNonce: %d", i)
		address, signature, recovery := signMessage(t, privateKeyHex, message)
		if recovery == want {
			return address, message, signature
		}
	}

	t.Fatalf("unable to produce signature with recovery id %d", want)
	return "", "", ""
}

func signWalletMessage(t *testing.T, privateKeyHex, message string) (string, string) {
	address, signature, _ := signMessage(t, privateKeyHex, message)
	return address, signature
}

func signMessage(t *testing.T, privateKeyHex, message string) (string, string, byte) {
	t.Helper()

	keyBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		t.Fatalf("failed to decode private key: %v", err)
	}
	privKey, _ := btcec.PrivKeyFromBytes(keyBytes)

	prefixedMessage := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(message), message)
	hash := keccak256([]byte(prefixedMessage))

	compactSig := ecdsa.SignCompact(privKey, hash, false)
	if len(compactSig) != 65 {
		t.Fatalf("unexpected compact signature length: %d", len(compactSig))
	}

	r := compactSig[1:33]
	s := compactSig[33:65]
	recoveryID := compactSig[0]
	signature := append(append([]byte{}, r...), s...)
	signature = append(signature, recoveryID)

	address := pubKeyToEthAddress(privKey.PubKey())
	return address, "0x" + hex.EncodeToString(signature), recoveryID
}
