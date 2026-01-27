package handlers

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"frameworks/pkg/logging"

	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/google/uuid"
	"golang.org/x/crypto/sha3"
)

// HDWallet handles address derivation from extended public key (xpub).
// Private keys NEVER touch the server - only the xpub is stored.
// Sweep operations happen offline with the master seed.
type HDWallet struct {
	db     *sql.DB
	logger logging.Logger
}

// NewHDWallet creates a new HD wallet manager
func NewHDWallet(database *sql.DB, log logging.Logger) *HDWallet {
	return &HDWallet{
		db:     database,
		logger: log,
	}
}

// pubkeyToEthAddress converts an uncompressed public key to an Ethereum address.
// Algorithm: keccak256(pubkey_bytes)[12:32] formatted as 0x-prefixed hex
func pubkeyToEthAddress(pubkeyUncompressed []byte) string {
	// Uncompressed pubkey is 65 bytes: 0x04 + 32-byte X + 32-byte Y
	// For address derivation, we hash only X+Y (64 bytes), not the 0x04 prefix
	if len(pubkeyUncompressed) != 65 || pubkeyUncompressed[0] != 0x04 {
		return ""
	}

	// Keccak256 hash of X+Y coordinates
	hasher := sha3.NewLegacyKeccak256()
	hasher.Write(pubkeyUncompressed[1:]) // Skip 0x04 prefix
	hash := hasher.Sum(nil)

	// Last 20 bytes = Ethereum address
	address := hash[12:32]
	return "0x" + hex.EncodeToString(address)
}

// DeriveAddressFromXpub derives an Ethereum address from an extended public key.
// The xpub should be at path m/44'/60'/0'/0 (BIP44 external chain for Ethereum).
// We derive child key at index: xpub/{index} → address
func DeriveAddressFromXpub(xpub string, index uint32) (string, error) {
	// Parse the extended public key
	extKey, err := hdkeychain.NewKeyFromString(xpub)
	if err != nil {
		return "", fmt.Errorf("invalid xpub: %w", err)
	}

	// Ensure this is a public key (not a private key)
	if extKey.IsPrivate() {
		return "", fmt.Errorf("expected xpub but got xprv - never store private keys on server")
	}

	// Derive child key at index (non-hardened derivation from public key)
	childKey, err := extKey.Derive(index)
	if err != nil {
		return "", fmt.Errorf("failed to derive child key at index %d: %w", index, err)
	}

	// Get the public key
	pubKey, err := childKey.ECPubKey()
	if err != nil {
		return "", fmt.Errorf("failed to get public key: %w", err)
	}

	// Get uncompressed public key bytes (65 bytes: 0x04 + X + Y)
	pubkeyBytes := pubKey.SerializeUncompressed()

	// Convert to Ethereum address
	address := pubkeyToEthAddress(pubkeyBytes)
	if address == "" {
		return "", fmt.Errorf("failed to derive address from public key")
	}

	return address, nil
}

