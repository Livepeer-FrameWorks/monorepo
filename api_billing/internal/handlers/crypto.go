package handlers

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
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

	"frameworks/pkg/logging"
)

// CryptoMonitor manages cryptocurrency payment monitoring
type CryptoMonitor struct {
	db     *sql.DB
	logger logging.Logger
	stopCh chan struct{}
}

// CryptoTransaction represents a blockchain transaction
type CryptoTransaction struct {
	Hash          string    `json:"hash"`
	From          string    `json:"from"`
	To            string    `json:"to"`
	Value         string    `json:"value"`
	Confirmations int       `json:"confirmations"`
	BlockTime     time.Time `json:"block_time"`
}

// NewCryptoMonitor creates a new crypto payment monitor
func NewCryptoMonitor(database *sql.DB, log logging.Logger) *CryptoMonitor {
	return &CryptoMonitor{
		db:     database,
		logger: log,
		stopCh: make(chan struct{}),
	}
}

// Start begins monitoring crypto payments
func (cm *CryptoMonitor) Start(ctx context.Context) {
	cm.logger.Info("Starting crypto payment monitor")

	ticker := time.NewTicker(30 * time.Second) // Check every 30 seconds
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
			cm.checkPendingPayments()
		}
	}
}

// Stop stops the crypto payment monitor
func (cm *CryptoMonitor) Stop() {
	close(cm.stopCh)
}

// checkPendingPayments checks all active crypto wallets for payments
func (cm *CryptoMonitor) checkPendingPayments() {
	// Get all active crypto wallets with pending payments
	rows, err := cm.db.Query(`
		SELECT cw.id, cw.tenant_id, cw.invoice_id, cw.asset, cw.wallet_address,
			   bi.amount, bi.currency
		FROM purser.crypto_wallets cw
		JOIN purser.billing_invoices bi ON cw.invoice_id = bi.id
		WHERE cw.status = 'active' 
		  AND cw.expires_at > NOW()
		  AND bi.status = 'pending'
	`)

	if err != nil {
		cm.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to fetch active crypto wallets")
		return
	}
	defer rows.Close()

	for rows.Next() {
		var walletID, tenantID, invoiceID, asset, walletAddress, currency string
		var amount float64

		err := rows.Scan(&walletID, &tenantID, &invoiceID, &asset, &walletAddress, &amount, &currency)
		if err != nil {
			cm.logger.WithFields(logging.Fields{
				"error": err,
			}).Error("Error scanning crypto wallet")
			continue
		}

		// Check for payments to this address
		cm.checkWalletForPayments(walletID, tenantID, invoiceID, asset, walletAddress, amount)
	}
}

// checkWalletForPayments checks a specific wallet address for payments
func (cm *CryptoMonitor) checkWalletForPayments(walletID, tenantID, invoiceID, asset, address string, expectedAmount float64) {
	cm.logger.WithFields(logging.Fields{
		"wallet_id":       walletID,
		"asset":           asset,
		"address":         address,
		"expected_amount": expectedAmount,
	}).Info("Checking wallet for payments")

	var transactions []CryptoTransaction
	var err error

	switch asset {
	case "BTC":
		transactions, err = cm.getBitcoinTransactions(address)
	case "ETH":
		transactions, err = cm.getEthereumTransactions(address)
	case "USDC":
		transactions, err = cm.getUSDCTransactions(address)
	case "LPT":
		transactions, err = cm.getLivepeerTransactions(address)
	default:
		cm.logger.WithFields(logging.Fields{
			"asset": asset,
		}).Error("Unsupported crypto asset")
		return
	}

	if err != nil {
		cm.logger.WithFields(logging.Fields{
			"error":   err,
			"asset":   asset,
			"address": address,
		}).Error("Failed to fetch transactions")
		return
	}

	// Check if any transaction matches our expected payment
	for _, tx := range transactions {
		if cm.isValidPayment(tx, expectedAmount, asset) {
			cm.confirmPayment(walletID, tenantID, invoiceID, tx)
			return
		}
	}
}

