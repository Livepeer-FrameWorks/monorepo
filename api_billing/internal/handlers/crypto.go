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

	"frameworks/api_billing/internal/operator"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/shopspring/decimal"

	"github.com/google/uuid"
)

// CryptoMonitor manages cryptocurrency payment monitoring for both
// invoice payments and prepaid top-ups. Supports ETH, Base, and Arbitrum networks.
type CryptoMonitor struct {
	db              *sql.DB
	logger          logging.Logger
	decklogClient   *decklogclient.BatchedClient
	priceFeed       *PriceFeed
	stopCh          chan struct{}
	includeTestnets bool
	rpc             *RPCClient
	metrics         *PurserMetrics
	taxInvoices     *X402Handler
}

// CryptoTransaction represents a blockchain transaction
type CryptoTransaction struct {
	EventID       string    `json:"-"`
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

	// Locked quote — see DepositQuote.
	ExpectedAmountBaseUnits *big.Int
	QuotedPriceUSD          decimal.Decimal
	QuotedUSDToEURRate      *decimal.Decimal
	QuoteSource             string
	CreditedAmountCurrency  string
	ClientIP                string

	// Detected-but-not-yet-confirmed state.
	Status    string // 'pending' or 'confirming'
	TxHash    string // populated once a matching tx has been seen
	ExpiresAt time.Time
}

// NewCryptoMonitor creates a new crypto payment monitor
func NewCryptoMonitor(database *sql.DB, log logging.Logger, decklogSvc *decklogclient.BatchedClient) *CryptoMonitor {
	return NewCryptoMonitorWithMetrics(database, log, decklogSvc, nil)
}

func NewCryptoMonitorWithMetrics(database *sql.DB, log logging.Logger, decklogSvc *decklogclient.BatchedClient, metrics *PurserMetrics) *CryptoMonitor {
	rpc := NewRPCClient()
	return &CryptoMonitor{
		db:              database,
		logger:          log,
		decklogClient:   decklogSvc,
		priceFeed:       NewPriceFeed(rpc, log),
		stopCh:          make(chan struct{}),
		includeTestnets: config.X402IncludeTestnetsEnabled(),
		rpc:             rpc,
		metrics:         metrics,
		taxInvoices:     NewX402Handler(database, log, NewHDWallet(database, log), rpc, nil),
	}
}

