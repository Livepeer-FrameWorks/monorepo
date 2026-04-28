package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"frameworks/pkg/billing"
	decklogclient "frameworks/pkg/clients/decklog"
	"frameworks/pkg/config"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/shopspring/decimal"

	"github.com/google/uuid"
)

// CryptoMonitor manages cryptocurrency payment monitoring for both
// invoice payments and prepaid top-ups. Supports ETH, Base, and Arbitrum networks.
type CryptoMonitor struct {
	db              *sql.DB
	logger          logging.Logger
	decklogClient   *decklogclient.BatchedClient
	stopCh          chan struct{}
	includeTestnets bool
}

// CryptoTransaction represents a blockchain transaction
type CryptoTransaction struct {
	Hash          string    `json:"hash"`
	From          string    `json:"from"`
	To            string    `json:"to"`
	Value         string    `json:"value"`
	Confirmations int       `json:"confirmations"`
	BlockNumber   int64     `json:"block_number"`
	BlockTime     time.Time `json:"block_time"`
}

// PendingWallet represents an active crypto wallet awaiting payment
type PendingWallet struct {
	ID                  string
	TenantID            string
	Purpose             string  // 'invoice' or 'prepaid'
	InvoiceID           *string // set for invoice purpose
	ExpectedAmountCents *int64  // set for prepaid purpose
	Asset               string  // ETH, USDC, LPT
	Network             string  // ethereum, base, arbitrum, base-sepolia, arbitrum-sepolia
	WalletAddress       string
	InvoiceAmount       *float64 // invoice amount in currency (for invoice purpose)
	InvoiceCurrency     *string  // invoice currency (for invoice purpose)

	// Locked quote (prepaid path only) — see DepositQuote.
	ExpectedAmountBaseUnits *big.Int
	QuotedPriceUSD          decimal.Decimal
	QuotedUSDToEURRate      *decimal.Decimal
	QuoteSource             string
	CreditedAmountCurrency  string

	// Detected-but-not-yet-confirmed state.
	Status string // 'pending' or 'confirming'
	TxHash string // populated once a matching tx has been seen
}

// NewCryptoMonitor creates a new crypto payment monitor
func NewCryptoMonitor(database *sql.DB, log logging.Logger, decklogSvc *decklogclient.BatchedClient) *CryptoMonitor {
	return &CryptoMonitor{
		db:              database,
		logger:          log,
		decklogClient:   decklogSvc,
		stopCh:          make(chan struct{}),
		includeTestnets: config.X402IncludeTestnetsEnabled(),
	}
}

// Start begins monitoring crypto payments
func (cm *CryptoMonitor) Start(ctx context.Context) {
	networks := DepositNetworks(cm.includeTestnets)
	cm.logger.WithFields(logging.Fields{
		"network_count":    len(networks),
		"include_testnets": cm.includeTestnets,
	}).Info("Starting crypto payment monitor (multi-chain)")

	// Check if at least one explorer API key is configured
	hasAnyKey := false
	for _, n := range networks {
		if n.GetExplorerAPIKey() != "" {
			hasAnyKey = true
			break
		}
	}
	if !hasAnyKey {
		cm.logger.Warn("No block explorer API keys configured - crypto payment detection disabled")
		return
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			cm.logger.Info("Crypto monitor stopping due to context cancellation")
			return
		case <-cm.stopCh:
			cm.logger.Info("Crypto monitor stopping")
			return
		case <-ticker.C:
			cm.checkPendingPayments(ctx)
		}
	}
}

// Stop stops the crypto payment monitor
func (cm *CryptoMonitor) Stop() {
	close(cm.stopCh)
}

