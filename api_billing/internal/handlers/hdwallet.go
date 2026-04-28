package handlers

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"frameworks/pkg/logging"

	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
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
	ctx := context.Background()
	tx, err := hw.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	index, xpub, err := hw.GetNextDerivationIndexTx(ctx, tx)
	if err != nil {
		return 0, "", err
	}

	if err = tx.Commit(); err != nil {
		return 0, "", fmt.Errorf("failed to commit: %w", err)
	}

	return uint32(index), xpub, nil
}

// GetNextDerivationIndexTx atomically gets and increments the next derivation index within a transaction.
// Returns the index to use and the xpub.
func (hw *HDWallet) GetNextDerivationIndexTx(ctx context.Context, tx *sql.Tx) (uint32, string, error) {
	var index int
	var xpub string

	err := tx.QueryRowContext(ctx, `
		UPDATE purser.hd_wallet_state
		SET next_index = next_index + 1, updated_at = NOW()
		WHERE id = 1
		RETURNING next_index - 1, xpub
	`).Scan(&index, &xpub)

	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", fmt.Errorf("hd_wallet_state not initialized - run: INSERT INTO purser.hd_wallet_state (xpub) VALUES ('your-xpub')")
	}
	if err != nil {
		return 0, "", fmt.Errorf("failed to get next derivation index: %w", err)
	}

	return uint32(index), xpub, nil
}

// GetNextNonZeroDerivationIndexTx allocates a derivation index within a transaction, skipping index 0.
func (hw *HDWallet) GetNextNonZeroDerivationIndexTx(ctx context.Context, tx *sql.Tx) (uint32, string, error) {
	for {
		index, xpub, err := hw.GetNextDerivationIndexTx(ctx, tx)
		if err != nil {
			return 0, "", err
		}
		if index != 0 {
			return index, xpub, nil
		}
	}
}