// GetNextDerivationIndex atomically gets and increments the next derivation index.
// Returns the index to use and the xpub.
func (hw *HDWallet) GetNextDerivationIndex() (uint32, string, error) {
	var index int
	var xpub string

	tx, err := hw.db.Begin()
	if err != nil {
		return 0, "", fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	// Get current state and increment atomically
	err = tx.QueryRow(`
		UPDATE purser.hd_wallet_state
		SET next_index = next_index + 1, updated_at = NOW()
		WHERE id = 1
		RETURNING next_index - 1, xpub
	`).Scan(&index, &xpub)

	if err == sql.ErrNoRows {
		return 0, "", fmt.Errorf("hd_wallet_state not initialized - run: INSERT INTO purser.hd_wallet_state (xpub) VALUES ('your-xpub')")
	}
	if err != nil {
		return 0, "", fmt.Errorf("failed to get next derivation index: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return 0, "", fmt.Errorf("failed to commit: %w", err)
	}

	return uint32(index), xpub, nil
}

// EnsureState ensures the HD wallet state row exists.
// If no row exists, it will initialize with the provided xpub.
func (hw *HDWallet) EnsureState(xpub string) (bool, error) {
	var existing string
	err := hw.db.QueryRow(`SELECT xpub FROM purser.hd_wallet_state WHERE id = 1`).Scan(&existing)
	if err == sql.ErrNoRows {
		if strings.TrimSpace(xpub) == "" {
			return false, fmt.Errorf("hd_wallet_state not initialized and HD_WALLET_XPUB not set")
		}
		_, err = hw.db.Exec(`INSERT INTO purser.hd_wallet_state (id, xpub) VALUES (1, $1)`, xpub)
		if err != nil {
			return false, fmt.Errorf("failed to initialize hd_wallet_state: %w", err)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to check hd_wallet_state: %w", err)
	}
	if strings.TrimSpace(xpub) != "" && strings.TrimSpace(existing) != strings.TrimSpace(xpub) {
		hw.logger.WithFields(logging.Fields{
			"existing_xpub": strings.TrimSpace(existing),
			"env_xpub":      strings.TrimSpace(xpub),
		}).Warn("HD wallet xpub mismatch; keeping existing database value")
	}
	return false, nil
}

// GenerateDepositAddress creates a new deposit address for invoice or prepaid.
// For invoices: purpose='invoice', invoiceID required
// For prepaid: purpose='prepaid', expectedAmountCents required
func (hw *HDWallet) GenerateDepositAddress(
	tenantID string,
	purpose string,
	invoiceID *string,
	expectedAmountCents *int64,
	asset string,
	expiresAt time.Time,
) (walletID string, address string, err error) {
	// Validate
	if purpose != "invoice" && purpose != "prepaid" {
		return "", "", fmt.Errorf("invalid purpose: %s", purpose)
	}
	if asset != "ETH" && asset != "USDC" && asset != "LPT" {
		return "", "", fmt.Errorf("invalid asset: %s (ETH, USDC, or LPT only)", asset)
	}
	if purpose == "invoice" && (invoiceID == nil || *invoiceID == "") {
		return "", "", fmt.Errorf("invoice_id required for invoice purpose")
	}
	if purpose == "prepaid" && (expectedAmountCents == nil || *expectedAmountCents <= 0) {
		return "", "", fmt.Errorf("expected_amount_cents required for prepaid purpose")
	}

	// Get next index and xpub
	derivationIndex, xpub, err := hw.GetNextDerivationIndex()
	if err != nil {
		return "", "", err
	}

	// Derive address
	address, err = DeriveAddressFromXpub(xpub, derivationIndex)
	if err != nil {
		return "", "", fmt.Errorf("failed to derive address: %w", err)
	}

	// Normalize to lowercase (Ethereum addresses are case-insensitive)
	address = strings.ToLower(address)

	walletID = uuid.New().String()

	var invoiceIDValue interface{}
	if invoiceID != nil {
		invoiceIDValue = *invoiceID
	}
	var expectedAmountValue interface{}
	if expectedAmountCents != nil {
		expectedAmountValue = *expectedAmountCents
	}

	_, err = hw.db.Exec(`
		INSERT INTO purser.crypto_wallets (
			id, tenant_id, purpose, invoice_id, expected_amount_cents,
			asset, wallet_address, derivation_index, status, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'active', $9)
	`,
		walletID, tenantID, purpose, invoiceIDValue, expectedAmountValue,
		asset, address, derivationIndex, expiresAt,
	)
	if err != nil {
		return "", "", fmt.Errorf("failed to insert crypto wallet: %w", err)
	}

	hw.logger.WithFields(logging.Fields{
		"wallet_id":        walletID,
		"tenant_id":        tenantID,
		"purpose":          purpose,
		"asset":            asset,
		"address":          address,
		"derivation_index": derivationIndex,
	}).Info("Generated deposit address")

	return walletID, address, nil
}

// InitializeHDWallet sets up the HD wallet state with an xpub.
// The xpub should be derived offline at BIP44 path m/44'/60'/0'/0 for Ethereum.
func (hw *HDWallet) InitializeHDWallet(xpub string, network string) error {
	extKey, err := hdkeychain.NewKeyFromString(xpub)
	if err != nil {
		return fmt.Errorf("invalid xpub: %w", err)
	}
	if extKey.IsPrivate() {
		return fmt.Errorf("CRITICAL: received xprv instead of xpub - never store private keys")
	}

	// Validate network
	var expectedNet *chaincfg.Params
	switch network {
	case "mainnet":
		expectedNet = &chaincfg.MainNetParams
	case "testnet":
		expectedNet = &chaincfg.TestNet3Params
	default:
		return fmt.Errorf("invalid network: %s", network)
	}

	if !extKey.IsForNet(expectedNet) {
		return fmt.Errorf("xpub network mismatch")
	}

	// Test derivation
	testAddr, err := DeriveAddressFromXpub(xpub, 0)
	if err != nil {
		return fmt.Errorf("failed to derive test address: %w", err)
	}

	_, err = hw.db.Exec(`
		INSERT INTO purser.hd_wallet_state (id, xpub, network, next_index)
		VALUES (1, $1, $2, 0)
		ON CONFLICT (id) DO UPDATE SET xpub = $1, network = $2, updated_at = NOW()
	`, xpub, network)
	if err != nil {
		return fmt.Errorf("failed to initialize hd_wallet_state: %w", err)
	}

	hw.logger.WithFields(logging.Fields{
		"network":      network,
		"test_address": testAddr,
	}).Info("HD wallet initialized")

	return nil
}

// ValidateXpub checks if stored xpub can derive addresses
func (hw *HDWallet) ValidateXpub() error {
	var xpub string
	err := hw.db.QueryRow(`SELECT xpub FROM purser.hd_wallet_state WHERE id = 1`).Scan(&xpub)
	if err == sql.ErrNoRows {
		return fmt.Errorf("hd_wallet_state not initialized")
	}
	if err != nil {
		return err
	}

	extKey, err := hdkeychain.NewKeyFromString(xpub)
	if err != nil {
		return fmt.Errorf("invalid xpub: %w", err)
	}
	if extKey.IsPrivate() {
		return fmt.Errorf("CRITICAL: stored key is xprv not xpub")
	}

	_, err = DeriveAddressFromXpub(xpub, 0)
	return err
}