// checkPendingPayments checks all active crypto wallets for payments.
// Handles both invoice payments and prepaid top-ups across all supported networks.
func (cm *CryptoMonitor) checkPendingPayments(ctx context.Context) {
	// Query all active wallets - both invoice and prepaid
	// For invoice: join with billing_invoices to get expected amount
	// For prepaid: use expected_amount_cents directly
	rows, err := cm.db.QueryContext(ctx, `
		SELECT
			cw.id,
			cw.tenant_id,
			cw.purpose,
			cw.invoice_id,
			cw.expected_amount_cents,
			cw.asset,
			cw.network,
			cw.wallet_address,
			cw.status,
			cw.tx_hash,
			cw.expected_amount_base_units,
			cw.quoted_price_usd,
			cw.quoted_usd_to_eur_rate,
			cw.quote_source,
			cw.credited_amount_currency,
			bi.amount as invoice_amount,
			bi.currency as invoice_currency
		FROM purser.crypto_wallets cw
		LEFT JOIN purser.billing_invoices bi ON cw.invoice_id = bi.id
		WHERE cw.status IN ('pending', 'confirming')
		  AND cw.expires_at > NOW()
		  AND (
			  (cw.purpose = 'invoice' AND bi.status = 'pending')
			  OR cw.purpose = 'prepaid'
		  )
	`)

	if err != nil {
		cm.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to fetch active crypto wallets")
		return
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var wallet PendingWallet
		var invoiceAmount sql.NullFloat64
		var invoiceCurrency, invoiceID sql.NullString
		var expectedAmountCents sql.NullInt64
		var txHash, expectedBaseUnitsStr, quotedPriceUSDStr, quotedUSDToEURStr, quoteSource, creditedCurrency sql.NullString

		err := rows.Scan(
			&wallet.ID,
			&wallet.TenantID,
			&wallet.Purpose,
			&invoiceID,
			&expectedAmountCents,
			&wallet.Asset,
			&wallet.Network,
			&wallet.WalletAddress,
			&wallet.Status,
			&txHash,
			&expectedBaseUnitsStr,
			&quotedPriceUSDStr,
			&quotedUSDToEURStr,
			&quoteSource,
			&creditedCurrency,
			&invoiceAmount,
			&invoiceCurrency,
		)
		if err != nil {
			cm.logger.WithFields(logging.Fields{
				"error": err,
			}).Error("Error scanning crypto wallet")
			continue
		}

		if invoiceID.Valid {
			wallet.InvoiceID = &invoiceID.String
		}
		if expectedAmountCents.Valid {
			wallet.ExpectedAmountCents = &expectedAmountCents.Int64
		}
		if invoiceAmount.Valid {
			wallet.InvoiceAmount = &invoiceAmount.Float64
		}
		if invoiceCurrency.Valid {
			wallet.InvoiceCurrency = &invoiceCurrency.String
		}
		if txHash.Valid {
			wallet.TxHash = txHash.String
		}
		if expectedBaseUnitsStr.Valid && expectedBaseUnitsStr.String != "" {
			wallet.ExpectedAmountBaseUnits, _ = new(big.Int).SetString(expectedBaseUnitsStr.String, 10)
		}
		if quotedPriceUSDStr.Valid && quotedPriceUSDStr.String != "" {
			if d, decErr := decimal.NewFromString(quotedPriceUSDStr.String); decErr == nil {
				wallet.QuotedPriceUSD = d
			}
		}
		if quotedUSDToEURStr.Valid && quotedUSDToEURStr.String != "" {
			if d, decErr := decimal.NewFromString(quotedUSDToEURStr.String); decErr == nil {
				wallet.QuotedUSDToEURRate = &d
			}
		}
		if quoteSource.Valid {
			wallet.QuoteSource = quoteSource.String
		}
		if creditedCurrency.Valid {
			wallet.CreditedAmountCurrency = creditedCurrency.String
		}

		cm.checkWalletForPayments(ctx, wallet)
	}
}