// Start begins monitoring crypto payments
func (cm *CryptoMonitor) Start(ctx context.Context) {
	networks := DepositNetworks(cm.includeTestnets)
	cm.logger.WithFields(logging.Fields{
		"network_count":    len(networks),
		"include_testnets": cm.includeTestnets,
	}).Info("Starting crypto payment monitor (multi-chain)")

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	cm.scanRPCDeposits(ctx)

	for {
		select {
		case <-ctx.Done():
			cm.logger.Info("Crypto monitor stopping due to context cancellation")
			return
		case <-cm.stopCh:
			cm.logger.Info("Crypto monitor stopping")
			return
		case <-ticker.C:
			cm.scanRPCDeposits(ctx)
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
	// Query all unsettled wallets - both invoice and prepaid. Quote expiry ends
	// the promised conversion, not our custody responsibility: an address
	// remains observable so late funds cannot silently disappear.
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
			cw.client_ip,
			cw.expires_at,
			bi.amount as invoice_amount,
			bi.currency as invoice_currency
		FROM purser.crypto_wallets cw
		LEFT JOIN purser.billing_invoices bi ON cw.invoice_id = bi.id AND bi.tenant_id = cw.tenant_id
		WHERE cw.status IN ('pending', 'confirming')
		  AND (
			  (cw.purpose = 'invoice' AND bi.status IN ('pending', 'overdue'))
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
		var txHash, expectedBaseUnitsStr, quotedPriceUSDStr, quotedUSDToEURStr, quoteSource, creditedCurrency, clientIP sql.NullString

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
			&clientIP,
			&wallet.ExpiresAt,
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
		if clientIP.Valid {
			wallet.ClientIP = clientIP.String
		}

		cm.checkWalletForPayments(ctx, wallet)
	}
	if err := rows.Err(); err != nil {
		cm.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed while iterating crypto wallets")
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
	// units. New invoice and prepaid wallets persist expected_amount_base_units;
	// rows without a quote are ignored so they cannot settle against stale
	// currency assumptions.
	var expectedAmount float64
	switch {
	case wallet.ExpectedAmountBaseUnits != nil:
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
			if wallet.Purpose == "invoice" && wallet.ExpectedAmountBaseUnits != nil && match.txBaseUnits.Cmp(wallet.ExpectedAmountBaseUnits) < 0 {
				reason := "invoice payment is below the exact quote"
				cm.markDepositForReview(wallet, tx, match.txBaseUnits, reason)
				return
			}
			if time.Now().After(wallet.ExpiresAt) {
				if wallet.Purpose == "invoice" {
					cm.markDepositForReview(wallet, tx, match.txBaseUnits, "invoice payment arrived after quote expiry")
					return
				}
				if err := cm.refreshLatePrepaidValuation(ctx, &wallet, network); err != nil {
					cm.logger.WithError(err).WithField("wallet_id", wallet.ID).Error("Late crypto top-up requires valuation review")
					cm.markDepositForReview(wallet, tx, match.txBaseUnits, "late top-up valuation unavailable")
					return
				}
			}
			cm.confirmPayment(wallet, tx, match.txBaseUnits, match.txAmount)
		} else {
			cm.markConfirming(wallet, tx)
		}
		return
	}
}

func (cm *CryptoMonitor) refreshLatePrepaidValuation(ctx context.Context, wallet *PendingWallet, network NetworkConfig) error {
	price, err := cm.priceFeed.GetAssetUSDPrice(ctx, network, wallet.Asset)
	if err != nil {
		return err
	}
	wallet.QuotedPriceUSD = price.PriceUSD
	wallet.QuoteSource = price.Source + ":late_receipt"
	if strings.EqualFold(wallet.CreditedAmountCurrency, "EUR") {
		rate, rateErr := GetEurUsdRate(cm.logger)
		if rateErr != nil {
			return rateErr
		}
		rateDecimal := decimal.NewFromFloat(rate)
		wallet.QuotedUSDToEURRate = &rateDecimal
	}
	return nil
}

func (cm *CryptoMonitor) markDepositForReview(wallet PendingWallet, tx CryptoTransaction, baseUnits *big.Int, reason string) {
	if baseUnits == nil {
		return
	}
	result, err := cm.db.ExecContext(context.Background(), `
		UPDATE purser.crypto_wallets
		SET status = 'review_required',
			tx_hash = $2,
			received_amount_base_units = $3,
			block_number = $4,
			confirmations = $5,
			detected_at = COALESCE(detected_at, NOW()),
			updated_at = NOW()
		WHERE id = $1 AND tenant_id = $6 AND status IN ('pending', 'confirming')
	`, wallet.ID, tx.Hash, baseUnits.String(), tx.BlockNumber, tx.Confirmations, wallet.TenantID)
	if err != nil {
		cm.logger.WithError(err).WithField("wallet_id", wallet.ID).Error("Failed to retain crypto deposit for review")
		return
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		cm.logger.WithFields(logging.Fields{"wallet_id": wallet.ID, "tenant_id": wallet.TenantID}).Error("Crypto review update matched no tenant-scoped row")
		return
	}
	cm.logger.WithFields(logging.Fields{
		"wallet_id": wallet.ID,
		"tenant_id": wallet.TenantID,
		"tx_hash":   tx.Hash,
		"reason":    reason,
	}).Warn("Crypto deposit requires operator review")
}

// paymentMatch carries everything the caller needs to act on a tx without
// re-parsing the same value.
type paymentMatch struct {
	amountSeen  bool     // tx amount within tolerance of the wallet's expected amount
	confirmed   bool     // also has the network's required confirmation count
	txBaseUnits *big.Int // exact on-chain amount in token base units
	txAmount    float64  // whole-token display value persisted on invoice payment rows
}

// evaluatePayment checks whether `tx` is a valid receipt for `wallet` and
// returns whether it's also confirmed enough to credit. Money math operates
// on `*big.Int` base units to avoid 18-decimal float truncation; the float
// `txAmount` is only a display/audit value persisted on invoice payment rows.
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
	case wallet.ExpectedAmountBaseUnits != nil && wallet.Purpose == "invoice":
		// Every positive invoice deposit is observed. Only an exact quote match
		// can settle automatically; partial and excess payments are retained for
		// explicit allocation instead of silently marking the invoice paid.
		amountSeen = baseUnits.Sign() > 0
	case wallet.ExpectedAmountBaseUnits != nil:
		// Prepaid quotes require at least the requested amount, then credit the
		// actual confirmed receipt so an overpayment is never discarded.
		amountSeen = baseUnits.Cmp(wallet.ExpectedAmountBaseUnits) >= 0
	default:
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

// markConfirming preserves explorer-compatibility observations that have not
// reached their network confirmation threshold. The JSON-RPC scanner records
// only finalized events, but the compatibility monitor still calls this path.
//
//nolint:unused // Used by the explorer compatibility monitor.
func (cm *CryptoMonitor) markConfirming(wallet PendingWallet, tx CryptoTransaction) {
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
			cm.logger.WithFields(logging.Fields{"error": err}).Error("Failed to check tx dedup in markConfirming")
			return
		}
		if exists {
			return
		}
	}

	now := time.Now()
	result, err := cm.db.ExecContext(ctx, `
		UPDATE purser.crypto_wallets
		SET status = 'confirming', tx_hash = $2, confirmations = $3,
		    detected_at = COALESCE(detected_at, $4), updated_at = NOW()
		WHERE id = $1 AND tenant_id = $5 AND status IN ('pending', 'confirming')
	`, wallet.ID, tx.Hash, tx.Confirmations, now, wallet.TenantID)
	if err != nil {
		cm.logger.WithFields(logging.Fields{"error": err, "wallet_id": wallet.ID, "tx_hash": tx.Hash}).Error("Failed to mark wallet confirming")
		return
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil || rowsAffected == 0 {
		cm.logger.WithFields(logging.Fields{"error": err, "wallet_id": wallet.ID, "tenant_id": wallet.TenantID}).Warn("Wallet confirming update did not converge")
		return
	}
	cm.logger.WithFields(logging.Fields{"wallet_id": wallet.ID, "tx_hash": tx.Hash, "confirmations": tx.Confirmations}).Debug("Wallet marked confirming")
}

// confirmPayment processes a confirmed crypto payment.
// Invoice and prepaid wallets both validate the on-chain receipt against the
// locked base-unit quote persisted when the address was created; prepaid
// wallets then credit the tenant balance using the same locked quote.
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
	var overpaymentCents int64
	var overpaymentCurrency string
	var invoicePayment *confirmedInvoicePayment

	switch wallet.Purpose {
	case "invoice":
		invoicePayment, err = cm.confirmInvoicePayment(ctx, dbTx, wallet, tx, txAmount, now)
		if err == nil {
			overpaymentCents, overpaymentCurrency, err = cm.creditInvoiceOverpaymentTx(ctx, dbTx, wallet, txBaseUnits, now)
		}
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
	result, err := dbTx.ExecContext(ctx, `
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
		  AND tenant_id = $7
	`, wallet.ID, tx.Hash, txBaseUnits.String(), tx.BlockNumber, tx.Confirmations, now, wallet.TenantID)
	if err != nil {
		cm.logger.WithFields(logging.Fields{"error": err}).Error("Failed to update wallet status")
		return
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		cm.logger.WithFields(logging.Fields{"error": err}).Error("Failed to read wallet completion update result")
		return
	}
	if rowsAffected == 0 {
		cm.logger.WithFields(logging.Fields{
			"wallet_id": wallet.ID,
			"tenant_id": wallet.TenantID,
		}).Error("Wallet completion update matched no tenant-scoped row")
		return
	}
	if tx.EventID != "" {
		eventResult, eventErr := dbTx.ExecContext(ctx, `
			UPDATE purser.crypto_deposit_events
			SET status = 'allocated', wallet_id = $2, allocated_at = NOW(), allocation_error = NULL
			WHERE id = $1 AND canonical AND status = 'confirmed'
		`, tx.EventID, wallet.ID)
		if eventErr != nil {
			cm.logger.WithError(eventErr).WithField("event_id", tx.EventID).Error("Failed to allocate crypto deposit event")
			return
		}
		eventRows, eventErr := eventResult.RowsAffected()
		if eventErr != nil || eventRows != 1 {
			cm.logger.WithField("event_id", tx.EventID).Error("Crypto deposit event was already allocated")
			return
		}
	}

	if err = dbTx.Commit(); err != nil {
		cm.logger.WithFields(logging.Fields{"error": err}).Error("Failed to commit payment confirmation")
		return
	}
	if wallet.Purpose == "prepaid" && cm.taxInvoices != nil {
		amountEurCents, conversionErr := cryptoTopupAmountEurCents(wallet, creditedCents, creditedCurrency)
		if conversionErr != nil {
			recordCryptoAccountingAnomaly(ctx, cm.db, cm.logger, wallet.TenantID,
				"tax_document_missing", wallet.Network, "crypto_payment", tx.Hash,
				creditedCents, conversionErr.Error(), map[string]any{"wallet_id": wallet.ID})
			cm.logger.WithError(conversionErr).WithField("wallet_id", wallet.ID).Error("Failed to determine direct crypto top-up invoice amount")
		} else if _, invoiceErr := cm.taxInvoices.generateCryptoTopupInvoice(
			ctx, wallet.TenantID, amountEurCents, "crypto_payment", tx.Hash, wallet.ClientIP, wallet.Network,
		); invoiceErr != nil {
			recordCryptoAccountingAnomaly(ctx, cm.db, cm.logger, wallet.TenantID,
				"tax_document_missing", wallet.Network, "crypto_payment", tx.Hash,
				amountEurCents, invoiceErr.Error(), map[string]any{"wallet_id": wallet.ID})
			cm.logger.WithError(invoiceErr).WithField("wallet_id", wallet.ID).Error("Failed to ensure direct crypto top-up tax document")
		} else {
			resolveCryptoAccountingAnomaly(ctx, cm.db, cm.logger,
				"tax_document_missing", "crypto_payment", tx.Hash, "tax document created")
		}
	}

	cm.logger.WithFields(logging.Fields{
		"wallet_id": wallet.ID,
		"tenant_id": wallet.TenantID,
		"purpose":   wallet.Purpose,
		"tx_hash":   tx.Hash,
	}).Info("Crypto payment confirmed successfully")

	if wallet.Purpose == "invoice" && wallet.InvoiceID != nil {
		if invoicePayment != nil {
			emitBillingEvent(cm.db, cm.logger, eventPaymentSucceeded, wallet.TenantID, "payment", invoicePayment.PaymentID, &ipcpb.BillingEvent{
				PaymentId: invoicePayment.PaymentID,
				InvoiceId: *wallet.InvoiceID,
				Amount:    invoicePayment.Amount,
				Currency:  invoicePayment.Currency,
				Provider:  "crypto",
				Status:    "confirmed",
				Asset:     wallet.Asset,
				TxHash:    tx.Hash,
				Network:   wallet.Network,
			})
			emitBillingEvent(cm.db, cm.logger, eventInvoicePaid, wallet.TenantID, "invoice", *wallet.InvoiceID, &ipcpb.BillingEvent{
				InvoiceId: *wallet.InvoiceID,
				Amount:    invoicePayment.Amount,
				Currency:  invoicePayment.Currency,
				Provider:  "crypto",
				Status:    "paid",
				Asset:     wallet.Asset,
				TxHash:    tx.Hash,
				Network:   wallet.Network,
			})
			if overpaymentCents > 0 {
				emitBillingEvent(cm.db, cm.logger, eventTopupCredited, wallet.TenantID, "topup", wallet.ID, &ipcpb.BillingEvent{
					TopupId: wallet.ID, Amount: float64(overpaymentCents) / 100,
					Currency: overpaymentCurrency, Provider: "crypto_overpayment",
					Status: "credited", Asset: wallet.Asset, TxHash: tx.Hash, Network: wallet.Network,
				})
			}
		}
	} else if wallet.Purpose == "prepaid" {
		emitBillingEvent(cm.db, cm.logger, eventTopupCredited, wallet.TenantID, "topup", wallet.ID, &ipcpb.BillingEvent{
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

func cryptoTopupAmountEurCents(wallet PendingWallet, creditedCents int64, creditedCurrency string) (int64, error) {
	switch strings.ToUpper(creditedCurrency) {
	case "EUR":
		return creditedCents, nil
	case "USD":
		if wallet.QuotedUSDToEURRate == nil || wallet.QuotedUSDToEURRate.Sign() <= 0 {
			return 0, fmt.Errorf("USD top-up is missing its locked USD/EUR rate")
		}
		return decimal.NewFromInt(creditedCents).Mul(*wallet.QuotedUSDToEURRate).Round(0).IntPart(), nil
	default:
		return 0, fmt.Errorf("unsupported crypto top-up invoice currency %q", creditedCurrency)
	}
}

func (cm *CryptoMonitor) reconcileCompletedCryptoTopupInvoices(ctx context.Context) {
	if cm.taxInvoices == nil {
		return
	}
	rows, err := cm.db.QueryContext(ctx, `
		SELECT wallet.tenant_id::text, wallet.credited_amount_cents,
		       wallet.credited_amount_currency, wallet.quoted_usd_to_eur_rate::text,
		       wallet.tx_hash, COALESCE(wallet.client_ip, ''), wallet.network
		FROM purser.crypto_wallets wallet
		WHERE wallet.purpose = 'prepaid' AND wallet.status IN ('completed', 'swept')
		  AND wallet.credited_amount_cents > 0 AND wallet.tx_hash IS NOT NULL
		  AND NOT EXISTS (
		      SELECT 1 FROM purser.simplified_invoices invoice
		      WHERE invoice.tenant_id = wallet.tenant_id
		        AND invoice.reference_type = 'crypto_payment'
		        AND invoice.reference_id = wallet.tx_hash
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM purser.crypto_invoices invoice
		      WHERE invoice.tenant_id = wallet.tenant_id
		        AND invoice.reference_type = 'crypto_payment'
		        AND invoice.reference_id = wallet.tx_hash
		  )
		ORDER BY wallet.completed_at
		LIMIT 100
	`)
	if err != nil {
		cm.logger.WithError(err).Warn("Failed to load direct crypto top-ups missing tax documents")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var tenantID, currency, txHash, clientIP, network string
		var creditedCents int64
		var fx sql.NullString
		if err := rows.Scan(&tenantID, &creditedCents, &currency, &fx, &txHash, &clientIP, &network); err != nil {
			cm.logger.WithError(err).Warn("Failed to scan direct crypto top-up invoice candidate")
			continue
		}
		wallet := PendingWallet{TenantID: tenantID, ClientIP: clientIP, Network: network}
		if fx.Valid {
			rate, parseErr := decimal.NewFromString(fx.String)
			if parseErr != nil {
				cm.logger.WithError(parseErr).WithField("tx_hash", txHash).Warn("Invalid locked FX rate for direct crypto top-up invoice")
				continue
			}
			wallet.QuotedUSDToEURRate = &rate
		}
		amountEurCents, conversionErr := cryptoTopupAmountEurCents(wallet, creditedCents, currency)
		if conversionErr != nil {
			recordCryptoAccountingAnomaly(ctx, cm.db, cm.logger, tenantID,
				"tax_document_missing", network, "crypto_payment", txHash,
				creditedCents, conversionErr.Error(), map[string]any{})
			cm.logger.WithError(conversionErr).WithField("tx_hash", txHash).Warn("Failed to convert direct crypto top-up invoice amount")
			continue
		}
		if _, invoiceErr := cm.taxInvoices.generateCryptoTopupInvoice(ctx, tenantID, amountEurCents, "crypto_payment", txHash, clientIP, network); invoiceErr != nil {
			recordCryptoAccountingAnomaly(ctx, cm.db, cm.logger, tenantID,
				"tax_document_missing", network, "crypto_payment", txHash,
				amountEurCents, invoiceErr.Error(), map[string]any{})
			cm.logger.WithError(invoiceErr).WithField("tx_hash", txHash).Warn("Failed to reconcile direct crypto top-up tax document")
		} else {
			resolveCryptoAccountingAnomaly(ctx, cm.db, cm.logger,
				"tax_document_missing", "crypto_payment", txHash, "tax document created")
		}
	}
}

func quotedCryptoValueCents(wallet PendingWallet, baseUnits *big.Int, currency string) (int64, error) {
	if baseUnits == nil || baseUnits.Sign() <= 0 {
		return 0, nil
	}
	decimals, ok := TokenDecimals(wallet.Asset)
	if !ok {
		return 0, fmt.Errorf("unknown token decimals for %s", wallet.Asset)
	}
	priceUSD := wallet.QuotedPriceUSD
	if wallet.Asset == "USDC" && priceUSD.IsZero() {
		priceUSD = decimal.NewFromInt(1)
	}
	if priceUSD.IsZero() {
		return 0, fmt.Errorf("missing quoted price for invoice overpayment")
	}
	value := decimal.NewFromBigInt(baseUnits, -decimals).Mul(priceUSD).Mul(decimal.NewFromInt(100))
	if strings.EqualFold(currency, "EUR") {
		if wallet.QuotedUSDToEURRate == nil {
			return 0, fmt.Errorf("missing quoted USD/EUR rate for invoice overpayment")
		}
		value = value.Mul(*wallet.QuotedUSDToEURRate)
	}
	return value.Round(0).IntPart(), nil
}

func (cm *CryptoMonitor) creditInvoiceOverpaymentTx(ctx context.Context, tx *sql.Tx, wallet PendingWallet, received *big.Int, now time.Time) (int64, string, error) {
	if wallet.ExpectedAmountBaseUnits == nil || received == nil || received.Cmp(wallet.ExpectedAmountBaseUnits) <= 0 {
		return 0, "", nil
	}
	surplus := new(big.Int).Sub(new(big.Int).Set(received), wallet.ExpectedAmountBaseUnits)
	currency := billing.DefaultCurrency()
	if wallet.InvoiceCurrency != nil && strings.TrimSpace(*wallet.InvoiceCurrency) != "" {
		currency = strings.ToUpper(strings.TrimSpace(*wallet.InvoiceCurrency))
	}
	amountCents, err := quotedCryptoValueCents(wallet, surplus, currency)
	if err != nil {
		return 0, "", err
	}
	if amountCents <= 0 {
		return 0, "", fmt.Errorf("invoice overpayment is below the smallest creditable currency unit")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency, updated_at)
		VALUES ($1, 0, $2, NOW()) ON CONFLICT (tenant_id, currency) DO NOTHING
	`, wallet.TenantID, currency); err != nil {
		return 0, "", err
	}
	var balance int64
	if err := tx.QueryRowContext(ctx, `
		SELECT balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2 FOR UPDATE
	`, wallet.TenantID, currency).Scan(&balance); err != nil {
		return 0, "", err
	}
	newBalance := balance + amountCents
	if _, err := tx.ExecContext(ctx, `
		UPDATE purser.prepaid_balances SET balance_cents = $1, updated_at = NOW()
		WHERE tenant_id = $2 AND currency = $3
	`, newBalance, wallet.TenantID, currency); err != nil {
		return 0, "", err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO purser.balance_transactions (
			id, tenant_id, amount_cents, balance_after_cents, transaction_type,
			description, reference_id, reference_type, actor_kind, reason, created_at
		) VALUES ($1,$2,$3,$4,'topup',$5,$6,'crypto_invoice_overpayment','system',$7,$8)
	`, uuid.NewString(), wallet.TenantID, amountCents, newBalance,
		fmt.Sprintf("Crypto invoice overpayment via %s", wallet.Asset), wallet.ID,
		"confirmed receipt exceeded exact invoice quote", now); err != nil {
		return 0, "", err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE purser.crypto_wallets
		SET credited_amount_cents = $2, credited_amount_currency = $3, updated_at = NOW()
		WHERE id = $1 AND tenant_id = $4
	`, wallet.ID, amountCents, currency, wallet.TenantID); err != nil {
		return 0, "", err
	}
	return amountCents, currency, nil
}

type confirmedInvoicePayment struct {
	PaymentID string
	Amount    float64
	Currency  string
}

// confirmInvoicePayment marks the pending invoice payment intent as confirmed.
func (cm *CryptoMonitor) confirmInvoicePayment(ctx context.Context, dbTx *sql.Tx, wallet PendingWallet, tx CryptoTransaction, txAmount float64, now time.Time) (*confirmedInvoicePayment, error) {
	if wallet.InvoiceID == nil {
		return nil, fmt.Errorf("invoice_id is nil for invoice wallet")
	}

	method := ""
	switch wallet.Asset {
	case "ETH":
		method = "crypto_eth"
	case "USDC":
		method = "crypto_usdc"
	default:
		return nil, fmt.Errorf("unsupported invoice asset: %s", wallet.Asset)
	}

	var payment confirmedInvoicePayment
	err := dbTx.QueryRowContext(ctx, `
		UPDATE purser.billing_payments bp
		SET tx_id = $1,
			status = 'confirmed',
			confirmed_at = $2,
			updated_at = NOW(),
			actual_tx_amount = $3,
			asset_type = $4,
			network = $5,
			block_number = $6
		FROM purser.billing_invoices bi
		WHERE bp.invoice_id = bi.id
		  AND bp.invoice_id = $7
		  AND bi.tenant_id = $8
		  AND bp.method = $9
		  AND bp.status = 'pending'
		  AND bp.tx_id = $10
		RETURNING bp.id, bp.amount, bp.currency
	`, tx.Hash, now, txAmount, wallet.Asset, wallet.Network, tx.BlockNumber, *wallet.InvoiceID, wallet.TenantID, method, wallet.WalletAddress).
		Scan(&payment.PaymentID, &payment.Amount, &payment.Currency)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("pending invoice payment not found for wallet %s", wallet.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to confirm payment record: %w", err)
	}

	result, err := dbTx.ExecContext(ctx, `
		UPDATE purser.billing_invoices
		SET status = 'paid', paid_at = $1, updated_at = NOW()
		WHERE id = $2 AND tenant_id = $3 AND status IN ('pending', 'overdue')
	`, now, *wallet.InvoiceID, wallet.TenantID)

	if err != nil {
		return nil, fmt.Errorf("failed to update invoice status: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("failed to read invoice update result: %w", err)
	}
	if rowsAffected == 0 {
		return nil, fmt.Errorf("invoice %s is not payable for tenant %s", *wallet.InvoiceID, wallet.TenantID)
	}
	if err := operator.ComputeAndPersistCredits(ctx, dbTx, *wallet.InvoiceID, "paid"); err != nil {
		return nil, fmt.Errorf("persist operator credits: %w", err)
	}

	return &payment, nil
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

	_, err := dbTx.ExecContext(ctx, `
		INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency, updated_at)
		VALUES ($1, 0, $2, NOW())
		ON CONFLICT (tenant_id, currency) DO NOTHING
	`, wallet.TenantID, currency)
	if err != nil {
		return 0, "", fmt.Errorf("failed to initialize prepaid balance: %w", err)
	}

	// Lock the balance row before computing the new total. Multiple confirmed
	// deposits for the same tenant must add to the current committed balance,
	// never overwrite each other from a stale read.
	var currentBalance int64
	err = dbTx.QueryRowContext(ctx, `
		SELECT balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2
		FOR UPDATE
	`, wallet.TenantID, currency).Scan(&currentBalance)
	if err != nil {
		return 0, "", fmt.Errorf("failed to get current balance: %w", err)
	}

	newBalance := currentBalance + amountCents

	_, err = dbTx.ExecContext(ctx, `
		UPDATE purser.prepaid_balances
		SET balance_cents = $1, updated_at = NOW()
		WHERE tenant_id = $2 AND currency = $3
	`, newBalance, wallet.TenantID, currency)

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
	result, err := dbTx.ExecContext(ctx, `
		UPDATE purser.crypto_wallets
		SET credited_amount_cents = $2,
		    credited_amount_currency = $3,
		    updated_at = NOW()
		WHERE id = $1
		  AND tenant_id = $4
	`, wallet.ID, amountCents, currency, wallet.TenantID)
	if err != nil {
		return 0, "", fmt.Errorf("failed to update credited amount on wallet: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, "", fmt.Errorf("failed to read credited wallet update result: %w", err)
	}
	if rowsAffected == 0 {
		return 0, "", fmt.Errorf("credited wallet update matched no tenant-scoped row")
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
		"%s?chainid=%d&module=account&action=txlist&address=%s&startblock=0&endblock=999999999&sort=desc&apikey=%s",
		network.ExplorerAPIURL, network.ChainID, address, apiKey,
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
		"%s?chainid=%d&module=account&action=tokentx&contractaddress=%s&address=%s&page=1&offset=100&sort=desc&apikey=%s",
		network.ExplorerAPIURL, network.ChainID, contractAddress, address, apiKey,
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
// helpers above are reserved for non-authoritative display values.
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