// isValidPayment checks if a transaction is a valid payment
func (cm *CryptoMonitor) isValidPayment(tx CryptoTransaction, expectedAmount float64, asset string) bool {
	// Parse transaction value based on asset
	var txAmount float64
	var err error

	switch asset {
	case "BTC":
		// Bitcoin amounts are in satoshis, convert to BTC
		txAmount, err = cm.parseBitcoinAmount(tx.Value)
	case "ETH":
		// Ethereum amounts are in wei, convert to ETH
		txAmount, err = cm.parseEthereumAmount(tx.Value)
	case "USDC", "LPT":
		// Token amounts are usually in smallest unit, convert appropriately
		txAmount, err = cm.parseTokenAmount(tx.Value, asset)
	}

	if err != nil {
		cm.logger.WithFields(logging.Fields{
			"error":    err,
			"tx_value": tx.Value,
			"asset":    asset,
		}).Error("Failed to parse transaction amount")
		return false
	}

	// Check if amount matches (allow small variance for fees/rounding)
	variance := 0.01 // 1% variance allowed
	minAmount := expectedAmount * (1 - variance)
	maxAmount := expectedAmount * (1 + variance)

	isAmountValid := txAmount >= minAmount && txAmount <= maxAmount

	// Require minimum confirmations based on asset
	minConfirmations := map[string]int{
		"BTC":  3,  // 3 confirmations for Bitcoin
		"ETH":  12, // 12 confirmations for Ethereum
		"USDC": 12, // 12 confirmations for USDC (ERC-20)
		"LPT":  12, // 12 confirmations for LPT (ERC-20)
	}

	hasEnoughConfirmations := tx.Confirmations >= minConfirmations[asset]

	cm.logger.WithFields(logging.Fields{
		"tx_hash":              tx.Hash,
		"tx_amount":            txAmount,
		"expected_amount":      expectedAmount,
		"confirmations":        tx.Confirmations,
		"min_confirmations":    minConfirmations[asset],
		"amount_valid":         isAmountValid,
		"enough_confirmations": hasEnoughConfirmations,
	}).Info("Validating crypto payment")

	return isAmountValid && hasEnoughConfirmations
}

// confirmPayment marks a crypto payment as confirmed
func (cm *CryptoMonitor) confirmPayment(walletID, tenantID, invoiceID string, tx CryptoTransaction) {
	cm.logger.WithFields(logging.Fields{
		"wallet_id":     walletID,
		"invoice_id":    invoiceID,
		"tx_hash":       tx.Hash,
		"confirmations": tx.Confirmations,
	}).Info("Confirming crypto payment")

	// Start transaction
	dbTx, err := cm.db.Begin()
	if err != nil {
		cm.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to begin transaction for payment confirmation")
		return
	}
	defer dbTx.Rollback()

	// Create payment record
	paymentID := generateEventID()
	now := time.Now()

	_, err = dbTx.Exec(`
		INSERT INTO purser.billing_payments (id, invoice_id, method, amount, currency, tx_id, status, confirmed_at, created_at, updated_at)
		SELECT $1, $2, 
			   CASE 
				   WHEN cw.asset = 'BTC' THEN 'crypto_btc'
				   WHEN cw.asset = 'ETH' THEN 'crypto_eth'
				   WHEN cw.asset = 'USDC' THEN 'crypto_usdc'
				   WHEN cw.asset = 'LPT' THEN 'crypto_lpt'
			   END,
			   bi.amount, bi.currency, $3, 'confirmed', $4, NOW(), NOW()
		FROM purser.crypto_wallets cw
		JOIN purser.billing_invoices bi ON cw.invoice_id = bi.id
		WHERE cw.id = $5
	`, paymentID, invoiceID, tx.Hash, now, walletID)

	if err != nil {
		cm.logger.WithFields(logging.Fields{
			"error":      err,
			"payment_id": paymentID,
			"invoice_id": invoiceID,
		}).Error("Failed to create payment record")
		return
	}

	// Mark invoice as paid
	_, err = dbTx.Exec(`
		UPDATE purser.billing_invoices 
		SET status = 'paid', paid_at = $1, updated_at = NOW()
		WHERE id = $2
	`, now, invoiceID)

	if err != nil {
		cm.logger.WithFields(logging.Fields{
			"error":      err,
			"invoice_id": invoiceID,
		}).Error("Failed to update invoice status")
		return
	}

	// Mark wallet as used
	_, err = dbTx.Exec(`
		UPDATE purser.crypto_wallets 
		SET status = 'used', updated_at = NOW()
		WHERE id = $1
	`, walletID)

	if err != nil {
		cm.logger.WithFields(logging.Fields{
			"error":     err,
			"wallet_id": walletID,
		}).Error("Failed to update wallet status")
		return
	}

	// Commit transaction
	if err = dbTx.Commit(); err != nil {
		cm.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to commit payment confirmation")
		return
	}

	cm.logger.WithFields(logging.Fields{
		"payment_id": paymentID,
		"invoice_id": invoiceID,
		"wallet_id":  walletID,
		"tx_hash":    tx.Hash,
		"tenant_id":  tenantID,
	}).Info("Crypto payment confirmed successfully")
}

