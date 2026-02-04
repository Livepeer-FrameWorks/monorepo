package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"frameworks/pkg/billing"
	decklogclient "frameworks/pkg/clients/decklog"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

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
}

// NewCryptoMonitor creates a new crypto payment monitor
func NewCryptoMonitor(database *sql.DB, log logging.Logger, decklogSvc *decklogclient.BatchedClient) *CryptoMonitor {
	return &CryptoMonitor{
		db:              database,
		logger:          log,
		decklogClient:   decklogSvc,
		stopCh:          make(chan struct{}),
		includeTestnets: os.Getenv("CRYPTO_INCLUDE_TESTNETS") == "true",
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
	rows, err := cm.db.Query(`
		SELECT
			cw.id,
			cw.tenant_id,
			cw.purpose,
			cw.invoice_id,
			cw.expected_amount_cents,
			cw.asset,
			COALESCE(cw.network, 'ethereum') as network,
			cw.wallet_address,
			bi.amount as invoice_amount,
			bi.currency as invoice_currency
		FROM purser.crypto_wallets cw
		LEFT JOIN purser.billing_invoices bi ON cw.invoice_id = bi.id
		WHERE cw.status = 'active'
		  AND cw.expires_at > NOW()
		  AND (
			  -- Invoice wallets: invoice must be pending
			  (cw.purpose = 'invoice' AND bi.status = 'pending')
			  OR
			  -- Prepaid wallets: just need to be active
			  (cw.purpose = 'prepaid')
		  )
	`)

	if err != nil {
		cm.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to fetch active crypto wallets")
		return
	}
	defer rows.Close()

	for rows.Next() {
		var wallet PendingWallet
		var invoiceAmount sql.NullFloat64
		var invoiceCurrency sql.NullString
		var invoiceID sql.NullString
		var expectedAmountCents sql.NullInt64

		err := rows.Scan(
			&wallet.ID,
			&wallet.TenantID,
			&wallet.Purpose,
			&invoiceID,
			&expectedAmountCents,
			&wallet.Asset,
			&wallet.Network,
			&wallet.WalletAddress,
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

		cm.checkWalletForPayments(wallet)
	}
}

// checkWalletForPayments checks a specific wallet address for payments
func (cm *CryptoMonitor) checkWalletForPayments(wallet PendingWallet) {
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

	// Calculate expected amount based on purpose
	var expectedAmount float64
	if wallet.Purpose == "invoice" && wallet.InvoiceAmount != nil {
		expectedAmount = *wallet.InvoiceAmount
	} else if wallet.Purpose == "prepaid" && wallet.ExpectedAmountCents != nil {
		// Convert cents to dollars for comparison
		expectedAmount = float64(*wallet.ExpectedAmountCents) / 100.0
	} else {
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

	if wallet.Purpose == "prepaid" && wallet.Asset != "USDC" {
		cm.logger.WithFields(logging.Fields{
			"wallet_id": wallet.ID,
			"asset":     wallet.Asset,
		}).Error("Unsupported prepaid asset without conversion")
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

	// Check if any transaction matches expected payment
	for _, tx := range transactions {
		isValid, txAmount := cm.isValidPaymentForNetwork(tx, expectedAmount, wallet.Asset, network, wallet.Purpose)
		if isValid {
			cm.confirmPayment(wallet, tx, txAmount)
			return
		}
	}
}

// isValidPaymentForNetwork checks if a transaction is a valid payment for a specific network
func (cm *CryptoMonitor) isValidPaymentForNetwork(tx CryptoTransaction, expectedAmount float64, asset string, network NetworkConfig, purpose string) (bool, float64) {
	txAmount, err := cm.parseTransactionAmount(tx.Value, asset)
	if err != nil {
		cm.logger.WithFields(logging.Fields{
			"error":    err,
			"tx_value": tx.Value,
			"asset":    asset,
		}).Error("Failed to parse transaction amount")
		return false, 0
	}

	var isAmountValid bool
	if purpose == "invoice" {
		minAmount := expectedAmount * 0.999
		isAmountValid = txAmount >= minAmount
	} else {
		// Prepaid topups should also match the requested amount to avoid dust/underpayment
		// permanently consuming a wallet address.
		minAmount := expectedAmount * 0.999
		isAmountValid = txAmount >= minAmount
	}

	// Use network-specific confirmations requirement
	hasEnoughConfirmations := tx.Confirmations >= network.Confirmations

	return isAmountValid && hasEnoughConfirmations, txAmount
}

// confirmPayment processes a confirmed crypto payment.
// For invoice: marks invoice as paid
// For prepaid: credits tenant's prepaid balance
func (cm *CryptoMonitor) confirmPayment(wallet PendingWallet, tx CryptoTransaction, txAmount float64) {
	if tx.Hash != "" {
		var exists bool
		err := cm.db.QueryRow(`
			SELECT EXISTS(
				SELECT 1 FROM purser.crypto_wallets
				WHERE network = $1 AND confirmed_tx_hash = $2
			)
		`, wallet.Network, tx.Hash).Scan(&exists)
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

	dbTx, err := cm.db.Begin()
	if err != nil {
		cm.logger.WithFields(logging.Fields{"error": err}).Error("Failed to begin transaction")
		return
	}
	defer dbTx.Rollback() //nolint:errcheck // rollback is best-effort

	now := time.Now()

	if wallet.Purpose == "invoice" {
		err = cm.confirmInvoicePayment(dbTx, wallet, tx, txAmount, now)
	} else if wallet.Purpose == "prepaid" {
		err = cm.confirmPrepaidTopup(dbTx, wallet, tx, txAmount, now)
	} else {
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

	// Mark wallet as used
	_, err = dbTx.Exec(`
		UPDATE purser.crypto_wallets
		SET status = 'used',
			confirmed_tx_hash = $2,
			actual_amount_received = $3,
			block_number = $4,
			confirmed_at = $5,
			updated_at = NOW()
		WHERE id = $1
	`, wallet.ID, tx.Hash, txAmount, tx.BlockNumber, now)
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
		if err := cm.db.QueryRow(`
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
			})
			emitBillingEvent(eventInvoicePaid, wallet.TenantID, "invoice", *wallet.InvoiceID, &pb.BillingEvent{
				InvoiceId: *wallet.InvoiceID,
				Amount:    amount,
				Currency:  currency,
				Provider:  "crypto",
				Status:    "paid",
			})
		}
	} else if wallet.Purpose == "prepaid" {
		amount := float64(int64(math.Round(txAmount*100))) / 100
		emitBillingEvent(eventTopupCredited, wallet.TenantID, "topup", wallet.ID, &pb.BillingEvent{
			TopupId:  wallet.ID,
			Amount:   amount,
			Currency: billing.DefaultCurrency(),
			Provider: "crypto",
			Status:   "credited",
		})
	}
}

// confirmInvoicePayment marks an invoice as paid
func (cm *CryptoMonitor) confirmInvoicePayment(dbTx *sql.Tx, wallet PendingWallet, tx CryptoTransaction, txAmount float64, now time.Time) error {
	if wallet.InvoiceID == nil {
		return fmt.Errorf("invoice_id is nil for invoice wallet")
	}

	paymentID := uuid.New().String()

	// Create payment record
	_, err := dbTx.Exec(`
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
	_, err = dbTx.Exec(`
		UPDATE purser.billing_invoices
		SET status = 'paid', paid_at = $1, updated_at = NOW()
		WHERE id = $2 AND status IN ('pending', 'overdue')
	`, now, *wallet.InvoiceID)

	if err != nil {
		return fmt.Errorf("failed to update invoice status: %w", err)
	}

	return nil
}

// confirmPrepaidTopup credits a tenant's prepaid balance
func (cm *CryptoMonitor) confirmPrepaidTopup(dbTx *sql.Tx, wallet PendingWallet, tx CryptoTransaction, txAmount float64, now time.Time) error {
	if wallet.ExpectedAmountCents == nil {
		return fmt.Errorf("expected_amount_cents is nil for prepaid wallet")
	}

	if wallet.Asset != "USDC" {
		return fmt.Errorf("unsupported prepaid asset without conversion: %s", wallet.Asset)
	}

	amountCents := int64(math.Round(txAmount * 100))
	if amountCents <= 0 {
		return fmt.Errorf("invalid prepaid amount from transaction: %f", txAmount)
	}
	currency := billing.DefaultCurrency()

	// Get current balance (or 0 if not exists)
	var currentBalance int64
	err := dbTx.QueryRow(`
		SELECT balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2
	`, wallet.TenantID, currency).Scan(&currentBalance)

	if err == sql.ErrNoRows {
		currentBalance = 0
	} else if err != nil {
		return fmt.Errorf("failed to get current balance: %w", err)
	}

	newBalance := currentBalance + amountCents

	// Upsert prepaid balance
	_, err = dbTx.Exec(`
		INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (tenant_id, currency)
		DO UPDATE SET balance_cents = $2, updated_at = NOW()
	`, wallet.TenantID, newBalance, currency)

	if err != nil {
		return fmt.Errorf("failed to update prepaid balance: %w", err)
	}

	// Record transaction in audit trail
	transactionID := uuid.New().String()
	_, err = dbTx.Exec(`
		INSERT INTO purser.balance_transactions (
			id, tenant_id, amount_cents, balance_after_cents,
			transaction_type, description, reference_id, reference_type, created_at
		) VALUES ($1, $2, $3, $4, 'topup', $5, $6, 'crypto_payment', $7)
	`,
		transactionID,
		wallet.TenantID,
		amountCents,
		newBalance,
		fmt.Sprintf("Crypto top-up via %s (%s)", wallet.Asset, tx.Hash[:16]+"..."),
		wallet.ID, // reference to crypto_wallet
		now,
	)

	if err != nil {
		return fmt.Errorf("failed to record balance transaction: %w", err)
	}

	cm.logger.WithFields(logging.Fields{
		"tenant_id":    wallet.TenantID,
		"amount_cents": amountCents,
		"new_balance":  newBalance,
		"asset":        wallet.Asset,
		"tx_hash":      tx.Hash,
	}).Info("Prepaid balance credited")

	return nil
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
	defer resp.Body.Close()

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
	defer resp.Body.Close()

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
