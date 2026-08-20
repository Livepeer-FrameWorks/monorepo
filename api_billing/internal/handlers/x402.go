//nolint:govet,errcheck,nilerr // Protocol validation returns structured failures; reconciliation marker writes may be best-effort.
package handlers

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/countries"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

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

const (
	ecbRateCacheTTL            = 24 * time.Hour
	defaultX402TopupUSDCents   = int64(500)
	maximumX402TopupUSDCents   = int64(10_000)
	usdcBaseUnitsPerDollarCent = int64(10_000)
	x402ReceiptPollInterval    = 500 * time.Millisecond
)

const x402SettlementValidityMargin = 6 * time.Second

// CryptoTopupTaxPolicyRef identifies the immutable VAT-at-confirmed-top-up policy recorded on crypto documents.
const CryptoTopupTaxPolicyRef = "vat-at-confirmed-topup-v1"

var errX402TransactionReverted = errors.New("x402 transaction reverted on-chain")

// X402Handler handles x402 payment verification and settlement
// Supports multiple networks (Base, Arbitrum) for x402 settlement
type X402Handler struct {
	db                    *sql.DB
	logger                logging.Logger
	hdwallet              *HDWallet
	rpc                   *RPCClient
	gasWalletPrivKey      string // Single privkey = same address on all EVM chains
	gasWalletAddress      string // Derived from privkey
	includeTestnets       bool   // Whether to accept testnet payments
	topupUSDCents         int64  // Exact v1 top-up amount; replaced by durable quotes in v2
	facilitatorProvider   string
	facilitator           x402FacilitatorClient
	facilitatorConfigErr  error
	facilitatorMu         sync.Mutex
	facilitatorReadyUntil time.Time
	facilitatorKinds      map[string]bool

	// Supplier info for invoicing (required)
	supplierName    string
	supplierAddress string
	supplierVAT     string
	supplierCountry string

	// Commodore client for cache invalidation after balance changes
	commodoreClient CommodoreClient
}

