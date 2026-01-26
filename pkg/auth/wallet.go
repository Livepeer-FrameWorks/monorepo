package auth

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"golang.org/x/crypto/sha3"
)

// ============================================================================
// CHAIN TYPES
// ============================================================================

// ChainType represents supported blockchain networks
type ChainType string

const (
	ChainEthereum ChainType = "ethereum" // Ethereum mainnet
	ChainBase     ChainType = "base"     // Base L2 (Coinbase) - primary for x402
	ChainArbitrum ChainType = "arbitrum" // Arbitrum L2
)

// ValidChainTypes lists all supported chain types
var ValidChainTypes = []ChainType{
	ChainEthereum,
	ChainBase,
	ChainArbitrum,
}

// IsValidChainType checks if a chain type is supported
func IsValidChainType(chain string) bool {
	for _, valid := range ValidChainTypes {
		if ChainType(chain) == valid {
			return true
		}
	}
	return false
}

// IsEVMChain returns true if the chain uses Ethereum-style addresses
func IsEVMChain(chain ChainType) bool {
	switch chain {
	case ChainEthereum, ChainBase, ChainArbitrum:
		return true
	default:
		return false
	}
}

// NormalizeAddress normalizes an address for the given chain type
func NormalizeAddress(chain ChainType, address string) (string, error) {
	if IsEVMChain(chain) {
		return NormalizeEthAddress(address)
	}
	return "", fmt.Errorf("unsupported chain type: %s", chain)
}

// ============================================================================
// WALLET AUTHENTICATION
// ============================================================================

// WalletMessage represents a signed message for authentication
type WalletMessage struct {
	// Wallet address (0x prefixed hex)
	Address string
	// Message that was signed
	Message string
	// Signature in hex format (0x prefixed, 65 bytes: R|S|V)
	Signature string
}

// VerifyWalletAuth verifies an Ethereum wallet authentication attempt
// Uses EIP-191 personal_sign format
func VerifyWalletAuth(msg WalletMessage) (bool, error) {
	// Normalize address
	normalizedAddr, err := NormalizeEthAddress(msg.Address)
	if err != nil {
		return false, fmt.Errorf("invalid address format: %w", err)
	}

	// Verify the message hasn't expired (5 minute window)
	if err := ValidateWalletMessageTimestamp(msg.Message); err != nil {
		return false, err
	}

	// Verify signature
	return VerifyEthSignature(normalizedAddr, msg.Message, msg.Signature)
}

// GenerateWalletAuthMessage creates a message for wallet signing
// Format: "FrameWorks Login\nTimestamp: {iso}\nNonce: {random}"
func GenerateWalletAuthMessage(nonce string) string {
	timestamp := time.Now().UTC().Format(time.RFC3339)
	return fmt.Sprintf("FrameWorks Login\nTimestamp: %s\nNonce: %s", timestamp, nonce)
}

// ValidateWalletMessageTimestamp checks if the message timestamp is within 5 minutes
func ValidateWalletMessageTimestamp(message string) error {
	lines := strings.Split(message, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "Timestamp: ") {
			timestampStr := strings.TrimPrefix(line, "Timestamp: ")
			timestamp, err := time.Parse(time.RFC3339, timestampStr)
			if err != nil {
				return fmt.Errorf("invalid timestamp format: %w", err)
			}

			age := time.Since(timestamp)
			if age < -1*time.Minute {
				return fmt.Errorf("message timestamp is in the future")
			}
			if age > 5*time.Minute {
				return fmt.Errorf("message timestamp expired (older than 5 minutes)")
			}
			return nil
		}
	}
	return fmt.Errorf("message missing timestamp")
}

// VerifyEthSignature verifies an EIP-191 personal_sign signature
func VerifyEthSignature(address, message, signature string) (bool, error) {
	// Decode signature
	sig, err := decodeHexSignature(signature)
	if err != nil {
		return false, fmt.Errorf("invalid signature format: %w", err)
	}
	if len(sig) != 65 {
		return false, fmt.Errorf("signature must be 65 bytes, got %d", len(sig))
	}

	// EIP-191: Hash the prefixed message
	prefixedMessage := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(message), message)
	hash := keccak256([]byte(prefixedMessage))

	// Extract R, S, V from signature
	r := sig[0:32]
	s := sig[32:64]
	v := sig[64]

	// Transform V from 27/28 to 0/1 if needed
	if v >= 27 {
		v -= 27
	}
	if v > 1 {
		return false, fmt.Errorf("invalid recovery id: %d", v)
	}

	// Recover public key
	pubKey, _, err := ecdsa.RecoverCompact(makeCompactSig(r, s, v), hash)
	if err != nil {
		return false, fmt.Errorf("failed to recover public key: %w", err)
	}

	// Derive Ethereum address from public key
	recoveredAddr := pubKeyToEthAddress(pubKey)

	// Compare addresses (case-insensitive)
	return strings.EqualFold(recoveredAddr, address), nil
}

// makeCompactSig creates a compact signature for btcec recovery
// btcec expects: [V (1 byte, 27-30)] + [R (32 bytes)] + [S (32 bytes)]
func makeCompactSig(r, s []byte, v byte) []byte {
	// btcec uses 27 + recovery_id + (4 if compressed)
	// For uncompressed pubkey recovery: 27 + v
	compact := make([]byte, 65)
	compact[0] = 27 + v
	copy(compact[1:33], r)
	copy(compact[33:65], s)
	return compact
}

// pubKeyToEthAddress derives an Ethereum address from a secp256k1 public key
func pubKeyToEthAddress(pubKey *btcec.PublicKey) string {
	// Ethereum uses uncompressed pubkey without the 0x04 prefix
	uncompressed := pubKey.SerializeUncompressed()
	// Hash the pubkey (excluding the 0x04 prefix byte)
	hash := keccak256(uncompressed[1:])
	// Address is last 20 bytes of hash
	addr := hash[12:]
	return toChecksumAddress(hex.EncodeToString(addr))
}

// NormalizeEthAddress converts an Ethereum address to checksum format
func NormalizeEthAddress(address string) (string, error) {
	addr := strings.TrimPrefix(strings.ToLower(address), "0x")
	if len(addr) != 40 {
		return "", fmt.Errorf("ethereum address must be 40 hex characters")
	}
	if _, err := hex.DecodeString(addr); err != nil {
		return "", fmt.Errorf("invalid hex in address: %w", err)
	}
	return toChecksumAddress(addr), nil
}

// toChecksumAddress applies EIP-55 checksum to an address
func toChecksumAddress(addr string) string {
	addr = strings.ToLower(addr)
	hash := keccak256([]byte(addr))

	result := make([]byte, 42)
	result[0] = '0'
	result[1] = 'x'

	for i := 0; i < 40; i++ {
		c := addr[i]
		hashNibble := hash[i/2]
		if i%2 == 0 {
			hashNibble >>= 4
		}
		hashNibble &= 0x0f

		if hashNibble >= 8 && c >= 'a' && c <= 'f' {
			result[i+2] = c - 32 // uppercase
		} else {
			result[i+2] = c
		}
	}
	return string(result)
}

// keccak256 computes Keccak-256 hash
func keccak256(data []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	return h.Sum(nil)
}

// decodeHexSignature decodes a hex-encoded signature
func decodeHexSignature(sig string) ([]byte, error) {
	sig = strings.TrimPrefix(sig, "0x")
	sig = strings.TrimPrefix(sig, "0X")
	return hex.DecodeString(sig)
}
