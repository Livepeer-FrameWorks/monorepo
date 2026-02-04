package handlers

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/billing"
	"frameworks/pkg/config"
	"frameworks/pkg/countries"
	"frameworks/pkg/geoip"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"golang.org/x/crypto/sha3"
)

// ecbRateCache holds the cached EUR/USD exchange rate
var ecbRateCache struct {
	sync.RWMutex
	rate      float64
	fetchedAt time.Time
}

const ecbRateCacheTTL = 24 * time.Hour

// X402Handler handles x402 payment verification and settlement
// Supports multiple networks (Base, Arbitrum) for x402 settlement
type X402Handler struct {
	db               *sql.DB
	logger           logging.Logger
	hdwallet         *HDWallet
	gasWalletPrivKey string // Single privkey = same address on all EVM chains
	gasWalletAddress string // Derived from privkey
	includeTestnets  bool   // Whether to accept testnet payments

	// Supplier info for invoicing (required)
	supplierName    string
	supplierAddress string
	supplierVAT     string

	// Commodore client for cache invalidation after balance changes
	commodoreClient CommodoreClient
}

// NewX402Handler creates a new x402 payment handler
func NewX402Handler(database *sql.DB, log logging.Logger, hdwallet *HDWallet, commodoreClient CommodoreClient) *X402Handler {
	privKey := os.Getenv("X402_GAS_WALLET_PRIVKEY")
	gasAddr := os.Getenv("X402_GAS_WALLET_ADDRESS")
	includeTestnets := os.Getenv("X402_INCLUDE_TESTNETS") == "true"

	// If address not provided but privkey is, derive it
	if gasAddr == "" && privKey != "" {
		addr, err := deriveAddressFromPrivKey(privKey)
		if err == nil {
			gasAddr = addr
		}
	}

	// Supplier info is optional - only needed for simplified invoicing
	supplierName := config.GetEnv("SUPPLIER_NAME", "")
	supplierAddress := config.GetEnv("SUPPLIER_ADDRESS", "")
	supplierVAT := config.GetEnv("SUPPLIER_VAT_NUMBER", "")
	if supplierName == "" || supplierAddress == "" || supplierVAT == "" {
		log.Warn("x402 supplier info incomplete - simplified invoicing disabled (set SUPPLIER_NAME, SUPPLIER_ADDRESS, SUPPLIER_VAT_NUMBER)")
	}

	return &X402Handler{
		db:               database,
		logger:           log,
		hdwallet:         hdwallet,
		gasWalletPrivKey: privKey,
		gasWalletAddress: gasAddr,
		includeTestnets:  includeTestnets,
		supplierName:     supplierName,
		supplierAddress:  supplierAddress,
		supplierVAT:      supplierVAT,
		commodoreClient:  commodoreClient,
	}
}

// deriveAddressFromPrivKey derives the Ethereum address from a private key
func deriveAddressFromPrivKey(privKeyHex string) (string, error) {
	privKeyHex = strings.TrimPrefix(privKeyHex, "0x")
	privKey, err := crypto.HexToECDSA(privKeyHex)
	if err != nil {
		return "", err
	}
	return strings.ToLower(crypto.PubkeyToAddress(privKey.PublicKey).Hex()), nil
}

// getNetworkConfig returns the network config for a given network name
func (h *X402Handler) getNetworkConfig(network string) (NetworkConfig, error) {
	cfg, ok := Networks[network]
	if !ok {
		return NetworkConfig{}, fmt.Errorf("unsupported network: %s", network)
	}
	if !cfg.X402Enabled {
		return NetworkConfig{}, fmt.Errorf("x402 not enabled on network: %s", network)
	}
	if cfg.IsTestnet && !h.includeTestnets {
		return NetworkConfig{}, fmt.Errorf("testnet payments disabled: %s", network)
	}
	return cfg, nil
}

// GetSupportedNetworks returns all networks available for x402 payments
func (h *X402Handler) GetSupportedNetworks() []NetworkConfig {
	return X402Networks(h.includeTestnets)
}

// PlatformX402Index is the reserved HD derivation index for the platform-wide payTo address.
// Per x402 spec, all x402 payments go to ONE platform address.
// The payer's identity is extracted from the signed authorization's `from` field.
// Index 0 = platform x402 payTo
// Index 1+ = tenant-specific deposit addresses (for crypto invoices, not x402)
const PlatformX402Index = uint32(0)

// GetPlatformX402Address returns the platform-wide x402 payTo address (HD index 0).
// This is used for all x402 payments regardless of tenant.
// Callers identify the payer from the authorization signature, not the address.
func (h *X402Handler) GetPlatformX402Address() (string, error) {
	xpub, err := h.getXpub()
	if err != nil {
		return "", err
	}
	addr, err := DeriveAddressFromXpub(xpub, PlatformX402Index)
	if err != nil {
		return "", fmt.Errorf("failed to derive platform x402 address: %w", err)
	}
	return strings.ToLower(addr), nil
}