// checkWalletForPayments checks a specific wallet address for payments
func (cm *CryptoMonitor) checkWalletForPayments(ctx context.Context, wallet PendingWallet) {
	// Get network config
	network, ok := Networks[wallet.Network]
	if !ok {
		cm.logger.WithFields(logging.Fields{
			"wallet_id": wallet.ID,
			"network":   wallet.Network,
		}).Error("Unknown network for wallet")
		return
	}

	// Check if network is enabled (testnets only if configured)
	if network.IsTestnet && !cm.includeTestnets {
		return // Skip testnet wallets when testnets disabled
	}

	// Compute the on-chain amount we expect to see in the asset's whole-token
	// units (not USD). For invoice and USDC-prepaid the legacy path equates
	// 1 token = 1 USD; for ETH-prepaid we read the locked
	// expected_amount_base_units quote.
	var expectedAmount float64
	switch {
	case wallet.Purpose == "invoice" && wallet.InvoiceAmount != nil:
		expectedAmount = *wallet.InvoiceAmount
	case wallet.Purpose == "prepaid" && wallet.Asset == "USDC" && wallet.ExpectedAmountCents != nil:
		expectedAmount = float64(*wallet.ExpectedAmountCents) / 100.0
	case wallet.Purpose == "prepaid" && wallet.ExpectedAmountBaseUnits != nil:
		td, ok := TokenDecimals(wallet.Asset)
		if !ok {
			cm.logger.WithFields(logging.Fields{"wallet_id": wallet.ID, "asset": wallet.Asset}).Error("Unknown token decimals")
			return
		}
		expectedAmount, _ = decimal.NewFromBigInt(wallet.ExpectedAmountBaseUnits, -td).Float64()
	default:
		cm.logger.WithFields(logging.Fields{
			"wallet_id": wallet.ID,
			"purpose":   wallet.Purpose,
		}).Error("Missing expected amount for wallet")
		return
	}

	if wallet.Purpose == "invoice" && wallet.InvoiceCurrency != nil {
		currency := strings.ToUpper(*wallet.InvoiceCurrency)
		if currency != "USD" {
			// Legacy safety: don't get stuck forever on existing non-USD invoice wallets.
			cm.logger.WithFields(logging.Fields{
				"wallet_id": wallet.ID,
				"currency":  currency,
			}).Warn("Unsupported invoice currency for crypto payment (skipping wallet)")
			return
		}
		if wallet.Asset != "USDC" {
			cm.logger.WithFields(logging.Fields{
				"wallet_id": wallet.ID,
				"asset":     wallet.Asset,
				"currency":  currency,
			}).Error("Unsupported crypto asset for fiat invoice")
			return
		}
	}

	if wallet.Purpose == "prepaid" && wallet.Asset == "LPT" {
		// LPT prepaid stays gated until a non-Chainlink price source is wired
		// (no LPT/USD aggregator exists). Skip silently to avoid log spam.
		return
	}

	cm.logger.WithFields(logging.Fields{
		"wallet_id":       wallet.ID,
		"purpose":         wallet.Purpose,
		"asset":           wallet.Asset,
		"network":         wallet.Network,
		"address":         wallet.WalletAddress,
		"expected_amount": expectedAmount,
	}).Debug("Checking wallet for payments")

	var transactions []CryptoTransaction
	var err error

	// Fetch transactions based on asset type and network
	switch wallet.Asset {
	case "ETH":
		transactions, err = cm.getETHTransactions(ctx, network, wallet.WalletAddress)
	case "USDC":
		transactions, err = cm.getUSDCTransactionsForNetwork(ctx, network, wallet.WalletAddress)
	case "LPT":
		// LPT only exists on Ethereum mainnet
		if network.LPTContract == "" {
			cm.logger.WithFields(logging.Fields{
				"network": wallet.Network,
				"asset":   wallet.Asset,
			}).Debug("LPT not available on this network")
			return
		}
		transactions, err = cm.getERC20TransactionsForNetwork(ctx, network, wallet.WalletAddress, network.LPTContract)
	default:
		cm.logger.WithFields(logging.Fields{
			"asset": wallet.Asset,
		}).Error("Unsupported crypto asset")
		return
	}

	if err != nil {
		cm.logger.WithFields(logging.Fields{
			"error":   err,
			"asset":   wallet.Asset,
			"network": wallet.Network,
			"address": wallet.WalletAddress,
		}).Error("Failed to fetch transactions")
		return
	}

	// Walk the transactions newest-first; first match wins.
	//
	// Three states per match:
	//   - amountSeen=true, confirmed=true  → credit the wallet now
	//   - amountSeen=true, confirmed=false → record `confirming` state so the
	//     UI/agent can show "detected, waiting for N confirmations"
	//   - amountSeen=false                 → keep looking
	for _, tx := range transactions {
		match := cm.evaluatePayment(tx, wallet, expectedAmount, network)
		if !match.amountSeen {
			continue
		}
		if match.confirmed {
			cm.confirmPayment(wallet, tx, match.txBaseUnits, match.txAmount)
		} else {
			cm.markConfirming(wallet, tx)
		}
		return
	}
}

// paymentMatch carries everything the caller needs to act on a tx without
// re-parsing the same value.
type paymentMatch struct {
	amountSeen  bool     // tx amount within tolerance of the wallet's expected amount
	confirmed   bool     // also has the network's required confirmation count
	txBaseUnits *big.Int // exact on-chain amount in token base units
	txAmount    float64  // legacy float (whole tokens) — used by the invoice display path only
}