// Simplified blockchain transaction fetching - ONE API per network
// Bitcoin: BlockCypher API
// Ethereum/Tokens: Etherscan API

func (cm *CryptoMonitor) getBitcoinTransactions(address string) ([]CryptoTransaction, error) {
	// Use BlockCypher API for Bitcoin transactions - SIMPLE and reliable
	apiKey := os.Getenv("BLOCKCYPHER_API_KEY")
	url := fmt.Sprintf("https://api.blockcypher.com/v1/btc/main/addrs/%s/full", address)
	if apiKey != "" {
		url += "?token=" + apiKey
	}

	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Bitcoin transactions: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("BlockCypher API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %v", err)
	}

	var result struct {
		TxRefs []struct {
			TxHash        string `json:"tx_hash"`
			Value         int64  `json:"value"`
			Confirmations int    `json:"confirmations"`
			Confirmed     string `json:"confirmed"`
		} `json:"txrefs"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %v", err)
	}

	var transactions []CryptoTransaction
	for _, tx := range result.TxRefs {
		if tx.Value > 0 { // Only incoming transactions
			blockTime, _ := time.Parse(time.RFC3339, tx.Confirmed)
			transactions = append(transactions, CryptoTransaction{
				Hash:          tx.TxHash,
				To:            address,
				Value:         fmt.Sprintf("%d", tx.Value), // Satoshis
				Confirmations: tx.Confirmations,
				BlockTime:     blockTime,
			})
		}
	}

	return transactions, nil
}

func (cm *CryptoMonitor) getEthereumTransactions(address string) ([]CryptoTransaction, error) {
	// Use Etherscan API for Ethereum transactions - SIMPLE and reliable
	apiKey := os.Getenv("ETHERSCAN_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("ETHERSCAN_API_KEY not configured")
	}

	url := fmt.Sprintf("https://api.etherscan.io/api?module=account&action=txlist&address=%s&startblock=0&endblock=99999999&sort=desc&apikey=%s", address, apiKey)

	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Ethereum transactions: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Etherscan API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %v", err)
	}

	var result struct {
		Status string `json:"status"`
		Result []struct {
			Hash          string `json:"hash"`
			To            string `json:"to"`
			Value         string `json:"value"`
			Confirmations string `json:"confirmations"`
			TimeStamp     string `json:"timeStamp"`
		} `json:"result"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %v", err)
	}

	var transactions []CryptoTransaction
	for _, tx := range result.Result {
		if tx.To == address && tx.Value != "0" { // Only incoming transactions with value
			confirmations, _ := strconv.Atoi(tx.Confirmations)
			timestamp, _ := strconv.ParseInt(tx.TimeStamp, 10, 64)
			transactions = append(transactions, CryptoTransaction{
				Hash:          tx.Hash,
				To:            tx.To,
				Value:         tx.Value, // Wei
				Confirmations: confirmations,
				BlockTime:     time.Unix(timestamp, 0),
			})
		}
	}

	return transactions, nil
}