// EnsureState ensures the HD wallet state row exists.
// If no row exists, it will initialize with the provided xpub.
func (hw *HDWallet) EnsureState(ctx context.Context, xpub string) (bool, error) {
	var existing string
	err := hw.db.QueryRowContext(ctx, `SELECT xpub FROM purser.hd_wallet_state WHERE id = 1`).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		if strings.TrimSpace(xpub) == "" {
			return false, fmt.Errorf("hd_wallet_state not initialized and HD_WALLET_XPUB not set")
		}
		_, err = hw.db.ExecContext(ctx, `INSERT INTO purser.hd_wallet_state (id, xpub) VALUES (1, $1)`, xpub)
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

// DepositAddressParams describes a request to allocate an HD-derived deposit
// address. Purpose drives which optional fields are required:
//   - "invoice"  → InvoiceID required, ExpectedAmountCents/Quote unused.
//   - "prepaid"  → ExpectedAmountCents and Quote required.
type DepositAddressParams struct {
	TenantID  string
	Purpose   string // "invoice" | "prepaid"
	Asset     string // "ETH" | "USDC" | "LPT"
	Network   string // "ethereum" | "arbitrum" | "base" | "*-sepolia"
	ExpiresAt time.Time

	InvoiceID           *string
	ExpectedAmountCents *int64

	Quote *DepositQuote // nil for invoice purpose
}

// DepositQuote is the locked quote persisted on a prepaid wallet row at
// CreateCryptoTopup time so the monitor can credit deterministically when
// the deposit confirms.
type DepositQuote struct {
	ExpectedAmountBaseUnits *big.Int         // Token base units the user must send (NOT NULL)
	QuotedPriceUSD          decimal.Decimal  // USD per 1 whole token
	QuotedUSDToEURRate      *decimal.Decimal // Populated when CreditedAmountCurrency == "EUR"
	QuotedAt                time.Time
	QuoteSource             string // "chainlink" | "one_to_one"
	CreditedAmountCurrency  string // "USD" | "EUR" — what the prepaid balance is denominated in
}

// GenerateDepositAddress allocates an HD-derived deposit address and inserts
// a crypto_wallets row. See DepositAddressParams for the shape.
func (hw *HDWallet) GenerateDepositAddress(p DepositAddressParams) (walletID string, address string, err error) {
	if p.Purpose != "invoice" && p.Purpose != "prepaid" {
		return "", "", fmt.Errorf("invalid purpose: %s", p.Purpose)
	}
	if p.Asset != "ETH" && p.Asset != "USDC" && p.Asset != "LPT" {
		return "", "", fmt.Errorf("invalid asset: %s (ETH, USDC, or LPT only)", p.Asset)
	}
	if p.Network == "" {
		return "", "", fmt.Errorf("network is required")
	}
	if p.Purpose == "invoice" && (p.InvoiceID == nil || *p.InvoiceID == "") {
		return "", "", fmt.Errorf("invoice_id required for invoice purpose")
	}
	if p.Purpose == "prepaid" {
		if p.ExpectedAmountCents == nil || *p.ExpectedAmountCents <= 0 {
			return "", "", fmt.Errorf("expected_amount_cents required for prepaid purpose")
		}
		if p.Quote == nil {
			return "", "", fmt.Errorf("quote required for prepaid purpose")
		}
		if p.Quote.ExpectedAmountBaseUnits == nil || p.Quote.ExpectedAmountBaseUnits.Sign() <= 0 {
			return "", "", fmt.Errorf("quote.ExpectedAmountBaseUnits must be positive")
		}
		if p.Quote.QuoteSource == "" {
			return "", "", fmt.Errorf("quote.QuoteSource required")
		}
		if p.Quote.CreditedAmountCurrency == "" {
			return "", "", fmt.Errorf("quote.CreditedAmountCurrency required")
		}
		if p.Quote.CreditedAmountCurrency == "EUR" && p.Quote.QuotedUSDToEURRate == nil {
			return "", "", fmt.Errorf("quote.QuotedUSDToEURRate required when CreditedAmountCurrency is EUR")
		}
	}

	ctx := context.Background()
	tx, err := hw.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	derivationIndex, xpub, err := hw.GetNextNonZeroDerivationIndexTx(ctx, tx)
	if err != nil {
		return "", "", err
	}

	address, err = DeriveAddressFromXpub(xpub, derivationIndex)
	if err != nil {
		return "", "", fmt.Errorf("failed to derive address: %w", err)
	}
	address = strings.ToLower(address)
	walletID = uuid.New().String()

	var (
		invoiceIDValue          any
		expectedAmountValue     any
		expectedAmountBaseUnits any
		quotedPriceUSD          any
		quotedUSDToEURRate      any
		quotedAt                any
		quoteSource             any
		creditedAmountCurrency  any
	)
	if p.InvoiceID != nil {
		invoiceIDValue = *p.InvoiceID
	}
	if p.ExpectedAmountCents != nil {
		expectedAmountValue = *p.ExpectedAmountCents
	}
	if p.Quote != nil {
		expectedAmountBaseUnits = p.Quote.ExpectedAmountBaseUnits.String()
		quotedPriceUSD = p.Quote.QuotedPriceUSD.String()
		if p.Quote.QuotedUSDToEURRate != nil {
			quotedUSDToEURRate = p.Quote.QuotedUSDToEURRate.String()
		}
		quotedAt = p.Quote.QuotedAt
		quoteSource = p.Quote.QuoteSource
		creditedAmountCurrency = p.Quote.CreditedAmountCurrency
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO purser.crypto_wallets (
			id, tenant_id, purpose, invoice_id, expected_amount_cents,
			asset, network, wallet_address, derivation_index, expires_at,
			expected_amount_base_units, quoted_price_usd, quoted_usd_to_eur_rate,
			quoted_at, quote_source, credited_amount_currency
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	`,
		walletID, p.TenantID, p.Purpose, invoiceIDValue, expectedAmountValue,
		p.Asset, p.Network, address, derivationIndex, p.ExpiresAt,
		expectedAmountBaseUnits, quotedPriceUSD, quotedUSDToEURRate,
		quotedAt, quoteSource, creditedAmountCurrency,
	)
	if err != nil {
		return "", "", fmt.Errorf("failed to insert crypto wallet: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return "", "", fmt.Errorf("failed to commit: %w", err)
	}

	hw.logger.WithFields(logging.Fields{
		"wallet_id":        walletID,
		"tenant_id":        p.TenantID,
		"purpose":          p.Purpose,
		"asset":            p.Asset,
		"network":          p.Network,
		"address":          address,
		"derivation_index": derivationIndex,
	}).Info("Generated deposit address")

	return walletID, address, nil
}

// InitializeHDWallet sets up the HD wallet state with an xpub.
// The xpub should be derived offline at BIP44 path m/44'/60'/0'/0 for Ethereum.
func (hw *HDWallet) InitializeHDWallet(ctx context.Context, xpub string, network string) error {
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

	_, err = hw.db.ExecContext(ctx, `
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
func (hw *HDWallet) ValidateXpub(ctx context.Context) error {
	var xpub string
	err := hw.db.QueryRowContext(ctx, `SELECT xpub FROM purser.hd_wallet_state WHERE id = 1`).Scan(&xpub)
	if errors.Is(err, sql.ErrNoRows) {
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