// evaluatePayment checks whether `tx` is a valid receipt for `wallet` and
// returns whether it's also confirmed enough to credit. Money math operates
// on `*big.Int` base units to avoid 18-decimal float truncation; the float
// `txAmount` is a convenience for the invoice path which historically
// reasoned in whole-token floats.
func (cm *CryptoMonitor) evaluatePayment(tx CryptoTransaction, wallet PendingWallet, expectedAmount float64, network NetworkConfig) paymentMatch {
	baseUnits, err := parseTransactionBaseUnits(tx.Value)
	if err != nil {
		cm.logger.WithFields(logging.Fields{
			"error":    err,
			"tx_value": tx.Value,
			"asset":    wallet.Asset,
		}).Error("Failed to parse transaction base units")
		return paymentMatch{}
	}

	td, ok := TokenDecimals(wallet.Asset)
	if !ok {
		cm.logger.WithFields(logging.Fields{"asset": wallet.Asset}).Error("Unknown token decimals")
		return paymentMatch{}
	}

	// txAmount: whole-token float; lossy at the 18-decimal end but only used
	// for invoice-path display, never for credit math.
	txAmountFloat, _ := decimal.NewFromBigInt(baseUnits, -td).Float64()

	amountSeen := false
	switch {
	case wallet.Purpose == "prepaid" && wallet.ExpectedAmountBaseUnits != nil:
		// Prepaid (any asset): compare on-chain base units against the quoted
		// expected_amount_base_units. The quote already accounts for tenant
		// currency (EUR top-ups are anchored to USD via the locked ECB rate),
		// so the float `expected_amount_cents/100` would be wrong for EUR
		// even when asset == USDC. Asset-specific tolerance: USDC has only 6
		// decimals so 0.1% covers wallet-rounding noise; 18-decimal assets
		// truncate more aggressively in user-wallet UIs, so 0.5%.
		toleranceBP := int64(995) // 0.5%
		if wallet.Asset == "USDC" {
			toleranceBP = 999 // 0.1%
		}
		minBaseUnits := new(big.Int).Mul(wallet.ExpectedAmountBaseUnits, big.NewInt(toleranceBP))
		minBaseUnits.Div(minBaseUnits, big.NewInt(1000))
		amountSeen = baseUnits.Cmp(minBaseUnits) >= 0
	default:
		// Invoice path: legacy 1:1 USD float comparison (USDC invoices only).
		amountSeen = txAmountFloat >= expectedAmount*0.999
	}

	confirmed := tx.Confirmations >= network.Confirmations
	return paymentMatch{
		amountSeen:  amountSeen,
		confirmed:   confirmed,
		txBaseUnits: baseUnits,
		txAmount:    txAmountFloat,
	}
}