// GetTenantDepositAddress returns a per-tenant deposit address for crypto invoices.
// Unlike x402 (which uses the platform address), direct deposits go to tenant-specific addresses.
// These use HD indexes 1+ (index 0 is reserved for platform x402).
func (h *X402Handler) GetTenantDepositAddress(tenantID string) (address string, derivationIndex int32, newlyCreated bool, err error) {
	tx, err := h.db.Begin()
	if err != nil {
		return "", 0, false, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	// First check if tenant already has a deposit address index
	var existingIndex sql.NullInt32
	err = tx.QueryRow(`
		SELECT x402_address_index FROM purser.tenant_subscriptions
		WHERE tenant_id = $1
		FOR UPDATE
	`, tenantID).Scan(&existingIndex)

	if err != nil && err != sql.ErrNoRows {
		return "", 0, false, fmt.Errorf("failed to check existing deposit address: %w", err)
	}

	if existingIndex.Valid && existingIndex.Int32 > 0 {
		// Derive address from existing index (must be > 0, index 0 is platform)
		xpub, err := h.getXpub()
		if err != nil {
			return "", 0, false, err
		}
		addr, err := DeriveAddressFromXpub(xpub, uint32(existingIndex.Int32))
		if err != nil {
			return "", 0, false, fmt.Errorf("failed to derive address: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return "", 0, false, fmt.Errorf("failed to commit: %w", err)
		}
		return strings.ToLower(addr), existingIndex.Int32, false, nil
	}

	// Create new address - get next derivation index atomically
	// The HD wallet starts at index 1 (index 0 is reserved for platform x402)
	index, xpub, err := h.hdwallet.GetNextNonZeroDerivationIndexTx(tx)
	if err != nil {
		return "", 0, false, fmt.Errorf("failed to get derivation index: %w", err)
	}

	address, err = DeriveAddressFromXpub(xpub, index)
	if err != nil {
		return "", 0, false, fmt.Errorf("failed to derive address: %w", err)
	}
	address = strings.ToLower(address)

	// Store the index on the tenant subscription
	_, err = tx.Exec(`
		UPDATE purser.tenant_subscriptions
		SET x402_address_index = $1, updated_at = NOW()
		WHERE tenant_id = $2
	`, index, tenantID)

	if err != nil {
		return "", 0, false, fmt.Errorf("failed to store deposit address index: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", 0, false, fmt.Errorf("failed to commit: %w", err)
	}

	h.logger.WithFields(logging.Fields{
		"tenant_id":        tenantID,
		"address":          address,
		"derivation_index": index,
	}).Info("Created deposit address for tenant")

	return address, int32(index), true, nil
}

// GetOrCreateTenantX402Address is deprecated - use GetPlatformX402Address for x402
// or GetTenantDepositAddress for direct crypto deposits.
// Kept for backward compatibility during migration.
func (h *X402Handler) GetOrCreateTenantX402Address(tenantID string) (address string, derivationIndex int32, newlyCreated bool, err error) {
	return h.GetTenantDepositAddress(tenantID)
}

// getXpub retrieves the stored extended public key
func (h *X402Handler) getXpub() (string, error) {
	var xpub string
	err := h.db.QueryRow(`SELECT xpub FROM purser.hd_wallet_state WHERE id = 1`).Scan(&xpub)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("hd_wallet_state not initialized")
	}
	if err != nil {
		return "", err
	}
	return xpub, nil
}

// VerifyPayment verifies an x402 payment payload without settling it.
// Checks:
// 1. Network is supported for x402
// 2. EIP-712 signature validity (ecrecover)
// 3. Payer has sufficient USDC balance (if value > 0)
// 4. Nonce has not been used (if value > 0)
// 5. validAfter <= now <= validBefore
// 6. 'to' matches platform payTo address
//
// Note: tenantID is optional. If empty, verification still works since x402 uses
// the platform-wide payTo address. The payer is identified from auth.From.
// Zero-value payments (value=0) are valid for auth-only mode (EIP-3009 allows this).
func (h *X402Handler) VerifyPayment(ctx context.Context, tenantID string, payload *X402PaymentPayload, clientIP string) (*VerifyResult, error) {
	if payload == nil || payload.Payload == nil || payload.Payload.Authorization == nil {
		return &VerifyResult{Valid: false, Error: "invalid payload structure"}, nil
	}

	// Validate network is supported for x402
	network, err := h.getNetworkConfig(payload.Network)
	if err != nil {
		//nolint:nilerr // validation failure returned in result struct, not as error
		return &VerifyResult{Valid: false, Error: err.Error()}, nil
	}

	auth := payload.Payload.Authorization

	// Get platform-wide payTo address (HD index 0)
	expectedPayTo, err := h.GetPlatformX402Address()
	if err != nil {
		return nil, fmt.Errorf("failed to get platform payTo address: %w", err)
	}

	// Check 'to' matches our platform address
	if !strings.EqualFold(auth.To, expectedPayTo) {
		return &VerifyResult{Valid: false, Error: "invalid payTo address"}, nil
	}

	// Parse amount (USDC has 6 decimals)
	amountBig, ok := new(big.Int).SetString(auth.Value, 10)
	if !ok {
		return &VerifyResult{Valid: false, Error: "invalid amount format"}, nil
	}

	// Convert to USD cents (USDC 6 decimals → cents)
	// 1 USDC = 100 cents = 1_000_000 base units
	// So: usd_cents = amount_base_units / 10_000
	centsDivisor := big.NewInt(10_000)
	if new(big.Int).Mod(amountBig, centsDivisor).Sign() != 0 {
		return &VerifyResult{Valid: false, Error: "amount must be in cent increments (multiple of 10000 base units)"}, nil
	}
	amountUsdCents := new(big.Int).Div(amountBig, centsDivisor).Int64()

	// Zero-value is allowed for auth-only mode (proves wallet ownership without payment)
	isAuthOnly := amountUsdCents == 0

	// Check time bounds
	now := time.Now().Unix()
	validAfter, err := parseUint256String(auth.ValidAfter)
	if err != nil {
		//nolint:nilerr // validation failure returned in result struct, not as error
		return &VerifyResult{Valid: false, Error: "invalid validAfter"}, nil
	}
	validBefore, err := parseUint256String(auth.ValidBefore)
	if err != nil {
		//nolint:nilerr // validation failure returned in result struct, not as error
		return &VerifyResult{Valid: false, Error: "invalid validBefore"}, nil
	}

	if now < validAfter.Int64() {
		return &VerifyResult{Valid: false, Error: "authorization not yet valid"}, nil
	}
	if now > validBefore.Int64() {
		return &VerifyResult{Valid: false, Error: "authorization expired"}, nil
	}

	// Verify EIP-712 signature and recover signer address
	signerAddr, err := h.recoverEIP3009Signer(payload, network)
	if err != nil {
		return &VerifyResult{Valid: false, Error: fmt.Sprintf("signature verification failed: %v", err)}, nil
	}

	// Check signer matches 'from' in authorization
	if !strings.EqualFold(signerAddr, auth.From) {
		return &VerifyResult{Valid: false, Error: "signer does not match from address"}, nil
	}

	// For auth-only (zero-value), skip nonce and balance checks since no transfer happens
	if !isAuthOnly {
		// Check nonce not already used (on-chain check)
		nonceUsed, err := h.checkNonceUsed(ctx, network, auth.From, auth.Nonce)
		if err != nil {
			h.logger.WithFields(logging.Fields{"error": err, "network": network.Name}).Warn("Failed to check nonce on-chain, continuing")
			// Continue - we'll catch replay at settlement
		} else if nonceUsed {
			return &VerifyResult{Valid: false, Error: "nonce already used"}, nil
		}

		// Check also in our database (for in-flight transactions)
		var count int
		err = h.db.QueryRow(`
			SELECT COUNT(*) FROM purser.x402_nonces
			WHERE network = $1 AND payer_address = $2 AND nonce = $3
		`, payload.Network, strings.ToLower(auth.From), auth.Nonce).Scan(&count)
		if err != nil {
			return nil, fmt.Errorf("failed to check nonce in database: %w", err)
		}
		if count > 0 {
			return &VerifyResult{Valid: false, Error: "nonce already used"}, nil
		}

		// Check payer USDC balance on the specified network
		balance, err := h.getUSDCBalance(ctx, network, auth.From)
		if err != nil {
			h.logger.WithFields(logging.Fields{"error": err, "network": network.Name}).Warn("Failed to check USDC balance")
			// Continue - settlement will fail if insufficient
		} else if balance.Cmp(amountBig) < 0 {
			return &VerifyResult{Valid: false, Error: "insufficient USDC balance"}, nil
		}
	}

	// Convert to EUR cents for ledger + VAT checks (0 for auth-only)
	var amountEurCents int64
	if !isAuthOnly {
		amountEurCents, err = h.convertToEurCents(amountUsdCents)
		if err != nil {
			return &VerifyResult{Valid: false, Error: fmt.Sprintf("failed to convert amount to EUR: %v", err)}, nil
		}
	}

	// Check if €100 threshold requires billing details (only for non-auth-only payments)
	requiresBillingDetails := false
	if !isAuthOnly && amountEurCents >= 10000 && tenantID != "" { // €100
		// Check if tenant has complete billing details (reuse billing details logic)
		var billingEmail sql.NullString
		var billingAddress []byte
		err = h.db.QueryRow(`
			SELECT billing_email, billing_address
			FROM purser.tenant_subscriptions
			WHERE tenant_id = $1 AND status != 'cancelled'
			ORDER BY created_at DESC
			LIMIT 1
		`, tenantID).Scan(&billingEmail, &billingAddress)
		if err != nil && err != sql.ErrNoRows {
			h.logger.WithFields(logging.Fields{"error": err}).Warn("Failed to check billing details")
		}
		isComplete := isBillingDetailsComplete(billingEmail, billingAddress)
		requiresBillingDetails = !isComplete
	}

	return &VerifyResult{
		Valid:                  true,
		PayerAddress:           signerAddr,
		AmountCents:            amountEurCents,
		IsAuthOnly:             isAuthOnly,
		RequiresBillingDetails: requiresBillingDetails,
	}, nil
}

// SettlePayment submits the transferWithAuthorization transaction to settle the payment.
// For auth-only payments (value=0), no transaction is submitted - just returns success
// to indicate the wallet signature was verified.
func (h *X402Handler) SettlePayment(ctx context.Context, tenantID string, payload *X402PaymentPayload, clientIP string) (*SettleResult, error) {
	// First verify
	verifyResult, err := h.VerifyPayment(ctx, tenantID, payload, clientIP)
	if err != nil {
		return nil, err
	}
	if !verifyResult.Valid {
		return &SettleResult{Success: false, Error: verifyResult.Error}, nil
	}

	// Auth-only (zero-value) - just return success with payer address
	// No transaction, no balance credit, just proves wallet ownership
	if verifyResult.IsAuthOnly {
		h.logger.WithFields(logging.Fields{
			"payer_address": verifyResult.PayerAddress,
			"network":       payload.Network,
		}).Info("x402 auth-only verification successful")

		return &SettleResult{
			Success:      true,
			IsAuthOnly:   true,
			PayerAddress: verifyResult.PayerAddress,
		}, nil
	}

	if verifyResult.RequiresBillingDetails {
		return &SettleResult{
			Success: false,
			Error:   "billing details required for payments ≥€100",
		}, nil
	}

	// Get network config (already validated in VerifyPayment, but get it again for use here)
	network, err := h.getNetworkConfig(payload.Network)
	if err != nil {
		//nolint:nilerr // settlement failure returned in result struct, not as error
		return &SettleResult{Success: false, Error: err.Error()}, nil
	}

	auth := payload.Payload.Authorization

	// Build and submit the transferWithAuthorization transaction
	txHash, err := h.submitTransferWithAuthorization(ctx, payload, network)
	if err != nil {
		return &SettleResult{
			Success: false,
			Error:   fmt.Sprintf("settlement failed: %v", err),
		}, nil
	}

	newBalance, storedAmount, inserted, nonceStatus, storedTxHash, err := h.recordNonceAndCredit(ctx, payload.Network, auth.From, auth.Nonce, txHash, tenantID, verifyResult.AmountCents)
	if err != nil {
		return &SettleResult{
			Success: false,
			Error:   fmt.Sprintf("failed to record settlement: %v", err),
		}, nil
	}

	if !inserted {
		return h.buildIdempotentSettleResult(ctx, tenantID, storedAmount, storedTxHash, nonceStatus)
	}

	// Generate simplified invoice
	invoiceNumber, err := h.generateSimplifiedInvoice(ctx, tenantID, verifyResult.AmountCents, txHash, clientIP, network.Name)
	if err != nil {
		h.logger.WithFields(logging.Fields{"error": err}).Error("Failed to generate simplified invoice")
		// Non-fatal
	}

	// Update balance rollups
	if err := h.updateBalanceRollups(ctx, tenantID, verifyResult.AmountCents); err != nil {
		h.logger.WithFields(logging.Fields{"error": err}).Error("Failed to update balance rollups")
		// Non-fatal
	}

	// Invalidate Foghorn cache (same as topup flow)
	if h.commodoreClient != nil {
		if _, err := h.commodoreClient.InvalidateTenantCache(ctx, tenantID, "x402 balance top-up"); err != nil {
			h.logger.WithFields(logging.Fields{
				"error":     err,
				"tenant_id": tenantID,
			}).Warn("Failed to invalidate tenant cache after x402 settlement")
			// Non-fatal - cache will expire naturally
		}
	}

	emitBillingEvent(eventX402SettlementPending, tenantID, "x402_nonce", txHash, &pb.BillingEvent{
		Amount:   float64(verifyResult.AmountCents) / 100,
		Currency: billing.DefaultCurrency(),
		Status:   "pending",
	})

	h.logger.WithFields(logging.Fields{
		"tenant_id":    tenantID,
		"amount_cents": verifyResult.AmountCents,
		"tx_hash":      txHash,
		"network":      network.Name,
		"new_balance":  newBalance,
		"invoice":      invoiceNumber,
	}).Info("x402 payment settled")

	return &SettleResult{
		Success:         true,
		TxHash:          txHash,
		CreditedCents:   verifyResult.AmountCents,
		Currency:        billing.DefaultCurrency(),
		NewBalanceCents: newBalance,
		InvoiceNumber:   invoiceNumber,
	}, nil
}

func (h *X402Handler) recordNonceAndCredit(ctx context.Context, network, payerAddress, nonce, txHash, tenantID string, amountCents int64) (int64, int64, bool, string, string, error) {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, false, "", "", err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	var (
		inserted     bool
		nonceStatus  string
		storedTxHash string
		storedTenant string
		storedAmount int64
		newBalance   int64
	)

	err = tx.QueryRowContext(ctx, `
		INSERT INTO purser.x402_nonces (
			network, payer_address, nonce, tx_hash, tenant_id, amount_cents, status, settled_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'pending', NOW())
		ON CONFLICT (network, payer_address, nonce) DO UPDATE
		SET tx_hash = purser.x402_nonces.tx_hash
		RETURNING tx_hash, tenant_id, amount_cents, status, (xmax = 0) AS inserted
	`, network, strings.ToLower(payerAddress), nonce, txHash, tenantID, amountCents).Scan(&storedTxHash, &storedTenant, &storedAmount, &nonceStatus, &inserted)
	if err != nil {
		return 0, 0, false, "", "", err
	}

	if storedTenant != tenantID {
		return 0, 0, false, "", "", fmt.Errorf("nonce already used by another tenant")
	}
	if storedAmount != amountCents {
		return 0, 0, false, "", "", fmt.Errorf("nonce already used for a different amount")
	}

	if !inserted {
		if err = tx.Commit(); err != nil {
			return 0, 0, false, "", "", err
		}
		return 0, storedAmount, false, nonceStatus, storedTxHash, nil
	}

	newBalance, err = h.creditPrepaidBalanceTx(ctx, tx, tenantID, amountCents, txHash, "x402 USDC payment")
	if err != nil {
		return 0, 0, false, "", "", err
	}

	if err = tx.Commit(); err != nil {
		return 0, 0, false, "", "", err
	}

	return newBalance, amountCents, true, "pending", txHash, nil
}

func (h *X402Handler) buildIdempotentSettleResult(ctx context.Context, tenantID string, amountCents int64, txHash, status string) (*SettleResult, error) {
	if status == "failed" {
		return &SettleResult{
			Success: false,
			Error:   "nonce already used",
		}, nil
	}

	currentBalance, err := h.getCurrentBalance(ctx, tenantID, billing.DefaultCurrency())
	if err != nil {
		return nil, err
	}

	return &SettleResult{
		Success:         true,
		TxHash:          txHash,
		CreditedCents:   amountCents,
		Currency:        billing.DefaultCurrency(),
		NewBalanceCents: currentBalance,
	}, nil
}

// recoverEIP3009Signer recovers the signer address from an EIP-3009 transferWithAuthorization
// The signature is over the EIP-712 typed data hash
func (h *X402Handler) recoverEIP3009Signer(payload *X402PaymentPayload, network NetworkConfig) (string, error) {
	// EIP-712 domain for USDC on the specified network
	domainSeparator := h.getUSDCDomainSeparator(network)

	// Build the TransferWithAuthorization struct hash
	auth := payload.Payload.Authorization
	structHash := h.hashTransferWithAuthorization(auth)

	// EIP-712 hash: keccak256("\x19\x01" + domainSeparator + structHash)
	messageHash := keccak256(
		[]byte{0x19, 0x01},
		domainSeparator,
		structHash,
	)

	// Parse signature
	sig, err := hex.DecodeString(strings.TrimPrefix(payload.Payload.Signature, "0x"))
	if err != nil {
		return "", fmt.Errorf("invalid signature hex: %w", err)
	}
	if len(sig) != 65 {
		return "", fmt.Errorf("signature must be 65 bytes, got %d", len(sig))
	}

	// Recover public key from signature
	// sig = r (32) + s (32) + v (1)
	r := new(big.Int).SetBytes(sig[0:32])
	s := new(big.Int).SetBytes(sig[32:64])
	v := sig[64]

	// Ethereum uses v = 27 or 28, some use 0 or 1
	if v < 27 {
		v += 27
	}
	if v != 27 && v != 28 {
		return "", fmt.Errorf("invalid recovery id: %d", v)
	}

	// Use crypto library to recover (we need go-ethereum for this)
	// For now, return a placeholder - actual implementation needs go-ethereum/crypto
	// In production, use: crypto.Ecrecover(messageHash, append(sig[:64], v-27))
	recoveredAddr, err := ecrecover(messageHash, r, s, v)
	if err != nil {
		return "", fmt.Errorf("ecrecover failed: %w", err)
	}

	return recoveredAddr, nil
}

// getUSDCDomainSeparator returns the EIP-712 domain separator for USDC on a given network
func (h *X402Handler) getUSDCDomainSeparator(network NetworkConfig) []byte {
	// EIP-712 domain:
	// name: "USD Coin"
	// version: "2" (Circle's USDC v2)
	// chainId: network-specific
	// verifyingContract: USDC contract address on that network

	chainId := big.NewInt(network.ChainID)

	// keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")
	typeHash := keccak256([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))

	nameHash := keccak256([]byte("USD Coin"))
	versionHash := keccak256([]byte("2"))

	contractAddr, _ := hex.DecodeString(strings.TrimPrefix(network.USDCContract, "0x"))
	contractAddrPadded := make([]byte, 32)
	copy(contractAddrPadded[12:], contractAddr)

	chainIdBytes := make([]byte, 32)
	chainId.FillBytes(chainIdBytes)

	return keccak256(
		typeHash,
		nameHash,
		versionHash,
		chainIdBytes,
		contractAddrPadded,
	)
}

// hashTransferWithAuthorization computes the EIP-712 struct hash for TransferWithAuthorization
func (h *X402Handler) hashTransferWithAuthorization(auth *X402Authorization) []byte {
	// keccak256("TransferWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)")
	typeHash := keccak256([]byte("TransferWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)"))

	from := padAddress(auth.From)
	to := padAddress(auth.To)
	value := padUint256(auth.Value)
	validAfter := padUint256(auth.ValidAfter)
	validBefore := padUint256(auth.ValidBefore)
	nonce := padBytes32(auth.Nonce)

	return keccak256(
		typeHash,
		from,
		to,
		value,
		validAfter,
		validBefore,
		nonce,
	)
}

// submitTransferWithAuthorization submits the settlement tx to the specified network
func (h *X402Handler) submitTransferWithAuthorization(ctx context.Context, payload *X402PaymentPayload, network NetworkConfig) (string, error) {
	if h.gasWalletPrivKey == "" {
		return "", fmt.Errorf("x402 gas wallet not configured")
	}

	auth := payload.Payload.Authorization
	sig, err := hex.DecodeString(strings.TrimPrefix(payload.Payload.Signature, "0x"))
	if err != nil {
		return "", fmt.Errorf("invalid signature: %w", err)
	}

	// Parse signature into v, r, s
	if len(sig) != 65 {
		return "", fmt.Errorf("signature must be 65 bytes")
	}
	r := sig[0:32]
	s := sig[32:64]
	v := sig[64]
	if v < 27 {
		v += 27
	}

	// Build transferWithAuthorization call data
	// function transferWithAuthorization(
	//     address from,
	//     address to,
	//     uint256 value,
	//     uint256 validAfter,
	//     uint256 validBefore,
	//     bytes32 nonce,
	//     uint8 v,
	//     bytes32 r,
	//     bytes32 s
	// )
	methodID := keccak256([]byte("transferWithAuthorization(address,address,uint256,uint256,uint256,bytes32,uint8,bytes32,bytes32)"))[0:4]

	callData := methodID
	callData = append(callData, padAddress(auth.From)...)
	callData = append(callData, padAddress(auth.To)...)
	callData = append(callData, padUint256(auth.Value)...)
	callData = append(callData, padUint256(auth.ValidAfter)...)
	callData = append(callData, padUint256(auth.ValidBefore)...)
	callData = append(callData, padBytes32(auth.Nonce)...)
	callData = append(callData, padUint8(v)...)
	callData = append(callData, padBytes32Bytes(r)...)
	callData = append(callData, padBytes32Bytes(s)...)

	// Simulate via eth_call before submitting (per x402 spec)
	if err := h.simulateTransfer(ctx, network, callData); err != nil {
		return "", fmt.Errorf("simulation failed: %w", err)
	}

	// Send raw transaction via RPC
	txHash, err := h.sendRawTransaction(ctx, network, network.USDCContract, callData)
	if err != nil {
		return "", fmt.Errorf("failed to send transaction: %w", err)
	}

	return txHash, nil
}

// sendRawTransaction signs and sends a transaction via JSON-RPC to the specified network
func (h *X402Handler) sendRawTransaction(ctx context.Context, network NetworkConfig, to string, data []byte) (string, error) {
	// Get nonce for gas wallet
	nonce, err := h.getNonce(ctx, network, h.gasWalletAddress)
	if err != nil {
		return "", fmt.Errorf("failed to get nonce: %w", err)
	}

	// Get gas price
	gasPrice, err := h.getGasPrice(ctx, network)
	if err != nil {
		return "", fmt.Errorf("failed to get gas price: %w", err)
	}

	// Build transaction
	// For simplicity, using legacy transaction format
	// Production should use EIP-1559
	gasLimit := uint64(150000) // Conservative estimate for transferWithAuthorization

	chainId := big.NewInt(network.ChainID)

	// Sign transaction
	signedTx, err := h.signTransaction(nonce, to, big.NewInt(0), gasLimit, gasPrice, data, chainId)
	if err != nil {
		return "", fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Submit via eth_sendRawTransaction
	var txHash string
	err = h.rpcCall(ctx, network, "eth_sendRawTransaction", []interface{}{"0x" + hex.EncodeToString(signedTx)}, &txHash)
	if err != nil {
		return "", fmt.Errorf("eth_sendRawTransaction failed: %w", err)
	}

	return txHash, nil
}

// creditPrepaidBalance credits the tenant's prepaid balance
func (h *X402Handler) creditPrepaidBalance(ctx context.Context, tenantID string, amountCents int64, txHash string) (int64, error) {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	newBalance, err := h.creditPrepaidBalanceTx(ctx, tx, tenantID, amountCents, txHash, "x402 USDC payment")
	if err != nil {
		return 0, err
	}

	if err = tx.Commit(); err != nil {
		return 0, err
	}

	return newBalance, nil
}

func (h *X402Handler) creditPrepaidBalanceTx(ctx context.Context, tx *sql.Tx, tenantID string, amountCents int64, txHash string, description string) (int64, error) {
	currency := billing.DefaultCurrency()

	// Get current balance
	var currentBalance int64
	err := tx.QueryRowContext(ctx, `
		SELECT balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2
	`, tenantID, currency).Scan(&currentBalance)
	if err == sql.ErrNoRows {
		currentBalance = 0
	} else if err != nil {
		return 0, err
	}

	newBalance := currentBalance + amountCents

	// Upsert balance
	_, err = tx.ExecContext(ctx, `
		INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (tenant_id, currency)
		DO UPDATE SET balance_cents = $2, updated_at = NOW()
	`, tenantID, newBalance, currency)
	if err != nil {
		return 0, err
	}

	// Record transaction
	_, err = tx.ExecContext(ctx, `
		INSERT INTO purser.balance_transactions (
			id, tenant_id, amount_cents, balance_after_cents,
			transaction_type, description, reference_id, reference_type, created_at
		) VALUES ($1, $2, $3, $4, 'topup', $5, $6, 'x402_payment', NOW())
	`,
		uuid.New().String(),
		tenantID,
		amountCents,
		newBalance,
		fmt.Sprintf("%s (%s...)", description, txHash[:16]),
		txHash,
	)
	if err != nil {
		return 0, err
	}

	return newBalance, nil
}

func (h *X402Handler) getCurrentBalance(ctx context.Context, tenantID, currency string) (int64, error) {
	var balance int64
	err := h.db.QueryRowContext(ctx, `
		SELECT balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2
	`, tenantID, currency).Scan(&balance)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return balance, nil
}

// generateSimplifiedInvoice creates an EU-compliant simplified invoice
func (h *X402Handler) generateSimplifiedInvoice(ctx context.Context, tenantID string, amountEurCents int64, txHash, clientIP, networkName string) (string, error) {
	// Supplier info required for invoicing
	if h.supplierName == "" || h.supplierAddress == "" || h.supplierVAT == "" {
		return "", fmt.Errorf("supplier information not configured for x402 invoicing")
	}

	// Get actual ECB rate for record
	ecbRate, err := h.getEurUsdRate()
	if err != nil {
		return "", fmt.Errorf("failed to get ECB rate: %w", err)
	}

	// Get VAT rate using hybrid approach (billing country > GeoIP)
	vatRateBps, country, isB2B := h.getVATRateForTenant(tenantID, clientIP)
	vatAmountCents := amountEurCents * int64(vatRateBps) / 10000
	netAmountCents := amountEurCents - vatAmountCents

	// Generate invoice number (SI = simplified invoice, B2B for reverse charge)
	prefix := "SI"
	if isB2B {
		prefix = "B2B" // Reverse charge invoice
	}
	invoiceNumber := fmt.Sprintf("%s-%s-%d", prefix, time.Now().Format("20060102"), time.Now().UnixNano()%100000)

	_, err = h.db.ExecContext(ctx, `
		INSERT INTO purser.simplified_invoices (
			invoice_number, tenant_id, reference_type, reference_id,
			gross_amount_cents, net_amount_cents, vat_amount_cents, vat_rate_bps,
			currency, amount_eur_cents, ecb_rate,
			evidence_ip_country, evidence_wallet_network,
			supplier_name, supplier_address, supplier_vat_number,
			issued_at
		) VALUES ($1, $2, 'x402_payment', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, NOW())
	`,
		invoiceNumber, tenantID, txHash,
		amountEurCents, netAmountCents, vatAmountCents, vatRateBps,
		billing.DefaultCurrency(), amountEurCents, ecbRate,
		country, networkName,
		h.supplierName, h.supplierAddress, h.supplierVAT,
	)

	if err != nil {
		return "", fmt.Errorf("failed to insert simplified invoice: %w", err)
	}

	return invoiceNumber, nil
}

// updateBalanceRollups updates the tenant's lifetime stats
func (h *X402Handler) updateBalanceRollups(ctx context.Context, tenantID string, amountEurCents int64) error {
	_, err := h.db.ExecContext(ctx, `
		INSERT INTO purser.tenant_balance_rollups (
			tenant_id, total_topup_cents, total_topup_eur_cents, topup_count, first_topup_at, last_topup_at
		) VALUES ($1, $2, $3, 1, NOW(), NOW())
		ON CONFLICT (tenant_id) DO UPDATE SET
			total_topup_cents = purser.tenant_balance_rollups.total_topup_cents + EXCLUDED.total_topup_cents,
			total_topup_eur_cents = purser.tenant_balance_rollups.total_topup_eur_cents + EXCLUDED.total_topup_eur_cents,
			topup_count = purser.tenant_balance_rollups.topup_count + 1,
			last_topup_at = NOW(),
			updated_at = NOW()
	`, tenantID, amountEurCents, amountEurCents)

	return err
}

// Helper functions

func (h *X402Handler) checkNonceUsed(ctx context.Context, network NetworkConfig, owner, nonce string) (bool, error) {
	// Call USDC contract to check nonce status
	// authorizationState(address authorizer, bytes32 nonce) returns (bool)
	methodID := keccak256([]byte("authorizationState(address,bytes32)"))[0:4]
	callData := methodID
	callData = append(callData, padAddress(owner)...)
	callData = append(callData, padBytes32(nonce)...)

	var result string
	err := h.rpcCall(ctx, network, "eth_call", []interface{}{
		map[string]string{
			"to":   network.USDCContract,
			"data": "0x" + hex.EncodeToString(callData),
		},
		"latest",
	}, &result)
	if err != nil {
		return false, err
	}

	// Result is bool as uint256 (32 bytes), 1 = used, 0 = unused
	return result != "0x0000000000000000000000000000000000000000000000000000000000000000", nil
}

func (h *X402Handler) getUSDCBalance(ctx context.Context, network NetworkConfig, address string) (*big.Int, error) {
	// balanceOf(address) returns (uint256)
	methodID := keccak256([]byte("balanceOf(address)"))[0:4]
	callData := append(methodID, padAddress(address)...)

	var result string
	err := h.rpcCall(ctx, network, "eth_call", []interface{}{
		map[string]string{
			"to":   network.USDCContract,
			"data": "0x" + hex.EncodeToString(callData),
		},
		"latest",
	}, &result)
	if err != nil {
		return nil, err
	}

	balance := new(big.Int)
	balance.SetString(strings.TrimPrefix(result, "0x"), 16)
	return balance, nil
}

func (h *X402Handler) getNonce(ctx context.Context, network NetworkConfig, address string) (uint64, error) {
	var result string
	err := h.rpcCall(ctx, network, "eth_getTransactionCount", []interface{}{address, "pending"}, &result)
	if err != nil {
		return 0, err
	}
	nonce, _ := new(big.Int).SetString(strings.TrimPrefix(result, "0x"), 16)
	return nonce.Uint64(), nil
}

func (h *X402Handler) getGasPrice(ctx context.Context, network NetworkConfig) (*big.Int, error) {
	var result string
	err := h.rpcCall(ctx, network, "eth_gasPrice", []interface{}{}, &result)
	if err != nil {
		return nil, err
	}
	gasPrice, _ := new(big.Int).SetString(strings.TrimPrefix(result, "0x"), 16)
	return gasPrice, nil
}

func (h *X402Handler) rpcCall(ctx context.Context, network NetworkConfig, method string, params interface{}, result interface{}) error {
	rpcEndpoint := network.GetRPCEndpointWithDefault()
	if rpcEndpoint == "" {
		return fmt.Errorf("no RPC endpoint configured for network %s", network.Name)
	}

	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
		"id":      1,
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", rpcEndpoint, strings.NewReader(string(reqJSON)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var rpcResp struct {
		Result interface{}      `json:"result"`
		Error  *json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return err
	}

	if rpcResp.Error != nil {
		return fmt.Errorf("RPC error: %s", string(*rpcResp.Error))
	}

	// Marshal and unmarshal to get result in correct type
	resultJSON, err := json.Marshal(rpcResp.Result)
	if err != nil {
		return err
	}
	return json.Unmarshal(resultJSON, result)
}

// simulateTransfer runs eth_call to verify the transfer will succeed before submitting
func (h *X402Handler) simulateTransfer(ctx context.Context, network NetworkConfig, callData []byte) error {
	var result string
	err := h.rpcCall(ctx, network, "eth_call", []interface{}{
		map[string]string{
			"to":   network.USDCContract,
			"data": "0x" + hex.EncodeToString(callData),
		},
		"latest",
	}, &result)
	if err != nil {
		return err
	}
	// eth_call succeeded - transaction should work
	return nil
}

func (h *X402Handler) convertToEurCents(usdCents int64) (int64, error) {
	rate, err := h.getEurUsdRate()
	if err != nil {
		return 0, err
	}
	// EUR = USD * rate (e.g., 0.92 EUR per USD)
	eurCents := int64(float64(usdCents) * rate)
	return eurCents, nil
}

// getEurUsdRate returns the EUR/USD exchange rate, fetching from ECB if cache expired
func (h *X402Handler) getEurUsdRate() (float64, error) {
	// Check cache first
	ecbRateCache.RLock()
	cachedRate := ecbRateCache.rate
	fetchedAt := ecbRateCache.fetchedAt
	ecbRateCache.RUnlock()

	// Return cached if still valid
	if time.Since(fetchedAt) < ecbRateCacheTTL && cachedRate > 0 {
		return cachedRate, nil
	}

	// Try to fetch fresh rate (using frankfurter.app - free ECB rate API)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.frankfurter.app/latest?from=USD&to=EUR", nil)
	if err != nil {
		// Return stale cache if available
		if cachedRate > 0 {
			return cachedRate, nil
		}
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Return stale cache if available
		if cachedRate > 0 {
			return cachedRate, nil
		}
		return 0, fmt.Errorf("failed to fetch ECB rate: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if cachedRate > 0 {
			return cachedRate, nil
		}
		return 0, fmt.Errorf("ECB rate API returned status %d", resp.StatusCode)
	}

	var result struct {
		Rates map[string]float64 `json:"rates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		if cachedRate > 0 {
			return cachedRate, nil
		}
		return 0, fmt.Errorf("failed to decode ECB rate response: %w", err)
	}

	rate, ok := result.Rates["EUR"]
	if !ok || rate <= 0 {
		if cachedRate > 0 {
			return cachedRate, nil
		}
		return 0, fmt.Errorf("EUR rate not found in response")
	}

	// Update cache
	ecbRateCache.Lock()
	ecbRateCache.rate = rate
	ecbRateCache.fetchedAt = time.Now()
	ecbRateCache.Unlock()

	h.logger.WithFields(logging.Fields{"rate": rate}).Debug("Fetched fresh EUR/USD rate from ECB")
	return rate, nil
}

// EU VAT rates by country (basis points, i.e. 2100 = 21%)
var euVATRates = map[string]int{
	"AT": 2000, // Austria
	"BE": 2100, // Belgium
	"BG": 2000, // Bulgaria
	"HR": 2500, // Croatia
	"CY": 1900, // Cyprus
	"CZ": 2100, // Czech Republic
	"DK": 2500, // Denmark
	"EE": 2000, // Estonia
	"FI": 2400, // Finland
	"FR": 2000, // France
	"DE": 1900, // Germany
	"GR": 2400, // Greece
	"HU": 2700, // Hungary
	"IE": 2300, // Ireland
	"IT": 2200, // Italy
	"LV": 2100, // Latvia
	"LT": 2100, // Lithuania
	"LU": 1700, // Luxembourg
	"MT": 1800, // Malta
	"NL": 2100, // Netherlands
	"PL": 2300, // Poland
	"PT": 2300, // Portugal
	"RO": 1900, // Romania
	"SK": 2000, // Slovakia
	"SI": 2200, // Slovenia
	"ES": 2100, // Spain
	"SE": 2500, // Sweden
}

// getVATRateForTenant returns VAT rate considering tenant's billing details
func (h *X402Handler) getVATRateForTenant(tenantID, clientIP string) (rateBps int, country string, isB2B bool) {
	// 1. Check tenant's billing details (country and VAT number)
	var taxID sql.NullString
	var billingAddress []byte
	err := h.db.QueryRow(`
		SELECT tax_id, billing_address
		FROM purser.tenant_subscriptions
		WHERE tenant_id = $1 AND status != 'cancelled'
		ORDER BY created_at DESC
		LIMIT 1
	`, tenantID).Scan(&taxID, &billingAddress)

	if err == nil && billingAddress != nil {
		// Parse billing address to get country
		var addr struct {
			Country string `json:"country"`
		}
		if json.Unmarshal(billingAddress, &addr) == nil && addr.Country != "" {
			country = countries.Normalize(addr.Country)

			// Check for B2B with valid EU VAT number
			if taxID.Valid && taxID.String != "" {
				if h.isValidEUVATFormat(taxID.String) {
					// B2B with EU VAT number = reverse charge (0% VAT)
					return 0, country, true
				}
			}

			// B2C or no valid VAT number
			if _, isEU := euVATRates[country]; isEU {
				return euVATRates[country], country, false
			}
			// Non-EU billing country = export exempt
			return 0, country, false
		}
	}

	// 2. Fall back to GeoIP
	country = h.getCountryFromIP(clientIP)
	if _, isEU := euVATRates[country]; isEU {
		return euVATRates[country], country, false
	}

	// 3. Non-EU GeoIP = export exempt
	return 0, country, false
}

func isBillingDetailsComplete(billingEmail sql.NullString, billingAddress []byte) bool {
	if !billingEmail.Valid || strings.TrimSpace(billingEmail.String) == "" {
		return false
	}
	if len(billingAddress) == 0 {
		return false
	}
	var addr struct {
		Street     string `json:"street"`
		City       string `json:"city"`
		PostalCode string `json:"postal_code"`
		Country    string `json:"country"`
	}
	if json.Unmarshal(billingAddress, &addr) != nil {
		return false
	}
	return addr.Street != "" && addr.City != "" && addr.PostalCode != "" && addr.Country != ""
}

// isValidEUVATFormat checks if a VAT number has valid EU format
// Format: 2-letter country code + 8-12 alphanumeric chars
// Note: Does NOT validate via VIES API - just format check
func (h *X402Handler) isValidEUVATFormat(vatNumber string) bool {
	vatNumber = strings.ToUpper(strings.ReplaceAll(vatNumber, " ", ""))
	if len(vatNumber) < 8 || len(vatNumber) > 14 {
		return false
	}
	countryCode := vatNumber[:2]
	_, isEU := euVATRates[countryCode]
	return isEU
}

func (h *X402Handler) getCountryFromIP(clientIP string) string {
	if reader := geoip.GetSharedReader(); reader != nil {
		if geo := reader.Lookup(clientIP); geo != nil && geo.CountryCode != "" {
			return geo.CountryCode
		}
	}
	return "NL" // Fallback to Netherlands (our base)
}

func (h *X402Handler) signTransaction(nonce uint64, to string, value *big.Int, gasLimit uint64, gasPrice *big.Int, data []byte, chainId *big.Int) ([]byte, error) {
	if h.gasWalletPrivKey == "" {
		return nil, fmt.Errorf("gas wallet private key not configured")
	}

	// Parse private key (strip 0x prefix if present)
	privKeyHex := strings.TrimPrefix(h.gasWalletPrivKey, "0x")
	privKey, err := crypto.HexToECDSA(privKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid gas wallet private key: %w", err)
	}

	// Build transaction
	toAddr := common.HexToAddress(to)
	tx := types.NewTx(&types.LegacyTx{
		Nonce:    nonce,
		To:       &toAddr,
		Value:    value,
		Gas:      gasLimit,
		GasPrice: gasPrice,
		Data:     data,
	})

	// Sign with EIP-155 (chain ID protected)
	signer := types.NewEIP155Signer(chainId)
	signedTx, err := types.SignTx(tx, signer, privKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Encode to RLP bytes
	return signedTx.MarshalBinary()
}

// Utility functions

func keccak256(data ...[]byte) []byte {
	hasher := sha3.NewLegacyKeccak256()
	for _, d := range data {
		hasher.Write(d)
	}
	return hasher.Sum(nil)
}

func padAddress(addr string) []byte {
	addrBytes, _ := hex.DecodeString(strings.TrimPrefix(addr, "0x"))
	padded := make([]byte, 32)
	copy(padded[12:], addrBytes)
	return padded
}

func padUint256(value string) []byte {
	v, _ := new(big.Int).SetString(value, 10)
	padded := make([]byte, 32)
	v.FillBytes(padded)
	return padded
}

func padUint8(v uint8) []byte {
	padded := make([]byte, 32)
	padded[31] = v
	return padded
}

func padBytes32(value string) []byte {
	// nonce is already bytes32 as hex string
	b, _ := hex.DecodeString(strings.TrimPrefix(value, "0x"))
	padded := make([]byte, 32)
	copy(padded, b)
	return padded
}

func padBytes32Bytes(b []byte) []byte {
	padded := make([]byte, 32)
	copy(padded[32-len(b):], b)
	return padded
}

func parseUint256String(s string) (*big.Int, error) {
	v := new(big.Int)
	_, ok := v.SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("invalid uint256: %s", s)
	}
	return v, nil
}

// ecrecover recovers the signer address from a signature using go-ethereum/crypto
func ecrecover(hash []byte, r, s *big.Int, v byte) (string, error) {
	// Build signature bytes: r (32) + s (32) + recovery id (1)
	sig := make([]byte, 65)
	r.FillBytes(sig[0:32])
	s.FillBytes(sig[32:64])

	// Ethereum recovery id is 0 or 1 (not 27 or 28)
	recoveryID := v
	if recoveryID >= 27 {
		recoveryID -= 27
	}
	if recoveryID > 1 {
		return "", fmt.Errorf("invalid recovery id: %d", v)
	}
	sig[64] = recoveryID

	// Recover public key
	pubKey, err := crypto.Ecrecover(hash, sig)
	if err != nil {
		return "", fmt.Errorf("ecrecover failed: %w", err)
	}

	// Convert to address (last 20 bytes of keccak256(pubKey[1:]))
	// go-ethereum provides a helper for this
	pubKeyECDSA, err := crypto.UnmarshalPubkey(pubKey)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal pubkey: %w", err)
	}

	addr := crypto.PubkeyToAddress(*pubKeyECDSA)
	return strings.ToLower(addr.Hex()), nil
}

// Types for internal use

// X402PaymentPayload represents the x402 payment data from client
type X402PaymentPayload struct {
	X402Version int               `json:"x402Version"`
	Scheme      string            `json:"scheme"`
	Network     string            `json:"network"`
	Payload     *X402ExactPayload `json:"payload"`
}

// X402ExactPayload contains the signature and authorization details
type X402ExactPayload struct {
	Signature     string             `json:"signature"`
	Authorization *X402Authorization `json:"authorization"`
}

// X402Authorization contains the EIP-3009 authorization parameters
type X402Authorization struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Value       string `json:"value"`
	ValidAfter  string `json:"validAfter"`
	ValidBefore string `json:"validBefore"`
	Nonce       string `json:"nonce"`
}

// VerifyResult contains the result of payment verification
type VerifyResult struct {
	Valid                  bool
	Error                  string
	PayerAddress           string
	AmountCents            int64 // Ledger currency (EUR) cents
	IsAuthOnly             bool  // True if value=0 (authentication only, no payment)
	RequiresBillingDetails bool
}

// SettleResult contains the result of payment settlement
type SettleResult struct {
	Success         bool
	Error           string
	IsAuthOnly      bool   // True if this was auth-only (value=0, no transaction)
	PayerAddress    string // Wallet address of payer (from authorization signature)
	TxHash          string
	CreditedCents   int64
	Currency        string
	NewBalanceCents int64
	InvoiceNumber   string
}