func (cm *CryptoMonitor) getUSDCTransactions(address string) ([]CryptoTransaction, error) {
	// Use Etherscan API for ERC-20 token transactions - SIMPLE approach
	apiKey := os.Getenv("ETHERSCAN_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("ETHERSCAN_API_KEY not configured")
	}

	// USDC contract address on Ethereum mainnet
	usdcContract := "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"
	url := fmt.Sprintf("https://api.etherscan.io/api?module=account&action=tokentx&contractaddress=%s&address=%s&page=1&offset=100&sort=desc&apikey=%s", usdcContract, address, apiKey)

	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch USDC transactions: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Etherscan API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %v", err)
	}

	var result struct {
		Status string `json:"status"`
		Result []struct {
			Hash          string `json:"hash"`
			To            string `json:"to"`
			Value         string `json:"value"`
			Confirmations string `json:"confirmations"`
			TimeStamp     string `json:"timeStamp"`
		} `json:"result"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %v", err)
	}

	var transactions []CryptoTransaction
	for _, tx := range result.Result {
		if tx.To == address && tx.Value != "0" { // Only incoming token transfers
			confirmations, _ := strconv.Atoi(tx.Confirmations)
			timestamp, _ := strconv.ParseInt(tx.TimeStamp, 10, 64)
			transactions = append(transactions, CryptoTransaction{
				Hash:          tx.Hash,
				To:            tx.To,
				Value:         tx.Value, // Token units (6 decimals for USDC)
				Confirmations: confirmations,
				BlockTime:     time.Unix(timestamp, 0),
			})
		}
	}

	return transactions, nil
}

func (cm *CryptoMonitor) getLivepeerTransactions(address string) ([]CryptoTransaction, error) {
	// Use Etherscan API for LPT token transactions
	apiKey := os.Getenv("ETHERSCAN_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("ETHERSCAN_API_KEY not configured")
	}

	// LPT contract address on Ethereum mainnet
	lptContract := "0x58b6A8A3302369DAEc383334672404Ee733aB239" // LPT contract

	url := fmt.Sprintf("https://api.etherscan.io/api?module=account&action=tokentx&contractaddress=%s&address=%s&page=1&offset=100&sort=desc&apikey=%s", lptContract, address, apiKey)

	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch LPT transactions: %v", err)
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
			TimeStamp     string `json:"timeStamp"`
		} `json:"result"`
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read LPT response: %v", err)
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse LPT response: %v", err)
	}

	if result.Status != "1" {
		return nil, fmt.Errorf("Etherscan API error: %s", result.Message)
	}

	var transactions []CryptoTransaction
	for _, tx := range result.Result {
		if strings.EqualFold(tx.To, address) {
			confirmations, _ := strconv.Atoi(tx.Confirmations)
			timestamp, _ := strconv.ParseInt(tx.TimeStamp, 10, 64)
			blockTime := time.Unix(timestamp, 0)

			transactions = append(transactions, CryptoTransaction{
				Hash:          tx.Hash,
				From:          tx.From,
				To:            tx.To,
				Value:         tx.Value, // LPT amount (18 decimals)
				Confirmations: confirmations,
				BlockTime:     blockTime,
			})
		}
	}

	cm.logger.WithFields(logging.Fields{
		"address":  address,
		"network":  "ethereum",
		"contract": "livepeer",
		"tx_count": len(transactions),
	}).Info("Fetched Livepeer transactions")

	return transactions, nil
}

// Amount parsing functions

func (cm *CryptoMonitor) parseBitcoinAmount(value string) (float64, error) {
	// Bitcoin amounts in API are in satoshis, convert to BTC
	satoshis, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, err
	}

	// 1 BTC = 100,000,000 satoshis
	btc := float64(satoshis) / 100000000.0
	return btc, nil
}

func (cm *CryptoMonitor) parseEthereumAmount(value string) (float64, error) {
	// Ethereum amounts are in wei, convert to ETH
	// Use big.Int for precision since wei values can be very large
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
	// Parse token amounts based on their decimal precision
	tokenValue := new(big.Int)
	tokenValue, ok := tokenValue.SetString(value, 10)
	if !ok {
		return 0, fmt.Errorf("invalid token value: %s", value)
	}

	var decimals int
	switch asset {
	case "USDC":
		decimals = 6 // USDC has 6 decimal places
	case "LPT":
		decimals = 18 // LPT has 18 decimal places
	default:
		return 0, fmt.Errorf("unknown token: %s", asset)
	}

	// Convert to float with proper decimal precision
	tokenFloat := new(big.Float).SetInt(tokenValue)
	divisor := new(big.Float).SetFloat64(math.Pow(10, float64(decimals)))
	tokenFloat.Quo(tokenFloat, divisor)

	result, _ := tokenFloat.Float64()
	return result, nil
}

// Helper function to generate event IDs
func generateEventID() string {
	return fmt.Sprintf("pay_%d", time.Now().UnixNano())
}

func generateRealCryptoAddress(asset string) (string, error) {
	switch asset {
	case "BTC":
		return generateBitcoinAddress()
	case "ETH", "USDC", "LPT":
		return generateEthereumAddress()
	default:
		return "", fmt.Errorf("unsupported crypto asset: %s", asset)
	}
}

func generateBitcoinAddress() (string, error) {
	// Generate a random 20-byte hash for a Bitcoin address
	// In production, this should use proper Bitcoin wallet generation
	bytes := make([]byte, 20)
	_, err := rand.Read(bytes)
	if err != nil {
		return "", err
	}

	// Create a simple Bitcoin address format (this is simplified)
	// In production, use proper Bitcoin address generation with checksums
	address := "1" + hex.EncodeToString(bytes)[:33] // P2PKH format
	return address, nil
}

func generateEthereumAddress() (string, error) {
	// Generate random 20-byte address for Ethereum/ERC-20 tokens
	bytes := make([]byte, 20)
	_, err := rand.Read(bytes)
	if err != nil {
		return "", err
	}

	// Format as Ethereum address
	address := "0x" + hex.EncodeToString(bytes)
	return address, nil
}