// markConfirming records that a matching deposit was seen but doesn't yet
// have enough confirmations. Subsequent monitor ticks update `confirmations`
// and eventually transition the row to `completed` via confirmPayment.
func (cm *CryptoMonitor) markConfirming(wallet PendingWallet, tx CryptoTransaction) {
	ctx := context.Background()

	// Same dedup guard as confirmPayment — a tx already credited against
	// another wallet should not re-mark this one. The unique index on
	// (network, tx_hash) would catch it but a clean error log is friendlier.
	if tx.Hash != "" {
		var exists bool
		err := cm.db.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM purser.crypto_wallets
				WHERE network = $1 AND tx_hash = $2 AND id != $3
			)
		`, wallet.Network, tx.Hash, wallet.ID).Scan(&exists)
		if err != nil {
			cm.logger.WithFields(logging.Fields{"error": err}).Error("Failed to check tx dedup in markConfirming")
			return
		}
		if exists {
			return
		}
	}

	now := time.Now()
	_, err := cm.db.ExecContext(ctx, `
		UPDATE purser.crypto_wallets
		SET status = 'confirming',
		    tx_hash = $2,
		    confirmations = $3,
		    detected_at = COALESCE(detected_at, $4),
		    updated_at = NOW()
		WHERE id = $1
		  AND status IN ('pending', 'confirming')
	`, wallet.ID, tx.Hash, tx.Confirmations, now)
	if err != nil {
		cm.logger.WithFields(logging.Fields{
			"error":     err,
			"wallet_id": wallet.ID,
			"tx_hash":   tx.Hash,
		}).Error("Failed to mark wallet confirming")
		return
	}

	cm.logger.WithFields(logging.Fields{
		"wallet_id":     wallet.ID,
		"tx_hash":       tx.Hash,
		"confirmations": tx.Confirmations,
	}).Debug("Wallet marked confirming")
}

// confirmPayment processes a confirmed crypto payment.
// For invoice: marks invoice as paid (legacy float-USD path; USDC only).
// For prepaid: credits tenant's prepaid balance using the locked quote and
// the on-chain receipt in base units.
func (cm *CryptoMonitor) confirmPayment(wallet PendingWallet, tx CryptoTransaction, txBaseUnits *big.Int, txAmount float64) {
	ctx := context.Background()
	if tx.Hash != "" {
		var exists bool
		err := cm.db.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM purser.crypto_wallets
				WHERE network = $1 AND tx_hash = $2 AND id != $3
			)
		`, wallet.Network, tx.Hash, wallet.ID).Scan(&exists)
		if err != nil {
			cm.logger.WithFields(logging.Fields{"error": err}).Error("Failed to check crypto transaction deduplication")
			return
		}
		if exists {
			cm.logger.WithFields(logging.Fields{
				"wallet_id": wallet.ID,
				"tx_hash":   tx.Hash,
			}).Warn("Duplicate crypto transaction detected")
			return
		}
	}

	cm.logger.WithFields(logging.Fields{
		"wallet_id":     wallet.ID,
		"purpose":       wallet.Purpose,
		"tx_hash":       tx.Hash,
		"confirmations": tx.Confirmations,
	}).Info("Confirming crypto payment")

	dbTx, err := cm.db.BeginTx(ctx, nil)
	if err != nil {
		cm.logger.WithFields(logging.Fields{"error": err}).Error("Failed to begin transaction")
		return
	}
	defer dbTx.Rollback() //nolint:errcheck // rollback is best-effort

	now := time.Now()

	var creditedCents int64
	var creditedCurrency string

	switch wallet.Purpose {
	case "invoice":
		err = cm.confirmInvoicePayment(ctx, dbTx, wallet, tx, txAmount, now)
	case "prepaid":
		creditedCents, creditedCurrency, err = cm.confirmPrepaidTopup(ctx, dbTx, wallet, tx, txBaseUnits, now)
	default:
		err = fmt.Errorf("unknown wallet purpose: %s", wallet.Purpose)
	}

	if err != nil {
		cm.logger.WithFields(logging.Fields{
			"error":     err,
			"wallet_id": wallet.ID,
			"purpose":   wallet.Purpose,
		}).Error("Failed to confirm payment")
		return
	}

	// Persist the exact on-chain receipt in base units; no float round-trip.
	_, err = dbTx.ExecContext(ctx, `
		UPDATE purser.crypto_wallets
		SET status = 'completed',
			tx_hash = $2,
			received_amount_base_units = $3,
			block_number = $4,
			confirmations = $5,
			detected_at = COALESCE(detected_at, $6),
			completed_at = $6,
			updated_at = NOW()
		WHERE id = $1
	`, wallet.ID, tx.Hash, txBaseUnits.String(), tx.BlockNumber, tx.Confirmations, now)
	if err != nil {
		cm.logger.WithFields(logging.Fields{"error": err}).Error("Failed to update wallet status")
		return
	}

	if err = dbTx.Commit(); err != nil {
		cm.logger.WithFields(logging.Fields{"error": err}).Error("Failed to commit payment confirmation")
		return
	}

	cm.logger.WithFields(logging.Fields{
		"wallet_id": wallet.ID,
		"tenant_id": wallet.TenantID,
		"purpose":   wallet.Purpose,
		"tx_hash":   tx.Hash,
	}).Info("Crypto payment confirmed successfully")

	if wallet.Purpose == "invoice" && wallet.InvoiceID != nil {
		var paymentID, currency string
		var amount float64
		if err := cm.db.QueryRowContext(ctx, `
			SELECT id, amount, currency
			FROM purser.billing_payments
			WHERE invoice_id = $1 AND status = 'confirmed'
			ORDER BY created_at DESC
			LIMIT 1
		`, *wallet.InvoiceID).Scan(&paymentID, &amount, &currency); err == nil {
			emitBillingEvent(eventPaymentSucceeded, wallet.TenantID, "payment", paymentID, &pb.BillingEvent{
				PaymentId: paymentID,
				InvoiceId: *wallet.InvoiceID,
				Amount:    amount,
				Currency:  currency,
				Provider:  "crypto",
				Status:    "confirmed",
				Asset:     wallet.Asset,
				TxHash:    tx.Hash,
				Network:   wallet.Network,
			})
			emitBillingEvent(eventInvoicePaid, wallet.TenantID, "invoice", *wallet.InvoiceID, &pb.BillingEvent{
				InvoiceId: *wallet.InvoiceID,
				Amount:    amount,
				Currency:  currency,
				Provider:  "crypto",
				Status:    "paid",
				Asset:     wallet.Asset,
				TxHash:    tx.Hash,
				Network:   wallet.Network,
			})
		}
	} else if wallet.Purpose == "prepaid" {
		emitBillingEvent(eventTopupCredited, wallet.TenantID, "topup", wallet.ID, &pb.BillingEvent{
			TopupId:  wallet.ID,
			Amount:   float64(creditedCents) / 100.0,
			Currency: creditedCurrency,
			Provider: "crypto",
			Status:   "credited",
			Asset:    wallet.Asset,
			TxHash:   tx.Hash,
			Network:  wallet.Network,
		})
	}
}