// NewX402Handler creates a new x402 payment handler
func NewX402Handler(database *sql.DB, log logging.Logger, hdwallet *HDWallet, rpc *RPCClient, commodoreClient CommodoreClient) *X402Handler {
	privKey := os.Getenv("X402_GAS_WALLET_PRIVKEY")
	gasAddr := os.Getenv("X402_GAS_WALLET_ADDRESS")
	includeTestnets := config.X402IncludeTestnetsEnabled()
	topupUSDCents := int64(config.GetEnvInt("X402_TOPUP_USD_CENTS", int(defaultX402TopupUSDCents)))
	if topupUSDCents <= 0 || topupUSDCents > maximumX402TopupUSDCents {
		log.WithFields(logging.Fields{
			"configured_cents": topupUSDCents,
			"default_cents":    defaultX402TopupUSDCents,
		}).Warn("Invalid X402_TOPUP_USD_CENTS; using safe default")
		topupUSDCents = defaultX402TopupUSDCents
	}

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
	supplierCountry := countries.Normalize(config.GetEnv("SUPPLIER_COUNTRY", ""))
	if supplierName == "" || supplierAddress == "" || supplierVAT == "" || len(supplierCountry) != 2 {
		log.Warn("x402 supplier info incomplete - simplified invoicing disabled (set SUPPLIER_NAME, SUPPLIER_ADDRESS, SUPPLIER_VAT_NUMBER, SUPPLIER_COUNTRY)")
	}
	facilitatorProvider, facilitator, facilitatorErr := newX402FacilitatorFromEnv()
	if facilitatorErr != nil {
		log.WithError(facilitatorErr).Warn("x402 facilitator is not ready")
	}

	return &X402Handler{
		db:                   database,
		logger:               log,
		hdwallet:             hdwallet,
		rpc:                  rpc,
		gasWalletPrivKey:     privKey,
		gasWalletAddress:     gasAddr,
		includeTestnets:      includeTestnets,
		topupUSDCents:        topupUSDCents,
		facilitatorProvider:  facilitatorProvider,
		facilitator:          facilitator,
		facilitatorConfigErr: facilitatorErr,
		supplierName:         supplierName,
		supplierAddress:      supplierAddress,
		supplierVAT:          supplierVAT,
		supplierCountry:      supplierCountry,
		commodoreClient:      commodoreClient,
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
	if cfg.IsTestnet && config.IsProduction() {
		return NetworkConfig{}, fmt.Errorf("testnet payments are forbidden in production: %s", network)
	}
	return cfg, nil
}

// GetSupportedNetworks returns all networks available for x402 payments
func (h *X402Handler) GetSupportedNetworks() []NetworkConfig {
	networks := X402Networks(h.includeTestnets)
	if !config.IsProduction() {
		return networks
	}
	mainnets := networks[:0]
	for _, network := range networks {
		if !network.IsTestnet {
			mainnets = append(mainnets, network)
		}
	}
	return mainnets
}

// Readiness validates the dependencies required to advertise a new x402
// payment. Reconciliation remains independent so an operator can disable or
// repair creation without abandoning already-submitted transactions.
func (h *X402Handler) Readiness(ctx context.Context) error {
	var missing []string
	if h.facilitatorProvider == "self" {
		if strings.TrimSpace(h.gasWalletPrivKey) == "" {
			missing = append(missing, "X402_GAS_WALLET_PRIVKEY")
		} else if derived, err := deriveAddressFromPrivKey(h.gasWalletPrivKey); err != nil {
			missing = append(missing, "valid X402_GAS_WALLET_PRIVKEY")
		} else if h.gasWalletAddress == "" || !strings.EqualFold(derived, h.gasWalletAddress) {
			missing = append(missing, "X402_GAS_WALLET_ADDRESS matching private key")
		}
	} else if h.facilitatorConfigErr != nil || h.facilitator == nil {
		missing = append(missing, "official x402 facilitator configuration")
	}
	if _, err := h.getXpub(ctx); err != nil {
		missing = append(missing, "initialized HD wallet xpub")
	}
	if h.supplierName == "" || h.supplierAddress == "" || h.supplierVAT == "" || len(h.supplierCountry) != 2 {
		missing = append(missing, "supplier invoice configuration")
	}
	networks, facilitatorErr := h.GetAdvertisableX402Networks(ctx)
	if facilitatorErr != nil {
		missing = append(missing, "reachable facilitator with v2 exact support")
	}
	if len(networks) == 0 {
		missing = append(missing, "supported settlement network")
	}
	for _, network := range networks {
		if network.GetRPCEndpointWithDefault() == "" {
			missing = append(missing, network.RPCEndpointEnv)
		}
		if config.IsProduction() {
			if err := ValidateCryptoCustodyNetwork(ctx, h.rpc, network, "USDC"); err != nil {
				missing = append(missing, err.Error())
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("x402 not ready: %s", strings.Join(missing, ", "))
	}
	return nil
}

// PlatformX402Index is retained for legacy recovery of payments created before
// tenant-bound x402 addresses. New requirements never advertise this address.
const PlatformX402Index = uint32(0)

// GetPlatformX402Address returns the platform-wide x402 payTo address (HD index 0).
// This is used for all x402 payments regardless of tenant.
// Callers identify the payer from the authorization signature, not the address.
func (h *X402Handler) GetPlatformX402Address(ctx context.Context) (string, error) {
	xpub, err := h.getXpub(ctx)
	if err != nil {
		return "", err
	}
	addr, err := DeriveAddressFromXpub(xpub, PlatformX402Index)
	if err != nil {
		return "", fmt.Errorf("failed to derive platform x402 address: %w", err)
	}
	return strings.ToLower(addr), nil
}

// GetTenantDepositAddress returns the stable per-tenant receiving address used
// by x402. These use HD indexes 1+ (index 0 is legacy platform recovery only).
func (h *X402Handler) GetTenantDepositAddress(ctx context.Context, tenantID string) (address string, derivationIndex int32, newlyCreated bool, err error) {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, false, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	// First check if tenant already has a deposit address index
	var existingIndex sql.NullInt32
	var existingXpub sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT x402_address_index, x402_address_xpub FROM purser.tenant_subscriptions
		WHERE tenant_id = $1
		FOR UPDATE
	`, tenantID).Scan(&existingIndex, &existingXpub)

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, fmt.Errorf("failed to check existing deposit address: %w", err)
	}

	if existingIndex.Valid && existingIndex.Int32 > 0 {
		// Derive address from existing index (must be > 0, index 0 is platform)
		if !existingXpub.Valid || strings.TrimSpace(existingXpub.String) == "" {
			return "", 0, false, fmt.Errorf("tenant x402 address is missing its derivation xpub")
		}
		addr, addrErr := DeriveAddressFromXpub(existingXpub.String, uint32(existingIndex.Int32))
		if addrErr != nil {
			return "", 0, false, fmt.Errorf("failed to derive address: %w", addrErr)
		}
		if inventoryErr := h.ensureX402CustodyInventoryTx(ctx, tx, tenantID, strings.ToLower(addr), existingIndex.Int32, existingXpub.String); inventoryErr != nil {
			return "", 0, false, inventoryErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return "", 0, false, fmt.Errorf("failed to commit: %w", commitErr)
		}
		return strings.ToLower(addr), existingIndex.Int32, false, nil
	}

	// Create new address - get next derivation index atomically
	// The HD wallet starts at index 1 (index 0 is reserved for platform x402)
	index, xpub, err := h.hdwallet.GetNextNonZeroDerivationIndexTx(ctx, tx)
	if err != nil {
		return "", 0, false, fmt.Errorf("failed to get derivation index: %w", err)
	}

	address, err = DeriveAddressFromXpub(xpub, index)
	if err != nil {
		return "", 0, false, fmt.Errorf("failed to derive address: %w", err)
	}
	address = strings.ToLower(address)

	// Store the index on the tenant subscription
	result, err := tx.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions
		SET x402_address_index = $1, x402_address_xpub = $2, updated_at = NOW()
		WHERE tenant_id = $3
	`, index, xpub, tenantID)

	if err != nil {
		return "", 0, false, fmt.Errorf("failed to store deposit address index: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return "", 0, false, fmt.Errorf("failed to verify deposit address allocation: %w", err)
	}
	if rowsAffected != 1 {
		return "", 0, false, fmt.Errorf("tenant subscription not found for address allocation")
	}
	if inventoryErr := h.ensureX402CustodyInventoryTx(ctx, tx, tenantID, address, int32(index), xpub); inventoryErr != nil {
		return "", 0, false, inventoryErr
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

func (h *X402Handler) ensureX402CustodyInventoryTx(ctx context.Context, tx *sql.Tx, tenantID, address string, derivationIndex int32, derivationXpub string) error {
	for _, network := range X402Networks(config.X402IncludeTestnetsEnabled()) {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO purser.crypto_custody_addresses (
				tenant_id, source_kind, source_ref, network, asset, address, derivation_index, derivation_xpub
			) VALUES ($1, 'x402', $1, $2, 'USDC', LOWER($3), $4, $5)
			ON CONFLICT (network, asset, address) DO UPDATE
			SET tenant_id = EXCLUDED.tenant_id,
			    derivation_index = EXCLUDED.derivation_index,
			    derivation_xpub = EXCLUDED.derivation_xpub,
			    updated_at = NOW()
		`, tenantID, network.Name, address, derivationIndex, derivationXpub); err != nil {
			return fmt.Errorf("register x402 custody address for %s: %w", network.Name, err)
		}
	}
	return nil
}

// GetOrCreateTenantX402Address returns the stable tenant-bound x402 payTo.
func (h *X402Handler) GetOrCreateTenantX402Address(ctx context.Context, tenantID string) (address string, derivationIndex int32, newlyCreated bool, err error) {
	return h.GetTenantDepositAddress(ctx, tenantID)
}

// getXpub retrieves the stored extended public key
func (h *X402Handler) getXpub(ctx context.Context) (string, error) {
	var xpub string
	err := h.db.QueryRowContext(ctx, `SELECT xpub FROM purser.hd_wallet_state WHERE id = 1`).Scan(&xpub)
	if errors.Is(err, sql.ErrNoRows) {
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
// 6. 'to' matches the tenant-bound payTo address
func (h *X402Handler) VerifyPayment(ctx context.Context, tenantID string, payload *X402PaymentPayload, clientIP string) (*VerifyResult, error) {
	if payload == nil || payload.Payload == nil || payload.Payload.Authorization == nil {
		return &VerifyResult{Valid: false, Error: "invalid payload structure"}, nil
	}
	if payload.X402Version != 1 && payload.X402Version != 2 {
		return &VerifyResult{Valid: false, Error: "unsupported x402 version"}, nil
	}
	if payload.X402Version == 1 && config.IsProduction() && !config.GetEnvBool("X402_ALLOW_V1", false) {
		return &VerifyResult{Valid: false, Error: "x402 v1 compatibility is disabled"}, nil
	}
	if payload.Scheme != "exact" {
		return &VerifyResult{Valid: false, Error: "unsupported x402 scheme"}, nil
	}

	auth := payload.Payload.Authorization
	if tenantID == "" {
		return &VerifyResult{Valid: false, Error: "tenant-bound payment target required"}, nil
	}

	var quote *X402PaymentQuote
	var network NetworkConfig
	var err error
	facilitatorPayer := ""
	expectedPayTo := ""
	if payload.X402Version == 2 {
		quote, network, err = h.validateV2Quote(ctx, tenantID, payload)
		if err != nil {
			return &VerifyResult{Valid: false, Error: err.Error()}, nil
		}
		expectedPayTo = quote.PayTo
		facilitatorPayer, err = h.verifyWithFacilitator(ctx, payload, quote)
		if err != nil {
			return &VerifyResult{Valid: false, Error: err.Error()}, nil
		}
	} else {
		network, err = h.getNetworkConfig(payload.Network)
		if err != nil {
			return &VerifyResult{Valid: false, Error: err.Error()}, nil
		}
		// Bind the signed EIP-3009 recipient to the tenant that will receive credit.
		expectedPayTo, _, _, err = h.GetOrCreateTenantX402Address(ctx, tenantID)
		if err != nil {
			return nil, fmt.Errorf("failed to get tenant payTo address: %w", err)
		}
	}

	// Check 'to' matches this tenant's stable receiving address.
	if !strings.EqualFold(auth.To, expectedPayTo) {
		return &VerifyResult{Valid: false, Error: "invalid payTo address"}, nil
	}

	// Parse amount (USDC has 6 decimals)
	amountBig, err := parseUint256String(auth.Value)
	if err != nil {
		return &VerifyResult{Valid: false, Error: "invalid amount format"}, nil
	}

	// Convert to USD cents (USDC 6 decimals → cents)
	// 1 USDC = 100 cents = 1_000_000 base units
	// So: usd_cents = amount_base_units / 10_000
	centsDivisor := big.NewInt(10_000)
	if new(big.Int).Mod(amountBig, centsDivisor).Sign() != 0 {
		return &VerifyResult{Valid: false, Error: "amount must be in cent increments (multiple of 10000 base units)"}, nil
	}
	if amountBig.Sign() == 0 {
		return &VerifyResult{Valid: false, Error: "zero-value authorizations are not payments"}, nil
	}
	requiredAmount := h.RequiredTopupBaseUnits()
	if quote != nil {
		requiredAmount = quote.AmountAtomic
	}
	requiredBaseUnits, parsed := new(big.Int).SetString(requiredAmount, 10)
	if !parsed || amountBig.Cmp(requiredBaseUnits) != 0 {
		return &VerifyResult{Valid: false, Error: "payment amount does not match quote"}, nil
	}
	amountUsdCents := new(big.Int).Div(amountBig, centsDivisor).Int64()

	// Check time bounds
	now := time.Now().Unix()
	nowBig := big.NewInt(now)
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

	if validAfter.Cmp(nowBig) > 0 {
		return &VerifyResult{Valid: false, Error: "authorization not yet valid"}, nil
	}
	if validBefore.Cmp(validAfter) <= 0 {
		return &VerifyResult{Valid: false, Error: "invalid authorization validity window"}, nil
	}
	settlementDeadline := big.NewInt(time.Now().Add(x402SettlementValidityMargin).Unix())
	if validBefore.Cmp(settlementDeadline) < 0 {
		return &VerifyResult{Valid: false, Error: "authorization expires too soon to settle"}, nil
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
	if facilitatorPayer != "" && !strings.EqualFold(facilitatorPayer, signerAddr) {
		return &VerifyResult{Valid: false, Error: "facilitator payer does not match authorization signer"}, nil
	}

	// Check nonce not already used (on-chain check)
	nonceUsed, nonceErr := h.checkNonceUsed(ctx, network, auth.From, auth.Nonce)
	if nonceErr != nil {
		return &VerifyResult{Valid: false, Error: "failed to verify nonce on-chain"}, nil //nolint:nilerr // verification failures are returned in VerifyResult
	} else if nonceUsed {
		return &VerifyResult{Valid: false, Error: "nonce already used"}, nil
	}

	// Check also in our database (for in-flight transactions)
	var count int
	err = h.db.QueryRowContext(ctx, `
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
	balance, balanceErr := h.getUSDCBalance(ctx, network, auth.From)
	if balanceErr != nil {
		return &VerifyResult{Valid: false, Error: "failed to verify payer balance"}, nil //nolint:nilerr // verification failures are returned in VerifyResult
	} else if balance.Cmp(amountBig) < 0 {
		return &VerifyResult{Valid: false, Error: "insufficient USDC balance"}, nil
	}

	// Convert to EUR cents for ledger + VAT checks.
	amountEurCents := int64(0)
	if quote != nil {
		amountEurCents = quote.CreditAmountCents
	} else {
		amountEurCents, err = h.convertToEurCents(amountUsdCents)
		if err != nil {
			return &VerifyResult{Valid: false, Error: fmt.Sprintf("failed to convert amount to EUR: %v", err)}, nil
		}
	}

	// Simplified invoices may include exactly EUR 100; larger payments require
	// the full billing-details path.
	requiresBillingDetails := false
	if amountEurCents > 10000 {
		// Check if tenant has complete billing details (reuse billing details logic)
		var billingEmail sql.NullString
		var billingAddress []byte
		err = h.db.QueryRowContext(ctx, `
			SELECT billing_email, billing_address
			FROM purser.tenant_subscriptions
			WHERE tenant_id = $1 AND status != 'cancelled'
			ORDER BY created_at DESC
			LIMIT 1
		`, tenantID).Scan(&billingEmail, &billingAddress)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			h.logger.WithFields(logging.Fields{"error": err}).Warn("Failed to check billing details")
		}
		isComplete := isBillingDetailsComplete(billingEmail, billingAddress)
		requiresBillingDetails = !isComplete
	}

	return &VerifyResult{
		Valid:                  true,
		PayerAddress:           signerAddr,
		AmountCents:            amountEurCents,
		IsAuthOnly:             false,
		RequiresBillingDetails: requiresBillingDetails,
	}, nil
}

// RequiredTopupUSDCents is the exact amount accepted by the legacy v1 flow.
// Durable tenant-bound quotes replace this fixed amount in the v2 flow.
func (h *X402Handler) RequiredTopupUSDCents() int64 {
	if h == nil || h.topupUSDCents <= 0 || h.topupUSDCents > maximumX402TopupUSDCents {
		return defaultX402TopupUSDCents
	}
	return h.topupUSDCents
}

// RequiredTopupBaseUnits returns the required amount in USDC atomic units.
func (h *X402Handler) RequiredTopupBaseUnits() string {
	return new(big.Int).Mul(
		big.NewInt(h.RequiredTopupUSDCents()),
		big.NewInt(usdcBaseUnitsPerDollarCent),
	).String()
}

// SettlePayment submits the transferWithAuthorization transaction and waits for
// confirmed credit. Zero-value authorizations are rejected by VerifyPayment.
func (h *X402Handler) SettlePayment(ctx context.Context, tenantID string, payload *X402PaymentPayload, clientIP string) (*SettleResult, error) {
	existing, err := h.existingSettlementForPayload(ctx, tenantID, payload)
	if err != nil {
		var conflict x402SettlementConflictError
		if errors.As(err, &conflict) {
			return &SettleResult{Success: false, Error: err.Error()}, nil
		}
		return nil, err
	}
	if existing != nil {
		return h.buildIdempotentSettleResult(ctx, tenantID, existing, clientIP)
	}

	// First verify
	verifyResult, err := h.VerifyPayment(ctx, tenantID, payload, clientIP)
	if err != nil {
		return nil, err
	}
	if !verifyResult.Valid {
		return &SettleResult{Success: false, Error: verifyResult.Error}, nil
	}

	if verifyResult.RequiresBillingDetails {
		return &SettleResult{
			Success: false,
			Error:   "billing details required for payments over €100",
		}, nil
	}
	if err := h.enforceSettlementRateLimits(ctx, tenantID, clientIP, payload.Network, verifyResult.PayerAddress, payload.QuoteID); err != nil {
		return &SettleResult{Success: false, Error: err.Error()}, nil
	}
	if payload.X402Version == 2 {
		claimed, claimErr := h.claimPaymentQuote(ctx, payload.QuoteID)
		if claimErr != nil {
			return nil, claimErr
		}
		if !claimed {
			return &SettleResult{Success: false, Error: "x402 quote is already claimed; retry the same payment safely"}, nil
		}
	}

	// Get network config (already validated in VerifyPayment, but get it again for use here).
	var network NetworkConfig
	var settlementQuote *X402PaymentQuote
	if payload.X402Version == 2 {
		settlementQuote, network, err = h.validateV2Quote(ctx, tenantID, payload)
	} else {
		network, err = h.getNetworkConfig(payload.Network)
	}
	if err != nil {
		//nolint:nilerr // settlement failure returned in result struct, not as error
		return &SettleResult{Success: false, Error: err.Error()}, nil
	}

	auth := payload.Payload.Authorization

	// Durable intent first so a chain-broadcast failure or post-broadcast DB
	// failure is recoverable by the reconciler via authorizationState.
	nonceID, inserted, existing, err := h.recordSettlementIntent(ctx, network.Name, auth.From, auth.Nonce, tenantID, verifyResult.AmountCents, clientIP, payload)
	if err != nil {
		if payload.X402Version == 2 {
			_, _ = h.db.ExecContext(ctx, `
				UPDATE purser.x402_payment_quotes
				SET status = 'offered', claim_token = NULL, claim_expires_at = NULL, updated_at = NOW()
				WHERE id = $1 AND status = 'claiming'
			`, payload.QuoteID)
		}
		return &SettleResult{
			Success: false,
			Error:   fmt.Sprintf("failed to record settlement intent: %v", err),
		}, nil
	}
	if !inserted {
		return h.buildIdempotentSettleResult(ctx, tenantID, existing, clientIP)
	}

	var txHash string
	if payload.X402Version == 2 && h.facilitatorProvider != "self" {
		txHash, err = h.settleWithFacilitator(ctx, payload, settlementQuote)
	} else {
		txHash, err = h.submitTransferWithAuthorization(ctx, payload, network)
	}
	if err != nil {
		// Row stays in 'submitting'; reconciler will resolve via authorizationState.
		return &SettleResult{
			Success: false,
			Error:   fmt.Sprintf("settlement broadcast failed: %v", err),
		}, nil
	}

	if mErr := h.markSettlementSubmitted(ctx, nonceID, txHash); mErr != nil {
		h.logger.WithError(mErr).WithFields(logging.Fields{
			"nonce_id": nonceID,
			"tx_hash":  txHash,
		}).Error("Chain broadcast succeeded but DB update failed; reconciler will recover")
		return &SettleResult{
			Success: false,
			Error:   fmt.Sprintf("failed to record submission: %v", mErr),
		}, nil
	}

	receipt, err := h.waitForSettlementConfirmation(ctx, network, txHash, auth)
	if err != nil {
		if errors.Is(err, errX402TransactionReverted) {
			h.markSettlementFailed(ctx, nonceID, "transaction reverted on-chain")
			return &SettleResult{Success: false, TxHash: txHash, Error: "settlement reverted on-chain"}, nil
		}
		// The broadcast outcome is durable and remains pending. A timeout or RPC
		// error is an unknown outcome, not a failure: the reconciler or an
		// idempotent retry will resolve it from the canonical receipt.
		h.logger.WithError(err).WithFields(logging.Fields{
			"nonce_id": nonceID,
			"tx_hash":  txHash,
		}).Warn("x402 settlement is awaiting confirmation")
		if payload.X402Version == 2 {
			_, _ = h.db.ExecContext(ctx, `
				UPDATE purser.x402_payment_quotes
				SET status = 'unknown', updated_at = NOW()
				WHERE id = $1 AND status = 'settling'
			`, payload.QuoteID)
		}
		return &SettleResult{
			Success: false,
			TxHash:  txHash,
			Error:   "settlement pending confirmation; retry safely",
		}, nil
	}

	blockNumber := parseHexInt64(receipt.BlockNumber)
	gasUsed := parseHexInt64(receipt.GasUsed)
	newBalance, err := h.confirmAndCreditSettlement(ctx, tenantID, verifyResult.AmountCents, nonceID, txHash, blockNumber, gasUsed)
	if err != nil {
		return &SettleResult{
			Success: false,
			TxHash:  txHash,
			Error:   fmt.Sprintf("failed to commit confirmed settlement: %v", err),
		}, nil
	}

	invoiceNumber := h.finalizeConfirmedSettlementEffects(ctx, SettlementRow{
		ID:          nonceID,
		Network:     network.Name,
		TxHash:      txHash,
		TenantID:    tenantID,
		AmountCents: verifyResult.AmountCents,
		Status:      "confirmed",
		ClientIP:    clientIP,
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

// SettlementRow describes an existing x402_nonces row returned when an
// idempotent settle hits a row already inserted by an earlier request.
type SettlementRow struct {
	ID          string
	Network     string
	TxHash      string
	TenantID    string
	AmountCents int64
	Status      string
	ClientIP    string
	AuthPayload string
}

type x402SettlementConflictError struct {
	msg string
}

func (e x402SettlementConflictError) Error() string {
	return e.msg
}

func newX402SettlementConflict(msg string) error {
	return x402SettlementConflictError{msg: msg}
}

func (h *X402Handler) existingSettlementForPayload(ctx context.Context, tenantID string, payload *X402PaymentPayload) (*SettlementRow, error) {
	if payload == nil || payload.Payload == nil || payload.Payload.Authorization == nil {
		return nil, nil
	}
	auth := payload.Payload.Authorization
	if strings.TrimSpace(auth.Value) == "" {
		return nil, nil
	}
	amount, ok := new(big.Int).SetString(auth.Value, 10)
	if !ok || amount.Sign() == 0 {
		return nil, nil
	}
	storageNetwork := payload.Network
	if payload.X402Version == 2 {
		if network, err := h.networkForCAIP2(payload.Network); err == nil {
			storageNetwork = network.Name
		}
	}

	var (
		row          SettlementRow
		storedHash   sql.NullString
		storedPay    sql.NullString
		storedTenant string
	)
	err := h.db.QueryRowContext(ctx, `
		SELECT id, network, tx_hash, tenant_id, amount_cents, status, auth_payload::text, COALESCE(client_ip::text, '')
		FROM purser.x402_nonces
		WHERE network = $1 AND payer_address = $2 AND nonce = $3
	`, storageNetwork, strings.ToLower(auth.From), auth.Nonce).
		Scan(&row.ID, &row.Network, &storedHash, &storedTenant, &row.AmountCents, &row.Status, &storedPay, &row.ClientIP)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("check existing settlement: %w", err)
	}
	if storedTenant != tenantID {
		return nil, newX402SettlementConflict("nonce already used by another tenant")
	}
	row.TenantID = storedTenant
	if storedPay.Valid && storedPay.String != "" && !sameX402Payload(storedPay.String, payload) {
		return nil, newX402SettlementConflict("nonce already used for a different authorization")
	}
	row.TxHash = storedHash.String
	row.AuthPayload = storedPay.String
	return &row, nil
}

func sameX402Payload(stored string, current *X402PaymentPayload) bool {
	var prev X402PaymentPayload
	if err := json.Unmarshal([]byte(stored), &prev); err != nil {
		return false
	}
	if current == nil || current.Payload == nil || current.Payload.Authorization == nil ||
		prev.Payload == nil || prev.Payload.Authorization == nil {
		return false
	}
	a, b := prev.Payload.Authorization, current.Payload.Authorization
	return prev.X402Version == current.X402Version &&
		prev.Scheme == current.Scheme &&
		prev.Network == current.Network &&
		prev.Payload.Signature == current.Payload.Signature &&
		a.From == b.From &&
		a.To == b.To &&
		a.Value == b.Value &&
		a.ValidAfter == b.ValidAfter &&
		a.ValidBefore == b.ValidBefore &&
		a.Nonce == b.Nonce
}

// recordSettlementIntent writes the durable pre-submit record. If a row for
// (network, payer, nonce) already exists, returns inserted=false and the
// existing row. The auth payload is stored as JSONB so the reconciler can
// replay the authorization on submit failure.
func (h *X402Handler) recordSettlementIntent(ctx context.Context, network, payerAddress, nonce, tenantID string, amountCents int64, clientIP string, payload *X402PaymentPayload) (string, bool, *SettlementRow, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", false, nil, fmt.Errorf("marshal auth payload: %w", err)
	}

	var (
		nonceID      string
		storedTxHash sql.NullString
		storedPay    sql.NullString
		storedTenant string
		storedAmount int64
		storedStatus string
		storedIP     string
		inserted     bool
	)
	err = h.db.QueryRowContext(ctx, `
		INSERT INTO purser.x402_nonces (
			network, payer_address, nonce, tenant_id, amount_cents,
			auth_payload, client_ip, status, settled_at, last_submit_attempt_at,
			quote_id, settlement_provider
		) VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, '')::inet, 'submitting', NOW(), NOW(), NULLIF($8, '')::uuid, $9)
		ON CONFLICT (network, payer_address, nonce) DO UPDATE
		SET tenant_id = purser.x402_nonces.tenant_id
		RETURNING id, tx_hash, tenant_id, amount_cents, status, auth_payload::text, COALESCE(client_ip::text, ''), (xmax = 0) AS inserted
	`, network, strings.ToLower(payerAddress), nonce, tenantID, amountCents, database.JSONText(payloadJSON), clientIP, payload.QuoteID, h.facilitatorProvider).
		Scan(&nonceID, &storedTxHash, &storedTenant, &storedAmount, &storedStatus, &storedPay, &storedIP, &inserted)
	if err != nil {
		return "", false, nil, err
	}

	if storedTenant != tenantID {
		return "", false, nil, newX402SettlementConflict("nonce already used by another tenant")
	}
	if storedAmount != amountCents {
		return "", false, nil, newX402SettlementConflict("nonce already used for a different amount")
	}
	if !inserted && storedPay.Valid && storedPay.String != "" && !sameX402Payload(storedPay.String, payload) {
		return "", false, nil, newX402SettlementConflict("nonce already used for a different authorization")
	}

	if inserted {
		return nonceID, true, nil, nil
	}
	return nonceID, false, &SettlementRow{
		ID:          nonceID,
		Network:     network,
		TxHash:      storedTxHash.String,
		TenantID:    storedTenant,
		AmountCents: storedAmount,
		Status:      storedStatus,
		ClientIP:    storedIP,
		AuthPayload: storedPay.String,
	}, nil
}

// markSettlementSubmitted promotes a 'submitting' row to 'pending' once the
// on-chain transferWithAuthorization broadcast has returned a tx hash.
func (h *X402Handler) markSettlementSubmitted(ctx context.Context, nonceID, txHash string) error {
	res, err := h.db.ExecContext(ctx, `
		UPDATE purser.x402_nonces
		SET tx_hash = $2, submitted_at = NOW(), status = 'pending'
		WHERE id = $1 AND status = 'submitting'
	`, nonceID, txHash)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("intent %s not in submitting state", nonceID)
	}
	_, _ = h.db.ExecContext(ctx, `
		UPDATE purser.x402_payment_quotes q
		SET status = 'settling', updated_at = NOW()
		FROM purser.x402_nonces n
		WHERE n.id = $1 AND q.id = n.quote_id AND q.status = 'claiming'
	`, nonceID)
	return nil
}

func (h *X402Handler) markSettlementFailed(ctx context.Context, nonceID, reason string) {
	if _, err := h.db.ExecContext(ctx, `
		UPDATE purser.x402_nonces
		SET status = 'failed', failure_reason = $2
		WHERE id = $1 AND status IN ('submitting', 'pending')
	`, nonceID, reason); err != nil {
		h.logger.WithError(err).WithField("nonce_id", nonceID).Error("Failed to mark x402 settlement failed")
	}
	_, _ = h.db.ExecContext(ctx, `
		UPDATE purser.x402_payment_quotes q
		SET status = 'failed', failure_reason = $2, updated_at = NOW()
		FROM purser.x402_nonces n
		WHERE n.id = $1 AND q.id = n.quote_id
		  AND q.status IN ('claiming', 'settling', 'unknown')
	`, nonceID, reason)
}

// confirmAndCreditSettlement makes confirmed chain state and spendable credit
// visible in one database transaction. The settlement row is locked before the
// tenant balance, giving concurrent handler/reconciler attempts one ordering.
func (h *X402Handler) confirmAndCreditSettlement(ctx context.Context, tenantID string, amountCents int64, nonceID, txHash string, blockNumber, gasUsed int64) (int64, error) {
	return confirmAndCreditX402Settlement(ctx, h.db, tenantID, amountCents, nonceID, txHash, blockNumber, gasUsed)
}

func confirmAndCreditX402Settlement(ctx context.Context, db *sql.DB, tenantID string, amountCents int64, nonceID, txHash string, blockNumber, gasUsed int64) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	var storedStatus, storedTenant string
	var storedAmount int64
	var storedTxHash sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT status, tenant_id, amount_cents, tx_hash
		FROM purser.x402_nonces
		WHERE id = $1
		FOR UPDATE
	`, nonceID).Scan(&storedStatus, &storedTenant, &storedAmount, &storedTxHash)
	if err != nil {
		return 0, err
	}
	if storedTenant != tenantID || storedAmount != amountCents || !storedTxHash.Valid || !strings.EqualFold(storedTxHash.String, txHash) {
		return 0, fmt.Errorf("settlement identity changed while confirming")
	}
	if storedStatus == "failed" || storedStatus == "submitting" {
		return 0, fmt.Errorf("settlement is in non-confirmable state %q", storedStatus)
	}

	newBalance, err := creditX402PrepaidBalanceTx(ctx, tx, tenantID, amountCents, nonceID, txHash, "x402 USDC payment")
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE purser.x402_nonces
		SET status = 'confirmed',
			confirmed_at = COALESCE(confirmed_at, NOW()),
			block_number = $2,
			gas_used = $3,
			failure_reason = NULL
		WHERE id = $1
	`, nonceID, blockNumber, gasUsed)
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE purser.x402_payment_quotes q
		SET status = 'confirmed', provider_transaction_id = $2,
		    confirmed_at = COALESCE(confirmed_at, NOW()), updated_at = NOW(),
		    claim_expires_at = NULL, failure_reason = NULL
		FROM purser.x402_nonces n
		WHERE n.id = $1 AND q.id = n.quote_id
	`, nonceID, txHash)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return newBalance, nil
}

func (h *X402Handler) buildIdempotentSettleResult(ctx context.Context, tenantID string, row *SettlementRow, clientIP string) (*SettleResult, error) {
	if row == nil {
		return &SettleResult{Success: false, Error: "settlement unavailable"}, nil
	}
	if row.Status == "failed" {
		return &SettleResult{
			Success: false,
			Error:   "nonce already used",
		}, nil
	}
	if row.Status == "submitting" {
		// Concurrent settle: the first call is mid-flight and the chain
		// broadcast outcome is not yet known. Let the caller retry.
		return &SettleResult{
			Success: false,
			Error:   "settlement in progress",
		}, nil
	}

	if row.TxHash != "" {
		creditExists, err := h.x402BalanceTransactionExists(ctx, tenantID, row.ID, "x402_payment", "topup")
		if err != nil {
			return nil, err
		}
		if !creditExists || row.Status != "confirmed" {
			if row.Network == "" {
				return &SettleResult{Success: false, TxHash: row.TxHash, Error: "settlement pending confirmation; retry safely"}, nil
			}
			network, networkErr := h.getNetworkConfig(row.Network)
			if networkErr != nil {
				return nil, networkErr
			}
			auth, authErr := authorizationFromStoredPayload(row.AuthPayload)
			if authErr != nil {
				return nil, authErr
			}
			receipt, waitErr := h.waitForSettlementConfirmation(ctx, network, row.TxHash, auth)
			if waitErr != nil {
				if errors.Is(waitErr, errX402TransactionReverted) {
					h.markSettlementFailed(ctx, row.ID, "transaction reverted on-chain")
					return &SettleResult{Success: false, TxHash: row.TxHash, Error: "settlement reverted on-chain"}, nil
				}
				return &SettleResult{Success: false, TxHash: row.TxHash, Error: "settlement pending confirmation; retry safely"}, nil
			}
			if _, confirmErr := h.confirmAndCreditSettlement(ctx, tenantID, row.AmountCents, row.ID, row.TxHash, parseHexInt64(receipt.BlockNumber), parseHexInt64(receipt.GasUsed)); confirmErr != nil {
				return nil, confirmErr
			}
			row.Status = "confirmed"
		}
	}

	currentBalance, err := h.getCurrentBalance(ctx, tenantID, billing.DefaultCurrency())
	if err != nil {
		return nil, err
	}

	if row.ClientIP == "" {
		row.ClientIP = clientIP
	}
	invoiceNumber := h.finalizeConfirmedSettlementEffects(ctx, *row)

	return &SettleResult{
		Success:         true,
		TxHash:          row.TxHash,
		CreditedCents:   row.AmountCents,
		Currency:        billing.DefaultCurrency(),
		NewBalanceCents: currentBalance,
		InvoiceNumber:   invoiceNumber,
	}, nil
}

// recoverEIP3009Signer recovers the signer address from an EIP-3009 transferWithAuthorization
// The signature is over the EIP-712 typed data hash
func (h *X402Handler) recoverEIP3009Signer(payload *X402PaymentPayload, network NetworkConfig) (string, error) {
	// EIP-712 domain for USDC on the specified network
	domainSeparator := h.getUSDCDomainSeparator(network)

	// Build the TransferWithAuthorization struct hash
	auth := payload.Payload.Authorization
	structHash, err := h.hashTransferWithAuthorization(auth)
	if err != nil {
		return "", fmt.Errorf("encode authorization: %w", err)
	}

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

	recoveredAddr, err := ecrecover(messageHash, r, s, v)
	if err != nil {
		return "", fmt.Errorf("ecrecover failed: %w", err)
	}

	return recoveredAddr, nil
}

// getUSDCDomainSeparator returns the EIP-712 domain separator for USDC on a given network
func (h *X402Handler) getUSDCDomainSeparator(network NetworkConfig) []byte {
	// EIP-712 domain:
	// name: the contract's live name() value (normally "USD Coin"; Base
	// Sepolia's Circle test token uses "USDC")
	// version: "2" (Circle's USDC v2)
	// chainId: network-specific
	// verifyingContract: USDC contract address on that network

	chainId := big.NewInt(network.ChainID)

	// keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")
	typeHash := keccak256([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))

	domainName := network.USDCDomainName
	if domainName == "" {
		domainName = "USD Coin"
	}
	nameHash := keccak256([]byte(domainName))
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

// hashTransferWithAuthorization computes the EIP-712 struct hash for
// TransferWithAuthorization. It returns an error rather than panicking or
// silently zero-padding when a field is not a valid address / uint256 / bytes32,
// so a malformed authorization can never be signed or replayed as a valid one.
func (h *X402Handler) hashTransferWithAuthorization(auth *X402Authorization) ([]byte, error) {
	// keccak256("TransferWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)")
	typeHash := keccak256([]byte("TransferWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)"))

	from, err := padAddress(auth.From)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	to, err := padAddress(auth.To)
	if err != nil {
		return nil, fmt.Errorf("to: %w", err)
	}
	value, err := padUint256(auth.Value)
	if err != nil {
		return nil, fmt.Errorf("value: %w", err)
	}
	validAfter, err := padUint256(auth.ValidAfter)
	if err != nil {
		return nil, fmt.Errorf("validAfter: %w", err)
	}
	validBefore, err := padUint256(auth.ValidBefore)
	if err != nil {
		return nil, fmt.Errorf("validBefore: %w", err)
	}
	nonce, err := padBytes32(auth.Nonce)
	if err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}

	return keccak256(
		typeHash,
		from,
		to,
		value,
		validAfter,
		validBefore,
		nonce,
	), nil
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

	from, err := padAddress(auth.From)
	if err != nil {
		return "", fmt.Errorf("from: %w", err)
	}
	to, err := padAddress(auth.To)
	if err != nil {
		return "", fmt.Errorf("to: %w", err)
	}
	value, err := padUint256(auth.Value)
	if err != nil {
		return "", fmt.Errorf("value: %w", err)
	}
	validAfter, err := padUint256(auth.ValidAfter)
	if err != nil {
		return "", fmt.Errorf("validAfter: %w", err)
	}
	validBefore, err := padUint256(auth.ValidBefore)
	if err != nil {
		return "", fmt.Errorf("validBefore: %w", err)
	}
	nonceBytes, err := padBytes32(auth.Nonce)
	if err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}

	callData := methodID
	callData = append(callData, from...)
	callData = append(callData, to...)
	callData = append(callData, value...)
	callData = append(callData, validAfter...)
	callData = append(callData, validBefore...)
	callData = append(callData, nonceBytes...)
	callData = append(callData, padUint8(v)...)
	callData = append(callData, padBytes32Bytes(r)...)
	callData = append(callData, padBytes32Bytes(s)...)

	// Simulate via eth_call before submitting (per x402 spec)
	if simErr := h.simulateTransfer(ctx, network, callData); simErr != nil {
		return "", fmt.Errorf("simulation failed: %w", simErr)
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
	// The relayer account has one nonce sequence per chain. Serialize nonce
	// selection and broadcast across all Purser replicas so distinct valid
	// authorizations cannot sign competing transactions with the same nonce.
	// The durable authorization row resolves an unknown commit/broadcast result.
	lockTx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin relayer nonce lock: %w", err)
	}
	defer lockTx.Rollback() //nolint:errcheck // rollback releases the advisory lock
	lockKey := fmt.Sprintf("x402-relayer:%d:%s", network.ChainID, strings.ToLower(h.gasWalletAddress))
	if _, lockErr := lockTx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); lockErr != nil {
		return "", fmt.Errorf("acquire relayer nonce lock: %w", lockErr)
	}

	// Get nonce for gas wallet
	nonce, err := h.getNonce(ctx, network, h.gasWalletAddress)
	if err != nil {
		return "", fmt.Errorf("failed to get nonce: %w", err)
	}

	// Get fee market inputs for EIP-1559 fee caps.
	gasPrice, err := h.getGasPrice(ctx, network)
	if err != nil {
		return "", fmt.Errorf("failed to get gas price: %w", err)
	}
	priorityFee, err := h.getPriorityFee(ctx, network)
	if err != nil {
		return "", fmt.Errorf("failed to get priority fee: %w", err)
	}

	// Build an EIP-1559 dynamic-fee transaction.
	gasLimit := uint64(150000) // Conservative estimate for transferWithAuthorization

	chainId := big.NewInt(network.ChainID)
	maxPriorityFeePerGas := priorityFee
	maxFeePerGas := new(big.Int).Mul(gasPrice, big.NewInt(2))
	maxFeePerGas.Add(maxFeePerGas, maxPriorityFeePerGas)

	// Sign transaction
	signedTx, err := h.signDynamicFeeTransaction(nonce, to, big.NewInt(0), gasLimit, maxFeePerGas, maxPriorityFeePerGas, data, chainId)
	if err != nil {
		return "", fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Submit via eth_sendRawTransaction
	var txHash string
	err = h.rpc.Call(ctx, network, "eth_sendRawTransaction", []any{"0x" + hex.EncodeToString(signedTx)}, &txHash)
	if err != nil {
		return "", fmt.Errorf("eth_sendRawTransaction failed: %w", err)
	}
	if err := lockTx.Commit(); err != nil {
		return "", fmt.Errorf("release relayer nonce lock after broadcast: %w", err)
	}

	return txHash, nil
}

// waitForSettlementConfirmation waits until the transaction has a successful
// receipt at the network's consensus-labelled finality head. A deadline or RPC
// failure leaves the durable settlement in pending state for safe retry and
// background reconciliation; it never creates provisional balance credit.
func (h *X402Handler) waitForSettlementConfirmation(ctx context.Context, network NetworkConfig, txHash string, auth *X402Authorization) (*TransactionReceipt, error) {
	if h.rpc == nil {
		return nil, fmt.Errorf("RPC client unavailable")
	}

	ticker := time.NewTicker(x402ReceiptPollInterval)
	defer ticker.Stop()

	for {
		var receipt *TransactionReceipt
		if err := h.rpc.Call(ctx, network, "eth_getTransactionReceipt", []any{txHash}, &receipt); err != nil {
			return nil, fmt.Errorf("get transaction receipt: %w", err)
		}
		if receipt != nil {
			if receipt.Status != "0x1" {
				return nil, errX402TransactionReverted
			}
			if err := validateX402TransferReceipt(receipt, network, auth); err != nil {
				return nil, err
			}

			blockNumber := parseHexInt64(receipt.BlockNumber)
			finality, err := GetFinalityHead(ctx, h.rpc, network)
			if err != nil {
				return nil, err
			}
			if blockNumber > 0 && finality.Number >= blockNumber {
				var canonical rpcBlock
				if err := h.rpc.Call(ctx, network, "eth_getBlockByNumber", []any{receipt.BlockNumber, false}, &canonical); err != nil {
					return nil, fmt.Errorf("get canonical receipt block: %w", err)
				}
				if receipt.BlockHash != "" && !strings.EqualFold(receipt.BlockHash, canonical.Hash) {
					return nil, fmt.Errorf("receipt block is not canonical")
				}
				return receipt, nil
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func authorizationFromStoredPayload(raw string) (*X402Authorization, error) {
	var payload X402PaymentPayload
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &payload) != nil ||
		payload.Payload == nil || payload.Payload.Authorization == nil {
		return nil, fmt.Errorf("stored x402 authorization is invalid")
	}
	return payload.Payload.Authorization, nil
}

func validateX402TransferReceipt(receipt *TransactionReceipt, network NetworkConfig, auth *X402Authorization) error {
	if receipt == nil || auth == nil {
		return fmt.Errorf("x402 receipt or authorization is missing")
	}
	amount, err := parseUint256String(auth.Value)
	if err != nil {
		return fmt.Errorf("x402 receipt authorization amount: %w", err)
	}
	from, err := padAddress(auth.From)
	if err != nil {
		return fmt.Errorf("x402 receipt authorization from: %w", err)
	}
	to, err := padAddress(auth.To)
	if err != nil {
		return fmt.Errorf("x402 receipt authorization to: %w", err)
	}
	transferTopic := "0x" + hex.EncodeToString(keccak256([]byte("Transfer(address,address,uint256)")))
	wantFrom := "0x" + hex.EncodeToString(from)
	wantTo := "0x" + hex.EncodeToString(to)
	for _, log := range receipt.Logs {
		if !strings.EqualFold(log.Address, network.USDCContract) || len(log.Topics) < 3 ||
			!strings.EqualFold(log.Topics[0], transferTopic) ||
			!strings.EqualFold(log.Topics[1], wantFrom) || !strings.EqualFold(log.Topics[2], wantTo) {
			continue
		}
		value := new(big.Int)
		if _, ok := value.SetString(strings.TrimPrefix(log.Data, "0x"), 16); ok && value.Cmp(amount) == 0 {
			return nil
		}
	}
	return fmt.Errorf("confirmed transaction lacks the exact USDC Transfer event")
}

func (h *X402Handler) creditPrepaidBalanceTx(ctx context.Context, tx *sql.Tx, tenantID string, amountCents int64, nonceID string, txHash string, description string) (int64, error) {
	return creditX402PrepaidBalanceTx(ctx, tx, tenantID, amountCents, nonceID, txHash, description)
}

func creditX402PrepaidBalanceTx(ctx context.Context, tx *sql.Tx, tenantID string, amountCents int64, nonceID string, txHash string, description string) (int64, error) {
	currency := billing.DefaultCurrency()

	var existingBalanceAfter int64
	err := tx.QueryRowContext(ctx, `
		SELECT balance_after_cents FROM purser.balance_transactions
		WHERE tenant_id = $1 AND reference_type = 'x402_payment' AND reference_id = $2
	`, tenantID, nonceID).Scan(&existingBalanceAfter)
	if err == nil {
		return existingBalanceAfter, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency, updated_at)
		VALUES ($1, 0, $2, NOW())
		ON CONFLICT (tenant_id, currency) DO NOTHING
	`, tenantID, currency)
	if err != nil {
		return 0, err
	}

	var currentBalance int64
	err = tx.QueryRowContext(ctx, `
		SELECT balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2
		FOR UPDATE
	`, tenantID, currency).Scan(&currentBalance)
	if err != nil {
		return 0, err
	}

	newBalance := currentBalance + amountCents

	_, err = tx.ExecContext(ctx, `
		UPDATE purser.prepaid_balances
		SET balance_cents = $1, updated_at = NOW()
		WHERE tenant_id = $2 AND currency = $3
	`, newBalance, tenantID, currency)
	if err != nil {
		return 0, err
	}

	// reference_id is UUID; link to the x402_nonces row that owns this settlement.
	// The txHash is preserved in the description for human inspection.
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
		fmt.Sprintf("%s (%s)", description, truncateTxHash(txHash)),
		nonceID,
	)
	if err != nil {
		return 0, err
	}

	return newBalance, nil
}

func (h *X402Handler) x402BalanceTransactionExists(ctx context.Context, tenantID, nonceID, referenceType, transactionType string) (bool, error) {
	var exists bool
	err := h.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM purser.balance_transactions
			WHERE tenant_id = $1 AND reference_id = $2 AND reference_type = $3 AND transaction_type = $4
		)
	`, tenantID, nonceID, referenceType, transactionType).Scan(&exists)
	return exists, err
}

func (h *X402Handler) getCurrentBalance(ctx context.Context, tenantID, currency string) (int64, error) {
	var balance int64
	err := h.db.QueryRowContext(ctx, `
		SELECT balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2
	`, tenantID, currency).Scan(&balance)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return balance, nil
}

// generateCryptoTopupInvoice records VAT when confirmed crypto credit is issued;
// later usage consumes that already-invoiced credit without another VAT event.
func (h *X402Handler) generateCryptoTopupInvoice(ctx context.Context, tenantID string, amountEurCents int64, referenceType, referenceID, clientIP, networkName string) (string, error) {
	// Supplier info required for invoicing
	if h.supplierName == "" || h.supplierAddress == "" || h.supplierVAT == "" || len(h.supplierCountry) != 2 {
		return "", fmt.Errorf("supplier information not configured for x402 invoicing")
	}

	// Get actual ECB rate for record
	ecbRate, err := h.getEurUsdRate()
	if err != nil {
		return "", fmt.Errorf("failed to get ECB rate: %w", err)
	}

	// Select an effective-dated VAT rate and retain its source on the record.
	vatDecision, err := h.getVATDecisionForTenant(ctx, tenantID, clientIP, time.Now().UTC())
	if err != nil {
		return "", err
	}
	vatRateBps, reverseCharge := vatDecision.RateBPS, vatDecision.ReverseCharge
	netAmountCents, vatAmountCents := extractVATInclusive(amountEurCents, vatRateBps)
	billingCountry := h.getBillingCountry(ctx, tenantID)
	ipCountry := h.getCountryFromIP(clientIP)
	evidenceStatus := "missing"
	evidenceConflict := false
	switch {
	case billingCountry != "" && ipCountry != "" && billingCountry == ipCountry:
		evidenceStatus = "complete"
	case billingCountry != "" && ipCountry != "" && billingCountry != ipCountry:
		evidenceStatus = "conflict"
		evidenceConflict = true
	case billingCountry != "" || ipCountry != "":
		evidenceStatus = "single_source"
	}
	taxValidationStatus := "not_validated"
	if reverseCharge {
		taxValidationStatus = "reverse_charge"
	} else if vatDecision.VIESValidated {
		taxValidationStatus = "vies_valid_domestic"
	} else if evidenceStatus != "complete" {
		taxValidationStatus = "location_review"
	}

	invoiceTx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin simplified invoice transaction: %w", err)
	}
	defer invoiceTx.Rollback() //nolint:errcheck // rollback is best-effort
	lockKey := tenantID + ":" + referenceType + ":" + strings.ToLower(referenceID)
	if _, lockErr := invoiceTx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); lockErr != nil {
		return "", fmt.Errorf("lock simplified invoice reference: %w", lockErr)
	}

	var existingInvoice string
	err = invoiceTx.QueryRowContext(ctx, `
		SELECT invoice_number
		FROM purser.simplified_invoices
		WHERE tenant_id = $1 AND reference_type = $2 AND reference_id = $3
		ORDER BY issued_at ASC
		LIMIT 1
	`, tenantID, referenceType, referenceID).Scan(&existingInvoice)
	if err == nil {
		if commitErr := invoiceTx.Commit(); commitErr != nil {
			return "", commitErr
		}
		return existingInvoice, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("check existing simplified invoice: %w", err)
	}

	var invoiceSequence int64
	if sequenceErr := invoiceTx.QueryRowContext(ctx, `SELECT nextval('purser.simplified_invoice_number_seq')`).Scan(&invoiceSequence); sequenceErr != nil {
		return "", fmt.Errorf("allocate simplified invoice number: %w", sequenceErr)
	}

	// Generate invoice number (SI = simplified invoice, B2B for reverse charge).
	prefix := "SI"
	if reverseCharge {
		prefix = "B2B" // Reverse charge invoice
	}
	invoiceNumber := fmt.Sprintf("%s-%010d", prefix, invoiceSequence)

	_, err = invoiceTx.ExecContext(ctx, `
		INSERT INTO purser.simplified_invoices (
			invoice_number, tenant_id, reference_type, reference_id,
			gross_amount_cents, net_amount_cents, vat_amount_cents, vat_rate_bps,
			vat_rate_source, vat_rate_table_checked_on, vat_rate_effective_from, tax_validation_status,
			currency, amount_eur_cents, ecb_rate, fx_rate_source, fx_rate_observed_at,
			evidence_ip_country, evidence_wallet_network,
			evidence_billing_country, evidence_status, evidence_conflict,
			tax_policy_ref,
			supplier_name, supplier_address, supplier_vat_number,
			issued_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::date, $11::date, $12, $13, $14, $15, $16, NOW(), $17, $18, $19, $20, $21, $22, $23, $24, $25, NOW())
	`,
		invoiceNumber, tenantID, referenceType, referenceID,
		amountEurCents, netAmountCents, vatAmountCents, vatRateBps,
		vatDecision.Source, vatDecision.CheckedOn, vatDecision.EffectiveFrom,
		taxValidationStatus, billing.DefaultCurrency(), amountEurCents, ecbRate, "European Central Bank reference rate",
		ipCountry, networkName, billingCountry, evidenceStatus, evidenceConflict,
		CryptoTopupTaxPolicyRef,
		h.supplierName, h.supplierAddress, h.supplierVAT,
	)

	if err != nil {
		return "", fmt.Errorf("failed to insert simplified invoice: %w", err)
	}
	if taxValidationStatus == "location_review" {
		_, err = invoiceTx.ExecContext(ctx, `
			INSERT INTO purser.crypto_accounting_anomalies (
				tenant_id, kind, network, reference_type, reference_id,
				amount_cents, currency, detail, evidence_json
			) VALUES ($1, 'tax_location_evidence_review', $2, 'simplified_invoice', $3, $4, 'EUR', $5,
			          jsonb_build_object('billing_country', $6::text, 'ip_country', $7::text, 'evidence_status', $8::text))
			ON CONFLICT (kind, reference_type, reference_id) DO UPDATE
			SET last_seen_at = NOW(), occurrences = purser.crypto_accounting_anomalies.occurrences + 1,
			    detail = EXCLUDED.detail, evidence_json = EXCLUDED.evidence_json
		`, tenantID, networkName, invoiceNumber, amountEurCents,
			"two non-conflicting qualifying customer-location signals are required", billingCountry, ipCountry, evidenceStatus)
		if err != nil {
			return "", fmt.Errorf("queue invoice location review: %w", err)
		}
	}
	if err := invoiceTx.Commit(); err != nil {
		return "", fmt.Errorf("commit simplified invoice: %w", err)
	}

	return invoiceNumber, nil
}

// extractVATInclusive splits an integer-cent VAT-inclusive gross amount using
// gross * rate / (100% + rate), rounded half-up to the nearest cent.
func extractVATInclusive(grossCents int64, rateBps int) (netCents, vatCents int64) {
	if grossCents <= 0 || rateBps <= 0 {
		return grossCents, 0
	}
	denominator := int64(10_000 + rateBps)
	vatCents = (grossCents*int64(rateBps) + denominator/2) / denominator
	return grossCents - vatCents, vatCents
}

// finalizeConfirmedSettlementEffects applies the post-credit accounting work
// for either the request path or the background reconciler. Invoice creation
// is reference-idempotent and the rollup marker is locked with the rollup
// update, so concurrent callers cannot double-count a confirmed settlement.
func (h *X402Handler) finalizeConfirmedSettlementEffects(ctx context.Context, row SettlementRow) string {
	if row.Status != "confirmed" || row.ID == "" || row.TxHash == "" || row.Network == "" || row.TenantID == "" {
		return ""
	}

	invoiceNumber, err := h.generateCryptoTopupInvoice(ctx, row.TenantID, row.AmountCents, "x402_payment", row.TxHash, row.ClientIP, row.Network)
	if err != nil {
		h.logger.WithError(err).WithFields(logging.Fields{
			"nonce_id": row.ID,
			"tx_hash":  row.TxHash,
		}).Error("Failed to ensure x402 simplified invoice")
	}

	applied, rollupErr := h.applyX402RollupOnce(ctx, row.TenantID, row.ID, row.AmountCents)
	if rollupErr != nil {
		h.logger.WithError(rollupErr).WithField("nonce_id", row.ID).Error("Failed to apply x402 balance rollup")
	} else if applied {
		emitBillingEvent(h.db, h.logger, eventX402SettlementConfirm, row.TenantID, "x402_nonce", row.TxHash, &ipcpb.BillingEvent{
			Amount:   float64(row.AmountCents) / 100,
			Currency: billing.DefaultCurrency(),
			Status:   "confirmed",
		})
		if h.commodoreClient != nil {
			if _, cacheErr := h.commodoreClient.InvalidateTenantCache(ctx, row.TenantID, "x402 balance top-up"); cacheErr != nil {
				h.logger.WithError(cacheErr).WithField("tenant_id", row.TenantID).Warn("Failed to invalidate tenant cache after x402 settlement")
			}
		}
	}
	return invoiceNumber
}

func (h *X402Handler) applyX402RollupOnce(ctx context.Context, tenantID, nonceID string, amountEurCents int64) (bool, error) {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	var storedTenant, status string
	var storedAmount int64
	var appliedAt, reversedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `
		SELECT tenant_id, amount_cents, status, rollup_applied_at, rollup_reversed_at
		FROM purser.x402_nonces
		WHERE id = $1
		FOR UPDATE
	`, nonceID).Scan(&storedTenant, &storedAmount, &status, &appliedAt, &reversedAt)
	if err != nil {
		return false, err
	}
	if storedTenant != tenantID || storedAmount != amountEurCents || status != "confirmed" {
		return false, fmt.Errorf("settlement identity changed while applying rollup")
	}
	if appliedAt.Valid && !reversedAt.Valid {
		return false, tx.Commit()
	}

	_, err = tx.ExecContext(ctx, `
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
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE purser.x402_nonces
		SET rollup_applied_at = NOW(), rollup_reversed_at = NULL
		WHERE id = $1
	`, nonceID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func reverseX402RollupOnce(ctx context.Context, db *sql.DB, tenantID, nonceID string, amountEurCents int64) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	var storedTenant, status string
	var storedAmount int64
	var appliedAt, reversedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `
		SELECT tenant_id, amount_cents, status, rollup_applied_at, rollup_reversed_at
		FROM purser.x402_nonces
		WHERE id = $1
		FOR UPDATE
	`, nonceID).Scan(&storedTenant, &storedAmount, &status, &appliedAt, &reversedAt)
	if err != nil {
		return false, err
	}
	if storedTenant != tenantID || storedAmount != amountEurCents || status != "failed" {
		return false, fmt.Errorf("settlement identity changed while reversing rollup")
	}
	if !appliedAt.Valid || reversedAt.Valid {
		return false, tx.Commit()
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE purser.tenant_balance_rollups
		SET total_topup_cents = total_topup_cents - $2,
			total_topup_eur_cents = total_topup_eur_cents - $2,
			topup_count = topup_count - 1,
			updated_at = NOW()
		WHERE tenant_id = $1
		  AND total_topup_cents >= $2
		  AND total_topup_eur_cents >= $2
		  AND topup_count > 0
	`, tenantID, amountEurCents)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows != 1 {
		return false, fmt.Errorf("x402 rollup is missing or inconsistent")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE purser.x402_nonces
		SET rollup_reversed_at = NOW()
		WHERE id = $1
	`, nonceID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// Helper functions

func (h *X402Handler) checkNonceUsed(ctx context.Context, network NetworkConfig, owner, nonce string) (bool, error) {
	// Call USDC contract to check nonce status
	// authorizationState(address authorizer, bytes32 nonce) returns (bool)
	methodID := keccak256([]byte("authorizationState(address,bytes32)"))[0:4]
	ownerBytes, err := padAddress(owner)
	if err != nil {
		return false, fmt.Errorf("owner: %w", err)
	}
	nonceBytes, err := padBytes32(nonce)
	if err != nil {
		return false, fmt.Errorf("nonce: %w", err)
	}
	callData := methodID
	callData = append(callData, ownerBytes...)
	callData = append(callData, nonceBytes...)

	var result string
	err = h.rpc.Call(ctx, network, "eth_call", []any{
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
	addrBytes, err := padAddress(address)
	if err != nil {
		return nil, fmt.Errorf("address: %w", err)
	}
	callData := append(methodID, addrBytes...)

	var result string
	err = h.rpc.Call(ctx, network, "eth_call", []any{
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
	err := h.rpc.Call(ctx, network, "eth_getTransactionCount", []any{address, "pending"}, &result)
	if err != nil {
		return 0, err
	}
	nonce, _ := new(big.Int).SetString(strings.TrimPrefix(result, "0x"), 16)
	return nonce.Uint64(), nil
}

func (h *X402Handler) getGasPrice(ctx context.Context, network NetworkConfig) (*big.Int, error) {
	var result string
	err := h.rpc.Call(ctx, network, "eth_gasPrice", []any{}, &result)
	if err != nil {
		return nil, err
	}
	gasPrice, _ := new(big.Int).SetString(strings.TrimPrefix(result, "0x"), 16)
	return gasPrice, nil
}

func (h *X402Handler) getPriorityFee(ctx context.Context, network NetworkConfig) (*big.Int, error) {
	var result string
	err := h.rpc.Call(ctx, network, "eth_maxPriorityFeePerGas", []any{}, &result)
	if err != nil {
		return nil, err
	}
	priorityFee, _ := new(big.Int).SetString(strings.TrimPrefix(result, "0x"), 16)
	if priorityFee == nil || priorityFee.Sign() < 0 {
		return nil, fmt.Errorf("invalid priority fee result")
	}
	return priorityFee, nil
}

// simulateTransfer runs eth_call to verify the transfer will succeed before submitting
func (h *X402Handler) simulateTransfer(ctx context.Context, network NetworkConfig, callData []byte) error {
	var result string
	err := h.rpc.Call(ctx, network, "eth_call", []any{
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
	eurCents := int64(math.Round(float64(usdCents) * rate))
	return eurCents, nil
}

// getEurUsdRate is the X402Handler-bound wrapper that preserves the existing
// invocation site signature; new callers should use GetEurUsdRate.
func (h *X402Handler) getEurUsdRate() (float64, error) {
	return GetEurUsdRate(h.logger)
}

// GetEurUsdRate returns the EUR/USD exchange rate (EUR per USD), fetching
// from frankfurter.app (ECB-sourced) when the cache is stale. Falls back to
// stale cache if a fresh fetch fails.
func GetEurUsdRate(logger logging.Logger) (float64, error) {
	ecbRateCache.RLock()
	cachedRate := ecbRateCache.rate
	fetchedAt := ecbRateCache.fetchedAt
	ecbRateCache.RUnlock()

	if time.Since(fetchedAt) < ecbRateCacheTTL && cachedRate > 0 {
		return cachedRate, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.frankfurter.app/latest?from=USD&to=EUR", nil)
	if err != nil {
		if cachedRate > 0 {
			return cachedRate, nil
		}
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if cachedRate > 0 {
			return cachedRate, nil
		}
		return 0, fmt.Errorf("failed to fetch ECB rate: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

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

	ecbRateCache.Lock()
	ecbRateCache.rate = rate
	ecbRateCache.fetchedAt = time.Now()
	ecbRateCache.Unlock()

	if logger != nil {
		logger.WithFields(logging.Fields{"rate": rate}).Debug("Fetched fresh EUR/USD rate from ECB")
	}
	return rate, nil
}

const developmentVATRateSource = "bundled development fallback; not allowed in production"

// This fallback keeps isolated unit/dev stacks useful before migrations are
// applied. Production always requires the effective-dated database catalog.
var developmentVATRates = map[string]int{
	"AT": 2000, // Austria
	"BE": 2100, // Belgium
	"BG": 2000, // Bulgaria
	"HR": 2500, // Croatia
	"CY": 1900, // Cyprus
	"CZ": 2100, // Czech Republic
	"DK": 2500, // Denmark
	"EE": 2400, // Estonia
	"FI": 2550, // Finland
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
	"RO": 2100, // Romania
	"SK": 2300, // Slovakia
	"SI": 2200, // Slovenia
	"ES": 2100, // Spain
	"SE": 2500, // Sweden
}

type vatDecision struct {
	RateBPS       int
	Country       string
	VIESValidated bool
	ReverseCharge bool
	Source        string
	CheckedOn     string
	EffectiveFrom string
}

func (h *X402Handler) loadEffectiveVATRate(ctx context.Context, country string, at time.Time) (vatDecision, error) {
	var decision vatDecision
	decision.Country = country
	err := h.db.QueryRowContext(ctx, `
		SELECT rate_bps, source, source_checked_on::text, effective_from::text
		FROM purser.vat_rate_periods
		WHERE country_code = $1 AND effective_from <= $2::date
		  AND (effective_until IS NULL OR effective_until > $2::date)
		ORDER BY effective_from DESC LIMIT 1
	`, country, at).Scan(&decision.RateBPS, &decision.Source, &decision.CheckedOn, &decision.EffectiveFrom)
	if err == nil {
		return decision, nil
	}
	if !config.IsProduction() {
		if rate, ok := developmentVATRates[country]; ok {
			decision.RateBPS = rate
			decision.Source = developmentVATRateSource
			decision.CheckedOn = "2026-07-13"
			decision.EffectiveFrom = "2026-01-01"
			return decision, nil
		}
	}
	return vatDecision{}, fmt.Errorf("no effective VAT rate for %s at %s", country, at.Format("2006-01-02"))
}

type billingAddress struct {
	Street     string `json:"street"`
	City       string `json:"city"`
	PostalCode string `json:"postal_code"`
	Country    string `json:"country"`
}

func parseBillingAddress(raw []byte) (billingAddress, error) {
	var addr billingAddress
	if err := json.Unmarshal(raw, &addr); err != nil {
		return billingAddress{}, err
	}
	return addr, nil
}

func (h *X402Handler) getVATDecisionForTenant(ctx context.Context, tenantID, clientIP string, at time.Time) (vatDecision, error) {
	// 1. Check tenant's billing details (country and VAT number)
	var taxID sql.NullString
	var billingAddress []byte
	err := h.db.QueryRowContext(ctx, `
		SELECT tax_id, billing_address
		FROM purser.tenant_subscriptions
		WHERE tenant_id = $1 AND status != 'cancelled'
		ORDER BY created_at DESC
		LIMIT 1
	`, tenantID).Scan(&taxID, &billingAddress)

	if err == nil && billingAddress != nil {
		// Parse billing address to get country
		addr, parseErr := parseBillingAddress(billingAddress)
		if parseErr == nil && addr.Country != "" {
			country := countries.Normalize(addr.Country)

			// Reverse charge requires live/cached VIES evidence and a supplier in a
			// different EU member state. Domestic business customers pay domestic VAT.
			if taxID.Valid && h.isValidEUVATFormat(taxID.String) {
				validated, validationErr := h.validateVIESVAT(ctx, tenantID, country, taxID.String)
				if validationErr == nil && validated {
					if reverseChargeEligible(h.supplierCountry, country, true) {
						return vatDecision{Country: country, VIESValidated: true, ReverseCharge: true, Source: "VIES validated cross-border EU reverse charge", CheckedOn: at.Format("2006-01-02"), EffectiveFrom: at.Format("2006-01-02")}, nil
					}
					decision, rateErr := h.loadEffectiveVATRate(ctx, country, at)
					if rateErr != nil {
						return vatDecision{}, rateErr
					}
					decision.VIESValidated = true
					decision.Source += "; VIES validated domestic customer"
					return decision, nil
				}
			}

			// B2C or no valid VAT number
			if _, isEU := developmentVATRates[country]; isEU {
				return h.loadEffectiveVATRate(ctx, country, at)
			}
			// Non-EU billing country = export exempt
			return vatDecision{Country: country, Source: "non-EU customer location", CheckedOn: at.Format("2006-01-02"), EffectiveFrom: at.Format("2006-01-02")}, nil
		}
	}

	// 2. Fall back to GeoIP
	country := h.getCountryFromIP(clientIP)
	if _, isEU := developmentVATRates[country]; isEU {
		return h.loadEffectiveVATRate(ctx, country, at)
	}

	// 3. Non-EU GeoIP = export exempt
	return vatDecision{Country: country, Source: "non-EU or unavailable customer location", CheckedOn: at.Format("2006-01-02"), EffectiveFrom: at.Format("2006-01-02")}, nil
}

func (h *X402Handler) getVATRateForTenant(ctx context.Context, tenantID, clientIP string) (rateBps int, country string, isB2B bool) {
	decision, err := h.getVATDecisionForTenant(ctx, tenantID, clientIP, time.Now().UTC())
	if err != nil {
		return 0, "", false
	}
	return decision.RateBPS, decision.Country, decision.ReverseCharge
}

func reverseChargeEligible(supplierCountry, customerCountry string, viesValidated bool) bool {
	supplierCountry = countries.Normalize(supplierCountry)
	customerCountry = countries.Normalize(customerCountry)
	_, supplierIsEU := developmentVATRates[supplierCountry]
	_, customerIsEU := developmentVATRates[customerCountry]
	return viesValidated && supplierIsEU && customerIsEU && supplierCountry != customerCountry
}

func (h *X402Handler) getBillingCountry(ctx context.Context, tenantID string) string {
	var raw []byte
	err := h.db.QueryRowContext(ctx, `
		SELECT billing_address FROM purser.tenant_subscriptions
		WHERE tenant_id = $1 AND status != 'cancelled'
		ORDER BY created_at DESC LIMIT 1
	`, tenantID).Scan(&raw)
	if err != nil || len(raw) == 0 {
		return ""
	}
	address, err := parseBillingAddress(raw)
	if err != nil {
		return ""
	}
	return countries.Normalize(address.Country)
}

func isBillingDetailsComplete(billingEmail sql.NullString, billingAddress []byte) bool {
	if !billingEmail.Valid || strings.TrimSpace(billingEmail.String) == "" {
		return false
	}
	if len(billingAddress) == 0 {
		return false
	}
	addr, err := parseBillingAddress(billingAddress)
	if err != nil {
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
	_, isEU := developmentVATRates[countryCode]
	return isEU
}

func (h *X402Handler) getCountryFromIP(clientIP string) string {
	if reader := geoip.GetSharedReader(); reader != nil {
		if geo := reader.Lookup(clientIP); geo != nil && geo.CountryCode != "" {
			return countries.Normalize(geo.CountryCode)
		}
	}
	return ""
}

func (h *X402Handler) signDynamicFeeTransaction(nonce uint64, to string, value *big.Int, gasLimit uint64, maxFeePerGas *big.Int, maxPriorityFeePerGas *big.Int, data []byte, chainId *big.Int) ([]byte, error) {
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
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainId,
		Nonce:     nonce,
		To:        &toAddr,
		Value:     value,
		Gas:       gasLimit,
		GasFeeCap: maxFeePerGas,
		GasTipCap: maxPriorityFeePerGas,
		Data:      data,
	})

	signer := types.NewLondonSigner(chainId)
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

func padAddress(addr string) ([]byte, error) {
	addrBytes, err := hex.DecodeString(strings.TrimPrefix(addr, "0x"))
	if err != nil {
		return nil, fmt.Errorf("invalid address hex %q: %w", addr, err)
	}
	if len(addrBytes) != 20 {
		return nil, fmt.Errorf("address must be 20 bytes, got %d", len(addrBytes))
	}
	padded := make([]byte, 32)
	copy(padded[12:], addrBytes)
	return padded, nil
}

func padUint256(value string) ([]byte, error) {
	v, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return nil, fmt.Errorf("invalid uint256 %q", value)
	}
	if v.Sign() < 0 {
		return nil, fmt.Errorf("uint256 cannot be negative: %q", value)
	}
	if v.BitLen() > 256 {
		return nil, fmt.Errorf("uint256 overflow: %q", value)
	}
	padded := make([]byte, 32)
	v.FillBytes(padded)
	return padded, nil
}

func padUint8(v uint8) []byte {
	padded := make([]byte, 32)
	padded[31] = v
	return padded
}

func padBytes32(value string) ([]byte, error) {
	// nonce is already bytes32 as hex string
	b, err := hex.DecodeString(strings.TrimPrefix(value, "0x"))
	if err != nil {
		return nil, fmt.Errorf("invalid bytes32 hex %q: %w", value, err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("bytes32 must be exactly 32 bytes, got %d", len(b))
	}
	return b, nil
}

func padBytes32Bytes(b []byte) []byte {
	padded := make([]byte, 32)
	copy(padded[32-len(b):], b)
	return padded
}

func parseUint256String(s string) (*big.Int, error) {
	v := new(big.Int)
	_, ok := v.SetString(s, 10)
	if !ok || v.Sign() < 0 || v.BitLen() > 256 {
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
	if !crypto.ValidateSignatureValues(recoveryID, r, s, true) {
		return "", fmt.Errorf("invalid or non-canonical signature values")
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
	X402Version          int                       `json:"x402Version"`
	Scheme               string                    `json:"scheme"`
	Network              string                    `json:"network"`
	Payload              *X402ExactPayload         `json:"payload"`
	Accepted             *X402AcceptedRequirements `json:"accepted,omitempty"`
	CanonicalPayloadJSON []byte                    `json:"-"`
	QuoteID              string                    `json:"-"`
}

type X402AcceptedRequirements struct {
	Scheme            string `json:"scheme"`
	Network           string `json:"network"`
	Asset             string `json:"asset"`
	Amount            string `json:"amount"`
	PayTo             string `json:"payTo"`
	MaxTimeoutSeconds int    `json:"maxTimeoutSeconds"`
	ExtraJSON         []byte `json:"-"`
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
	IsAuthOnly             bool  // Retained for wire compatibility; always false
	RequiresBillingDetails bool
}

// SettleResult contains the result of payment settlement
type SettleResult struct {
	Success         bool
	Error           string
	IsAuthOnly      bool   // Retained for wire compatibility; always false
	PayerAddress    string // Wallet address of payer (from authorization signature)
	TxHash          string
	CreditedCents   int64
	Currency        string
	NewBalanceCents int64
	InvoiceNumber   string
}