// confirmInvoicePayment marks an invoice as paid
func (cm *CryptoMonitor) confirmInvoicePayment(ctx context.Context, dbTx *sql.Tx, wallet PendingWallet, tx CryptoTransaction, txAmount float64, now time.Time) error {
	if wallet.InvoiceID == nil {
		return fmt.Errorf("invoice_id is nil for invoice wallet")
	}

	paymentID := uuid.New().String()

	// Create payment record
	_, err := dbTx.ExecContext(ctx, `
		INSERT INTO purser.billing_payments (
			id, invoice_id, method, amount, currency, tx_id, status, confirmed_at, created_at, updated_at,
			actual_tx_amount, asset_type, network, block_number
		)
		SELECT $1, $2,
			   CASE
				   WHEN $6 = 'ETH' THEN 'crypto_eth'
				   WHEN $6 = 'USDC' THEN 'crypto_usdc'
				   WHEN $6 = 'LPT' THEN 'crypto_lpt'
			   END,
			   bi.amount, bi.currency, $3, 'confirmed', $4, NOW(), NOW(),
			   $7, $6, $8, $9
		FROM purser.billing_invoices bi
		WHERE bi.id = $5
	`, paymentID, *wallet.InvoiceID, tx.Hash, now, *wallet.InvoiceID, wallet.Asset, txAmount, wallet.Network, tx.BlockNumber)

	if err != nil {
		return fmt.Errorf("failed to create payment record: %w", err)
	}

	// Mark invoice as paid
	_, err = dbTx.ExecContext(ctx, `
		UPDATE purser.billing_invoices
		SET status = 'paid', paid_at = $1, updated_at = NOW()
		WHERE id = $2 AND status IN ('pending', 'overdue')
	`, now, *wallet.InvoiceID)

	if err != nil {
		return fmt.Errorf("failed to update invoice status: %w", err)
	}

	return nil
}

// confirmPrepaidTopup credits a tenant's prepaid balance and returns the
// amount/currency actually credited so the calling event emitter doesn't
// have to recompute (and can't drift out of sync with this function's math).
//
// All money math operates on `*big.Int` base units and `decimal.Decimal`;
// no float conversions on the credit path.
func (cm *CryptoMonitor) confirmPrepaidTopup(ctx context.Context, dbTx *sql.Tx, wallet PendingWallet, tx CryptoTransaction, txBaseUnits *big.Int, now time.Time) (int64, string, error) {
	if wallet.ExpectedAmountCents == nil {
		return 0, "", fmt.Errorf("expected_amount_cents is nil for prepaid wallet")
	}
	if txBaseUnits == nil || txBaseUnits.Sign() <= 0 {
		return 0, "", fmt.Errorf("invalid tx base units")
	}

	currency := wallet.CreditedAmountCurrency
	if currency == "" {
		currency = billing.DefaultCurrency()
	}

	td, ok := TokenDecimals(wallet.Asset)
	if !ok {
		return 0, "", fmt.Errorf("unknown token decimals for %s", wallet.Asset)
	}

	// usdCents = (received_base_units / 10^decimals) × priceUSD × 100
	// USDC short-circuits with priceUSD=1 (no precision loss either way).
	priceUSD := wallet.QuotedPriceUSD
	if wallet.Asset == "USDC" && priceUSD.IsZero() {
		priceUSD = decimal.NewFromInt(1)
	}
	if priceUSD.IsZero() {
		return 0, "", fmt.Errorf("missing quoted_price_usd for %s prepaid wallet", wallet.Asset)
	}
	usdCentsDec := decimal.NewFromBigInt(txBaseUnits, -int32(td)).
		Mul(priceUSD).
		Mul(decimal.NewFromInt(100))
	usdCents := usdCentsDec.Round(0).IntPart()
	if usdCents <= 0 {
		return 0, "", fmt.Errorf("computed credit cents non-positive: base_units=%s price=%s", txBaseUnits, priceUSD)
	}

	var amountCents int64
	if currency == "EUR" {
		if wallet.QuotedUSDToEURRate == nil {
			return 0, "", fmt.Errorf("EUR-denominated %s top-up missing quoted_usd_to_eur_rate", wallet.Asset)
		}
		amountCents = decimal.NewFromInt(usdCents).Mul(*wallet.QuotedUSDToEURRate).Round(0).IntPart()
	} else {
		amountCents = usdCents
	}
	if amountCents <= 0 {
		return 0, "", fmt.Errorf("invalid credit amount: %d cents", amountCents)
	}

	// Get current balance (or 0 if not exists)
	var currentBalance int64
	err := dbTx.QueryRowContext(ctx, `
		SELECT balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2
	`, wallet.TenantID, currency).Scan(&currentBalance)

	if errors.Is(err, sql.ErrNoRows) {
		currentBalance = 0
	} else if err != nil {
		return 0, "", fmt.Errorf("failed to get current balance: %w", err)
	}

	newBalance := currentBalance + amountCents

	// Upsert prepaid balance
	_, err = dbTx.ExecContext(ctx, `
		INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (tenant_id, currency)
		DO UPDATE SET balance_cents = $2, updated_at = NOW()
	`, wallet.TenantID, newBalance, currency)

	if err != nil {
		return 0, "", fmt.Errorf("failed to update prepaid balance: %w", err)
	}

	// Record transaction in audit trail
	transactionID := uuid.New().String()
	_, err = dbTx.ExecContext(ctx, `
		INSERT INTO purser.balance_transactions (
			id, tenant_id, amount_cents, balance_after_cents,
			transaction_type, description, reference_id, reference_type, created_at
		) VALUES ($1, $2, $3, $4, 'topup', $5, $6, 'crypto_payment', $7)
	`,
		transactionID,
		wallet.TenantID,
		amountCents,
		newBalance,
		fmt.Sprintf("Crypto top-up via %s (%s)", wallet.Asset, tx.Hash),
		wallet.ID, // reference to crypto_wallet
		now,
	)

	if err != nil {
		return 0, "", fmt.Errorf("failed to record balance transaction: %w", err)
	}

	// Persist the credited amount + currency on the wallet so GetCryptoTopup
	// can render it without recomputing from the on-chain receipt.
	_, err = dbTx.ExecContext(ctx, `
		UPDATE purser.crypto_wallets
		SET credited_amount_cents = $2,
		    credited_amount_currency = $3,
		    updated_at = NOW()
		WHERE id = $1
	`, wallet.ID, amountCents, currency)
	if err != nil {
		return 0, "", fmt.Errorf("failed to update credited amount on wallet: %w", err)
	}

	cm.logger.WithFields(logging.Fields{
		"tenant_id":    wallet.TenantID,
		"amount_cents": amountCents,
		"currency":     currency,
		"new_balance":  newBalance,
		"asset":        wallet.Asset,
		"tx_hash":      tx.Hash,
	}).Info("Prepaid balance credited")

	return amountCents, currency, nil
}

// Block explorer API transaction fetching (multi-chain support)

// getETHTransactions fetches native ETH transactions for any supported network
func (cm *CryptoMonitor) getETHTransactions(ctx context.Context, network NetworkConfig, address string) ([]CryptoTransaction, error) {
	apiKey := network.GetExplorerAPIKey()
	if apiKey == "" {
		return nil, fmt.Errorf("%s API key not configured", network.ExplorerAPIEnv)
	}

	url := fmt.Sprintf(
		"%s?module=account&action=txlist&address=%s&startblock=0&endblock=99999999&sort=desc&apikey=%s",
		network.ExplorerAPIURL, address, apiKey,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create ETH explorer request for %s: %w", network.Name, err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch ETH transactions on %s: %w", network.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s API returned status %d", network.DisplayName, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var result struct {
		Status string `json:"status"`
		Result []struct {
			Hash          string `json:"hash"`
			To            string `json:"to"`
			Value         string `json:"value"`
			Confirmations string `json:"confirmations"`
			BlockNumber   string `json:"blockNumber"`
			TimeStamp     string `json:"timeStamp"`
		} `json:"result"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	var transactions []CryptoTransaction
	for _, tx := range result.Result {
		if strings.EqualFold(tx.To, address) && tx.Value != "0" {
			confirmations, _ := strconv.Atoi(tx.Confirmations)
			blockNumber, _ := strconv.ParseInt(tx.BlockNumber, 10, 64)
			timestamp, _ := strconv.ParseInt(tx.TimeStamp, 10, 64)
			transactions = append(transactions, CryptoTransaction{
				Hash:          tx.Hash,
				To:            tx.To,
				Value:         tx.Value,
				Confirmations: confirmations,
				BlockNumber:   blockNumber,
				BlockTime:     time.Unix(timestamp, 0),
			})
		}
	}

	return transactions, nil
}

// getUSDCTransactionsForNetwork fetches USDC token transactions for a specific network
func (cm *CryptoMonitor) getUSDCTransactionsForNetwork(ctx context.Context, network NetworkConfig, address string) ([]CryptoTransaction, error) {
	if network.USDCContract == "" {
		return nil, fmt.Errorf("USDC not available on %s", network.Name)
	}
	return cm.getERC20TransactionsForNetwork(ctx, network, address, network.USDCContract)
}

// getERC20TransactionsForNetwork fetches ERC20 token transactions for a specific network
func (cm *CryptoMonitor) getERC20TransactionsForNetwork(ctx context.Context, network NetworkConfig, address, contractAddress string) ([]CryptoTransaction, error) {
	apiKey := network.GetExplorerAPIKey()
	if apiKey == "" {
		return nil, fmt.Errorf("%s API key not configured", network.ExplorerAPIEnv)
	}

	url := fmt.Sprintf(
		"%s?module=account&action=tokentx&contractaddress=%s&address=%s&page=1&offset=100&sort=desc&apikey=%s",
		network.ExplorerAPIURL, contractAddress, address, apiKey,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s explorer request: %w", network.Name, err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch ERC20 transactions on %s: %w", network.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s API returned status %d", network.DisplayName, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var result struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Result  []struct {
			Hash          string `json:"hash"`
			From          string `json:"from"`
			To            string `json:"to"`
			Value         string `json:"value"`
			Confirmations string `json:"confirmations"`
			BlockNumber   string `json:"blockNumber"`
			TimeStamp     string `json:"timeStamp"`
		} `json:"result"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	var transactions []CryptoTransaction
	for _, tx := range result.Result {
		if strings.EqualFold(tx.To, address) && tx.Value != "0" {
			confirmations, _ := strconv.Atoi(tx.Confirmations)
			blockNumber, _ := strconv.ParseInt(tx.BlockNumber, 10, 64)
			timestamp, _ := strconv.ParseInt(tx.TimeStamp, 10, 64)
			transactions = append(transactions, CryptoTransaction{
				Hash:          tx.Hash,
				From:          tx.From,
				To:            tx.To,
				Value:         tx.Value,
				Confirmations: confirmations,
				BlockNumber:   blockNumber,
				BlockTime:     time.Unix(timestamp, 0),
			})
		}
	}

	return transactions, nil
}

// Amount parsing

func (cm *CryptoMonitor) parseTransactionAmount(value string, asset string) (float64, error) {
	switch asset {
	case "ETH":
		return cm.parseEthereumAmount(value)
	case "USDC":
		return cm.parseTokenAmount(value, "USDC")
	case "LPT":
		return cm.parseTokenAmount(value, "LPT")
	default:
		return 0, fmt.Errorf("unknown asset: %s", asset)
	}
}

// parseTransactionBaseUnits decodes the on-chain `value` field (always base
// units encoded as a decimal string from Etherscan/Arbiscan) into an exact
// *big.Int. Use this for any monetary comparison or persistence; the float
// helpers above are reserved for legacy invoice display.
func parseTransactionBaseUnits(value string) (*big.Int, error) {
	n := new(big.Int)
	if _, ok := n.SetString(value, 10); !ok {
		return nil, fmt.Errorf("invalid base-units value: %s", value)
	}
	return n, nil
}

func (cm *CryptoMonitor) parseEthereumAmount(value string) (float64, error) {
	wei := new(big.Int)
	wei, ok := wei.SetString(value, 10)
	if !ok {
		return 0, fmt.Errorf("invalid wei value: %s", value)
	}

	// 1 ETH = 10^18 wei
	ethFloat := new(big.Float).SetInt(wei)
	divisor := new(big.Float).SetFloat64(1e18)
	ethFloat.Quo(ethFloat, divisor)

	result, _ := ethFloat.Float64()
	return result, nil
}

func (cm *CryptoMonitor) parseTokenAmount(value string, asset string) (float64, error) {
	tokenValue := new(big.Int)
	tokenValue, ok := tokenValue.SetString(value, 10)
	if !ok {
		return 0, fmt.Errorf("invalid token value: %s", value)
	}

	var decimals int
	switch asset {
	case "USDC":
		decimals = 6
	case "LPT":
		decimals = 18
	default:
		return 0, fmt.Errorf("unknown token: %s", asset)
	}

	tokenFloat := new(big.Float).SetInt(tokenValue)
	divisor := new(big.Float).SetFloat64(math.Pow(10, float64(decimals)))
	tokenFloat.Quo(tokenFloat, divisor)

	result, _ := tokenFloat.Float64()
	return result, nil
}
