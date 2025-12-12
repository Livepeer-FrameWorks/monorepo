package handlers

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"frameworks/pkg/config"
	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
	"frameworks/pkg/models"
)

// isClusterMetered checks if a cluster is metered based on the metered clusters list
func isClusterMetered(meteredClusters []string, primaryClusterID string) bool {
	if meteredClusters == nil { // All clusters metered
		return true
	}
	for _, cluster := range meteredClusters {
		if cluster == primaryClusterID {
			return true
		}
	}
	return false
}

// JobManager handles background billing jobs
type JobManager struct {
	db            *sql.DB
	logger        logging.Logger
	emailService  *EmailService
	cryptoMonitor *CryptoMonitor
	kafkaConsumer *kafka.Consumer
	stopCh        chan struct{}
	billingTopic  string
}

// NewJobManager creates a new job manager
func NewJobManager(database *sql.DB, log logging.Logger) *JobManager {
	// Initialize Kafka consumer
	brokers := strings.Split(config.GetEnv("KAFKA_BROKERS", "kafka:9092"), ",")
	clusterID := config.GetEnv("KAFKA_CLUSTER_ID", "local")
	clientID := config.GetEnv("KAFKA_CLIENT_ID", "purser")
	groupID := config.GetEnv("KAFKA_GROUP_ID", "purser-ingest")
	billingTopic := config.GetEnv("BILLING_KAFKA_TOPIC", "billing.usage_reports")
	kLogger := logrus.New() // Adapt logger

	// Consumer group for billing reports
	// Note: We reuse KAFKA_BROKERS but use a unique group ID to avoid collision with analytics consumers
	consumer, err := kafka.NewConsumer(brokers, groupID, clusterID, clientID, kLogger)
	if err != nil {
		log.WithError(err).Error("Failed to create Kafka consumer for billing")
		// Don't fatal here, allow API to start without consumer if needed
	}

	return &JobManager{
		db:            database,
		logger:        log,
		emailService:  NewEmailService(log),
		cryptoMonitor: NewCryptoMonitor(database, log),
		kafkaConsumer: consumer,
		stopCh:        make(chan struct{}),
		billingTopic:  billingTopic,
	}
}

// Start begins all background jobs
func (jm *JobManager) Start(ctx context.Context) {
	jm.logger.Info("Starting billing job manager")

	// Start usage report consumer
	if jm.kafkaConsumer != nil {
		jm.kafkaConsumer.AddHandler(jm.billingTopic, jm.handleUsageReport)
		go func() {
			if err := jm.kafkaConsumer.Start(ctx); err != nil {
				jm.logger.WithError(err).Error("Kafka consumer exited with error")
			}
		}()
	}

	// Start crypto payment monitor
	go jm.cryptoMonitor.Start(ctx)

	// Start invoice generation job
	go jm.runInvoiceGeneration(ctx)

	// Start payment retry job
	go jm.runPaymentRetry(ctx)

	// Start crypto sweep job
	go jm.runCryptoSweep(ctx)

	// Start wallet cleanup job
	go jm.runWalletCleanup(ctx)
}

// Stop stops all background jobs
func (jm *JobManager) Stop() {
	jm.logger.Info("Stopping billing job manager")
	jm.cryptoMonitor.Stop()
	if jm.kafkaConsumer != nil {
		jm.kafkaConsumer.Close()
	}
	close(jm.stopCh)
}

// handleUsageReport consumes billing usage reports from Kafka
func (jm *JobManager) handleUsageReport(ctx context.Context, msg kafka.Message) error {
	var summary models.UsageSummary
	if err := json.Unmarshal(msg.Value, &summary); err != nil {
		jm.logger.WithError(err).Error("Failed to unmarshal usage summary from Kafka")
		return nil // Skip bad message
	}

	if err := jm.processUsageSummary(summary, "kafka"); err != nil {
		jm.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id": summary.TenantID,
			"period":    summary.Period,
		}).Error("Failed to process usage summary from Kafka")
		return err
	}

	// Update invoice draft after processing
	if err := jm.updateInvoiceDraft(summary.TenantID); err != nil {
		jm.logger.WithError(err).WithField("tenant_id", summary.TenantID).Warn("Failed to update invoice draft")
	}

	jm.logger.WithFields(logging.Fields{
		"tenant_id": summary.TenantID,
		"period":    summary.Period,
	}).Debug("Processed usage summary from Kafka")

	return nil
}

// runInvoiceGeneration generates monthly invoices for active tenants
func (jm *JobManager) runInvoiceGeneration(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour) // Run daily
	defer ticker.Stop()

	jm.logger.Info("Starting invoice generation job")

	for {
		select {
		case <-ctx.Done():
			return
		case <-jm.stopCh:
			return
		case <-ticker.C:
			jm.generateMonthlyInvoices()
		}
	}
}

// generateMonthlyInvoices generates invoices for tenants due for billing
func (jm *JobManager) generateMonthlyInvoices() {
	jm.logger.Info("Running monthly invoice generation")

	now := time.Now()
	firstOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	// Only run on the first day of the month
	if now.Day() != 1 {
		return
	}

	// Get all active tenant subscriptions with their tiers
	rows, err := jm.db.Query(`
		SELECT ts.tenant_id, ts.billing_email, ts.tier_id, ts.status,
		       bt.tier_name, bt.display_name, bt.base_price, bt.currency, bt.billing_period,
		       bt.metering_enabled, bt.overage_rates, bt.storage_allocation, bt.bandwidth_allocation,
		       ts.custom_pricing, ts.custom_features, ts.custom_allocations
		FROM purser.tenant_subscriptions ts
		JOIN purser.billing_tiers bt ON ts.tier_id = bt.id
		WHERE ts.status = 'active' 
		  AND bt.is_active = true
		  AND (ts.next_billing_date IS NULL OR ts.next_billing_date <= $1)
	`, now)

	if err != nil {
		jm.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to fetch tenant subscriptions for invoice generation")
		return
	}
	defer rows.Close()

	var invoicesGenerated int
	for rows.Next() {
		var tenantID, billingEmail, tierID, subscriptionStatus string
		var tierName, displayName, currency, billingPeriod string
		var basePrice float64
		var meteringEnabled bool
		var overageRates models.OverageRates
		var storageAllocation, bandwidthAllocation models.AllocationDetails
		var customPricing models.CustomPricing
		var customFeatures models.BillingFeatures
		var customAllocations models.AllocationDetails

		err := rows.Scan(&tenantID, &billingEmail, &tierID, &subscriptionStatus,
			&tierName, &displayName, &basePrice, &currency, &billingPeriod,
			&meteringEnabled, &overageRates, &storageAllocation, &bandwidthAllocation,
			&customPricing, &customFeatures, &customAllocations)
		if err != nil {
			jm.logger.WithFields(logging.Fields{
				"error": err,
			}).Error("Error scanning tenant subscription data")
			continue
		}

		// Check if invoice already exists for this month
		var existingCount int
		err = jm.db.QueryRow(`
			SELECT COUNT(*) FROM purser.billing_invoices
			WHERE tenant_id = $1 AND created_at >= $2
		`, tenantID, firstOfMonth).Scan(&existingCount)

		if err != nil {
			jm.logger.WithFields(logging.Fields{
				"error":     err,
				"tenant_id": tenantID,
			}).Error("Error checking existing invoices")
			continue
		}

		if existingCount > 0 {
			continue // Invoice already exists for this month
		}

		// Get usage data from the new usage_records table
		// Use precise timestamp boundaries instead of billing_month string
		prevMonthStart := firstOfMonth.AddDate(0, -1, 0) // First of previous month
		prevMonthEnd := firstOfMonth                      // First of current month (exclusive)
		usageData := map[string]float64{}

		// Fetch raw records to aggregate correctly (SUM vs MAX)
		usageRows, err := jm.db.Query(`
			SELECT usage_type, usage_value
			FROM purser.usage_records
			WHERE tenant_id = $1
			  AND period_start >= $2
			  AND period_end <= $3
		`, tenantID, prevMonthStart, prevMonthEnd)

		if err != nil {
			jm.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to fetch usage data")
			// Continue with zero usage
		} else {
			defer usageRows.Close()
			for usageRows.Next() {
				var usageType string
				var val float64
				if err := usageRows.Scan(&usageType, &val); err == nil {
					// Apply correct aggregation per type
					switch usageType {
					case "average_storage_gb", "peak_bandwidth_mbps", "max_viewers", "total_streams", "unique_users":
						// State/Peak metrics: Take MAX
						if val > usageData[usageType] {
							usageData[usageType] = val
						}
					default:
						// Flow metrics (hours, gb transfer): SUM
						usageData[usageType] += val
					}
				}
			}
		}

		// Calculate tier-based pricing
		totalAmount := basePrice
		var meteredAmount float64

		// Apply custom pricing if available (for enterprise tiers)
		if customPricing.BasePrice > 0 {
			totalAmount = customPricing.BasePrice
		}

		// Calculate overage charges if metering is enabled
		if meteringEnabled {
			// Billing model: delivered_minutes (viewer × minutes)
			// viewer_hours from usage data, convert to minutes
			viewerMinutes := usageData["viewer_hours"] * 60

			// Determine effective allocations (custom overrides tier defaults)
			effectiveBandwidthAllocation := bandwidthAllocation
			if customAllocations.Limit != nil {
				// Custom allocation override for bandwidth (primary billing metric)
				effectiveBandwidthAllocation = customAllocations
			}

			// Determine effective overage rates (custom pricing overrides tier defaults)
			effectiveOverageRates := overageRates
			if customPricing.OverageRates.Bandwidth.UnitPrice > 0 {
				effectiveOverageRates.Bandwidth = customPricing.OverageRates.Bandwidth
			}
			if customPricing.OverageRates.Storage.UnitPrice > 0 {
				effectiveOverageRates.Storage = customPricing.OverageRates.Storage
			}
			if customPricing.OverageRates.Compute.UnitPrice > 0 {
				effectiveOverageRates.Compute = customPricing.OverageRates.Compute
			}

			// 1. Bandwidth (delivered minutes)
			if effectiveBandwidthAllocation.Limit != nil && viewerMinutes > 0 {
				includedMinutes := *effectiveBandwidthAllocation.Limit
				billable := viewerMinutes - includedMinutes
				if billable > 0 && effectiveOverageRates.Bandwidth.UnitPrice > 0 {
					meteredAmount += billable * effectiveOverageRates.Bandwidth.UnitPrice
				}
			}

			// 2. Storage overage
			storageUsage := usageData["average_storage_gb"]
			if storageUsage == 0 {
				storageUsage = usageData["recording_gb"]
			}
			if storageAllocation.Limit != nil && storageUsage > 0 {
				includedStorage := *storageAllocation.Limit
				billable := storageUsage - includedStorage
				if billable > 0 && effectiveOverageRates.Storage.UnitPrice > 0 {
					meteredAmount += billable * effectiveOverageRates.Storage.UnitPrice
				}
			}

			// 3. Compute overage (GPU hours)
			gpuHours := usageData["gpu_hours"]
			if gpuHours > 0 && effectiveOverageRates.Compute.UnitPrice > 0 {
				// TODO: Get compute allocation limit when compute billing is implemented
				// For now, charge all GPU hours at overage rate
				meteredAmount += gpuHours * effectiveOverageRates.Compute.UnitPrice
			}
		}

		totalAmount += meteredAmount

		// Generate invoice
		invoiceID := uuid.New().String()
		dueDate := now.AddDate(0, 0, 14) // 14 days to pay

		// Determine invoice status
		status := "pending"
		if totalAmount == 0 {
			status = "paid"
		}

		// Create typed usage details
		usageDetails := UsageDetails{
			UsageData:   usageData,
			PeriodStart: prevMonthStart,
			PeriodEnd:   prevMonthEnd,
			TierInfo: TierInfo{
				TierID:          tierID,
				TierName:        tierName,
				DisplayName:     displayName,
				BasePrice:       basePrice,
				MeteringEnabled: meteringEnabled,
			},
		}

		// Marshal usage details
		usageJSON, err := json.Marshal(usageDetails)
		if err != nil {
			jm.logger.WithFields(logging.Fields{
				"error":     err,
				"tenant_id": tenantID,
			}).Error("Failed to marshal usage data")
			continue
		}

		// Store the invoice with usage details
		_, err = jm.db.Exec(`
			INSERT INTO purser.billing_invoices (
				id, tenant_id, amount, currency, status, due_date,
				base_amount, metered_amount,
				usage_details,
				created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), NOW()
			)
		`, invoiceID, tenantID, totalAmount, currency, status, dueDate, totalAmount-meteredAmount, meteredAmount, usageJSON)

		if err != nil {
			jm.logger.WithFields(logging.Fields{
				"error":     err,
				"tenant_id": tenantID,
				"amount":    totalAmount,
			}).Error("Failed to create invoice")
			continue
		}

		// Update subscription next billing date
		nextBillingDate := now.AddDate(0, 1, 0) // Next month
		_, err = jm.db.Exec(`
			UPDATE purser.tenant_subscriptions 
			SET next_billing_date = $1, updated_at = NOW()
			WHERE tenant_id = $2
		`, nextBillingDate, tenantID)

		if err != nil {
			jm.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to update next billing date")
		}

		invoicesGenerated++
		jm.logger.WithFields(logging.Fields{
			"invoice_id":       invoiceID,
			"tenant_id":        tenantID,
			"tier_name":        tierName,
			"base_amount":      totalAmount - meteredAmount,
			"metered_amount":   meteredAmount,
			"total_amount":     totalAmount,
			"currency":         currency,
			"due_date":         dueDate,
			"metering_enabled": meteringEnabled,
		}).Info("Generated monthly invoice")

		// Send invoice created email notification
		if billingEmail != "" {
			// Convert usage data for email template
			emailUsageDetails := make(map[string]interface{})
			for k, v := range usageData {
				emailUsageDetails[k] = v
			}
			// Add unique users from usage details if available (need to fetch from records or summary)
			// For now, usageData has the aggregates

			err = jm.emailService.SendInvoiceCreatedEmail(billingEmail, "", invoiceID, totalAmount, currency, dueDate, emailUsageDetails)
			if err != nil {
				jm.logger.WithError(err).WithFields(logging.Fields{
					"billing_email": billingEmail,
					"invoice_id":    invoiceID,
				}).Error("Failed to send invoice created email")
			}
		}
	}

	jm.logger.WithFields(logging.Fields{
		"invoices_generated": invoicesGenerated,
	}).Info("Monthly invoice generation completed")
}

// runPaymentRetry retries failed payments and sends reminders
func (jm *JobManager) runPaymentRetry(ctx context.Context) {
	ticker := time.NewTicker(4 * time.Hour) // Run every 4 hours
	defer ticker.Stop()

	jm.logger.Info("Starting payment retry job")

	for {
		select {
		case <-ctx.Done():
			return
		case <-jm.stopCh:
			return
		case <-ticker.C:
			jm.retryFailedPayments()
			jm.sendPaymentReminders()
		}
	}
}

// retryFailedPayments retries payments that failed due to temporary issues
func (jm *JobManager) retryFailedPayments() {
	// Mark failed traditional payments for retry (crypto payments don't need retry)
	_, err := jm.db.Exec(`
		UPDATE billing_payments 
		SET status = 'pending', updated_at = NOW()
		WHERE status = 'failed' 
		  AND method IN ('mollie')
		  AND created_at > NOW() - INTERVAL '24 hours'
		  AND updated_at < NOW() - INTERVAL '1 hour'
	`)

	if err != nil {
		jm.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to retry payments")
	} else {
		jm.logger.Info("Marked eligible failed payments for retry")
	}
}

// sendPaymentReminders sends reminders for overdue invoices
func (jm *JobManager) sendPaymentReminders() {
	// Get overdue invoices with tenant subscription information
	rows, err := jm.db.Query(`
		SELECT bi.id, bi.tenant_id, bi.amount, bi.currency, bi.due_date,
		       ts.billing_email
		FROM purser.billing_invoices bi
		JOIN purser.tenant_subscriptions ts ON bi.tenant_id = ts.tenant_id
		WHERE bi.status = 'pending' 
		  AND bi.due_date < NOW()
		  AND bi.due_date > NOW() - INTERVAL '30 days'
		  AND ts.status = 'active'
	`)

	if err != nil {
		jm.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to fetch overdue invoices")
		return
	}
	defer rows.Close()

	var overdueCount int
	for rows.Next() {
		var invoiceID, tenantID, currency, billingEmail string
		var amount float64
		var dueDate time.Time

		err := rows.Scan(&invoiceID, &tenantID, &amount, &currency, &dueDate, &billingEmail)
		if err != nil {
			continue
		}

		overdueCount++
		daysPastDue := int(time.Since(dueDate).Hours() / 24)

		jm.logger.WithFields(logging.Fields{
			"invoice_id":    invoiceID,
			"tenant_id":     tenantID,
			"amount":        amount,
			"currency":      currency,
			"days_past_due": daysPastDue,
		}).Warn("Invoice is overdue - reminder needed")

		// Send overdue reminder email
		if billingEmail != "" {
			err = jm.emailService.SendOverdueReminderEmail(billingEmail, "", invoiceID, amount, currency, daysPastDue)
			if err != nil {
				jm.logger.WithError(err).WithFields(logging.Fields{
					"billing_email": billingEmail,
					"invoice_id":    invoiceID,
				}).Error("Failed to send overdue reminder email")
			}
		}
	}

	if overdueCount > 0 {
		jm.logger.WithFields(logging.Fields{
			"overdue_count": overdueCount,
		}).Info("Processed payment reminders")
	}
}

// runCryptoSweep moves confirmed crypto payments to cold storage
func (jm *JobManager) runCryptoSweep(ctx context.Context) {
	ticker := time.NewTicker(6 * time.Hour) // Run every 6 hours
	defer ticker.Stop()

	jm.logger.Info("Starting crypto sweep job")

	for {
		select {
		case <-ctx.Done():
			return
		case <-jm.stopCh:
			return
		case <-ticker.C:
			jm.sweepCryptoFunds()
		}
	}
}

// sweepCryptoFunds moves crypto from payment wallets to cold storage
func (jm *JobManager) sweepCryptoFunds() {
	// Get used crypto wallets that haven't been swept
	rows, err := jm.db.Query(`
		SELECT cw.id, cw.asset, cw.wallet_address, bp.amount, bp.tx_id
		FROM purser.crypto_wallets cw
		JOIN purser.billing_payments bp ON bp.invoice_id = cw.invoice_id
		WHERE cw.status = 'used' 
		  AND bp.status = 'confirmed'
		  AND bp.method LIKE 'crypto_%'
		  AND bp.confirmed_at < NOW() - INTERVAL '1 hour'
	`)

	if err != nil {
		jm.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to fetch wallets for sweeping")
		return
	}
	defer rows.Close()

	var sweptCount int
	for rows.Next() {
		var walletID, asset, walletAddress, txID string
		var amount float64

		err := rows.Scan(&walletID, &asset, &walletAddress, &amount, &txID)
		if err != nil {
			continue
		}

		// Execute real crypto sweep transaction
		sweepTxID, err := jm.executeCryptoSweep(asset, walletAddress, amount)
		if err != nil {
			jm.logger.WithFields(logging.Fields{
				"error":          err,
				"wallet_id":      walletID,
				"asset":          asset,
				"wallet_address": walletAddress,
			}).Error("Failed to sweep crypto funds")
			continue
		}

		// Mark wallet as swept with transaction ID
		_, err = jm.db.Exec(`
			UPDATE purser.crypto_wallets 
			SET status = 'swept', updated_at = NOW()
			WHERE id = $1
		`, walletID)

		if err == nil {
			sweptCount++
			jm.logger.WithFields(logging.Fields{
				"wallet_id":      walletID,
				"asset":          asset,
				"wallet_address": walletAddress,
				"amount":         amount,
				"source_tx":      txID,
				"sweep_tx":       sweepTxID,
			}).Info("Successfully swept crypto funds to cold storage")
		}
	}

	if sweptCount > 0 {
		jm.logger.WithFields(logging.Fields{
			"swept_count": sweptCount,
		}).Info("Crypto fund sweep completed")
	}
}

func (jm *JobManager) executeCryptoSweep(asset, fromAddress string, amount float64) (string, error) {
	coldStorageAddress := os.Getenv(fmt.Sprintf("%s_COLD_STORAGE_ADDRESS", asset))
	if coldStorageAddress == "" {
		return "", fmt.Errorf("cold storage address not configured for %s", asset)
	}

	switch asset {
	case "BTC":
		return jm.sweepBitcoin(fromAddress, coldStorageAddress, amount)
	case "ETH":
		return jm.sweepEthereum(fromAddress, coldStorageAddress, amount)
	case "USDC":
		return jm.sweepUSDC(fromAddress, coldStorageAddress, amount)
	case "LPT":
		return jm.sweepLivepeer(fromAddress, coldStorageAddress, amount)
	default:
		return "", fmt.Errorf("unsupported asset for sweeping: %s", asset)
	}
}

func (jm *JobManager) sweepBitcoin(fromAddress, toAddress string, amount float64) (string, error) {
	// Get wallet private key using HD derivation from master seed
	privateKey, err := jm.deriveWalletPrivateKey(fromAddress, "BTC")
	if err != nil {
		return "", fmt.Errorf("failed to derive Bitcoin private key: %v", err)
	}

	// Use BlockCypher API to create and sign transaction
	apiKey := os.Getenv("BLOCKCYPHER_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("BLOCKCYPHER_API_KEY not configured")
	}

	// Calculate transaction fee (simplified - use dynamic fee estimation in production)
	feeInSatoshis := int64(10000) // 0.0001 BTC fee
	amountInSatoshis := int64(amount*100000000) - feeInSatoshis

	if amountInSatoshis <= 0 {
		return "", fmt.Errorf("insufficient funds for transaction (amount: %f BTC, fee: 0.0001 BTC)", amount)
	}

	// Create transaction payload for BlockCypher with private key for signing
	payload := BlockCypherTransactionRequest{
		Inputs: []BlockCypherTransactionInput{
			{Addresses: []string{fromAddress}},
		},
		Outputs: []BlockCypherTransactionOutput{
			{
				Addresses: []string{toAddress},
				Value:     amountInSatoshis,
			},
		},
		PrivateKeys: []string{privateKey}, // Include private key for signing
	}

	payloadBytes, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://api.blockcypher.com/v1/btc/main/txs/send?token=%s", apiKey) // Use send endpoint for signed transactions

	// Create HTTP request
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to create Bitcoin transaction: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		return "", fmt.Errorf("BlockCypher API returned status %d", resp.StatusCode)
	}

	var txResponse struct {
		TxHash string `json:"tx_hash"`
		Hash   string `json:"hash"` // BlockCypher uses "hash" field
		Error  string `json:"error"`
	}

	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &txResponse); err != nil {
		return "", fmt.Errorf("failed to parse Bitcoin transaction response: %v", err)
	}

	if txResponse.Error != "" {
		return "", fmt.Errorf("Bitcoin transaction error: %s", txResponse.Error)
	}

	// Use the appropriate hash field from the response
	txHash := txResponse.TxHash
	if txHash == "" {
		txHash = txResponse.Hash
	}

	jm.logger.WithFields(logging.Fields{
		"from_address": fromAddress,
		"to_address":   toAddress,
		"amount_btc":   amount,
		"tx_hash":      txHash,
	}).Info("Bitcoin sweep transaction created successfully")

	return txHash, nil
}

func (jm *JobManager) sweepEthereum(fromAddress, toAddress string, amount float64) (string, error) {
	// Get wallet private key using HD derivation
	privateKeyHex, err := jm.deriveWalletPrivateKey(fromAddress, "ETH")
	if err != nil {
		return "", fmt.Errorf("failed to derive Ethereum private key: %v", err)
	}

	// Parse private key
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return "", fmt.Errorf("failed to parse private key: %v", err)
	}

	// Connect to Ethereum client
	rpcURL := os.Getenv("ETHEREUM_RPC_URL")
	if rpcURL == "" {
		return "", fmt.Errorf("ETHEREUM_RPC_URL not configured")
	}

	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		return "", fmt.Errorf("failed to connect to Ethereum client: %v", err)
	}
	defer client.Close()

	// Get sender address from private key
	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("failed to cast public key to ECDSA")
	}
	fromAddr := crypto.PubkeyToAddress(*publicKeyECDSA)
	toAddr := common.HexToAddress(toAddress)

	// Get nonce
	nonce, err := client.PendingNonceAt(context.Background(), fromAddr)
	if err != nil {
		return "", fmt.Errorf("failed to get nonce: %v", err)
	}

	// Convert amount to wei (1 ETH = 10^18 wei)
	value := new(big.Int)
	weiAmount := new(big.Float).SetFloat64(amount * 1e18)
	weiAmount.Int(value)

	// Get gas price
	gasPrice, err := client.SuggestGasPrice(context.Background())
	if err != nil {
		return "", fmt.Errorf("failed to get gas price: %v", err)
	}

	// Set gas limit for simple ETH transfer
	gasLimit := uint64(21000)

	// Check if we have enough balance for gas
	balance, err := client.BalanceAt(context.Background(), fromAddr, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get balance: %v", err)
	}

	gasCost := new(big.Int).Mul(gasPrice, big.NewInt(int64(gasLimit)))
	totalCost := new(big.Int).Add(value, gasCost)

	if balance.Cmp(totalCost) < 0 {
		return "", fmt.Errorf("insufficient balance: have %s wei, need %s wei", balance.String(), totalCost.String())
	}

	// Create transaction
	tx := types.NewTransaction(nonce, toAddr, value, gasLimit, gasPrice, nil)

	// Get chain ID
	chainID, err := client.NetworkID(context.Background())
	if err != nil {
		return "", fmt.Errorf("failed to get chain ID: %v", err)
	}

	// Sign transaction
	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(chainID), privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign transaction: %v", err)
	}

	// Send transaction
	err = client.SendTransaction(context.Background(), signedTx)
	if err != nil {
		return "", fmt.Errorf("failed to send transaction: %v", err)
	}

	txHash := signedTx.Hash().Hex()

	jm.logger.WithFields(logging.Fields{
		"from_address": fromAddr.Hex(),
		"to_address":   toAddr.Hex(),
		"amount_eth":   amount,
		"amount_wei":   value.String(),
		"gas_price":    gasPrice.String(),
		"gas_limit":    gasLimit,
		"nonce":        nonce,
		"chain_id":     chainID.String(),
		"tx_hash":      txHash,
	}).Info("Ethereum sweep transaction sent successfully")

	return txHash, nil
}

func (jm *JobManager) sweepUSDC(fromAddress, toAddress string, amount float64) (string, error) {
	// USDC uses Ethereum infrastructure with ERC-20 token transfer
	return jm.sweepERC20Token(fromAddress, toAddress, amount, "USDC", "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", 6)
}

func (jm *JobManager) sweepLivepeer(fromAddress, toAddress string, amount float64) (string, error) {
	// LPT uses Ethereum infrastructure with ERC-20 token transfer
	return jm.sweepERC20Token(fromAddress, toAddress, amount, "LPT", "0x58b6A8A3302369DAEc383334672404Ee733aB239", 18)
}

func (jm *JobManager) sweepERC20Token(fromAddress, toAddress string, amount float64, tokenSymbol, contractAddress string, decimals int) (string, error) {
	// Get wallet private key using HD derivation
	privateKeyHex, err := jm.deriveWalletPrivateKey(fromAddress, "ETH")
	if err != nil {
		return "", fmt.Errorf("failed to derive Ethereum private key for %s: %v", tokenSymbol, err)
	}

	// Parse private key
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return "", fmt.Errorf("failed to parse private key: %v", err)
	}

	// Connect to Ethereum client
	rpcURL := os.Getenv("ETHEREUM_RPC_URL")
	if rpcURL == "" {
		return "", fmt.Errorf("ETHEREUM_RPC_URL not configured")
	}

	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		return "", fmt.Errorf("failed to connect to Ethereum client: %v", err)
	}
	defer client.Close()

	// Get sender address from private key
	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("failed to cast public key to ECDSA")
	}
	fromAddr := crypto.PubkeyToAddress(*publicKeyECDSA)
	toAddr := common.HexToAddress(toAddress)
	contractAddr := common.HexToAddress(contractAddress)

	// Get nonce
	nonce, err := client.PendingNonceAt(context.Background(), fromAddr)
	if err != nil {
		return "", fmt.Errorf("failed to get nonce: %v", err)
	}

	// Convert amount to token units (considering decimals)
	tokenAmount := new(big.Int)
	multiplier := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	amountFloat := new(big.Float).SetFloat64(amount)
	amountFloat.Mul(amountFloat, new(big.Float).SetInt(multiplier))
	amountFloat.Int(tokenAmount)

	// Create ERC-20 transfer function call data
	// transfer(address,uint256) = 0xa9059cbb
	transferFnSignature := crypto.Keccak256([]byte("transfer(address,uint256)"))[:4]
	paddedToAddr := common.LeftPadBytes(toAddr.Bytes(), 32)
	paddedAmount := common.LeftPadBytes(tokenAmount.Bytes(), 32)

	data := append(transferFnSignature, paddedToAddr...)
	data = append(data, paddedAmount...)

	// Get gas price
	gasPrice, err := client.SuggestGasPrice(context.Background())
	if err != nil {
		return "", fmt.Errorf("failed to get gas price: %v", err)
	}

	// Set gas limit for ERC-20 transfer (higher than ETH transfer)
	gasLimit := uint64(100000)

	// Check ETH balance for gas fees
	balance, err := client.BalanceAt(context.Background(), fromAddr, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get ETH balance: %v", err)
	}

	gasCost := new(big.Int).Mul(gasPrice, big.NewInt(int64(gasLimit)))
	if balance.Cmp(gasCost) < 0 {
		return "", fmt.Errorf("insufficient ETH balance for gas: have %s wei, need %s wei", balance.String(), gasCost.String())
	}

	// Create transaction (value = 0 for ERC-20 transfers)
	tx := types.NewTransaction(nonce, contractAddr, big.NewInt(0), gasLimit, gasPrice, data)

	// Get chain ID
	chainID, err := client.NetworkID(context.Background())
	if err != nil {
		return "", fmt.Errorf("failed to get chain ID: %v", err)
	}

	// Sign transaction
	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(chainID), privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign transaction: %v", err)
	}

	// Send transaction
	err = client.SendTransaction(context.Background(), signedTx)
	if err != nil {
		return "", fmt.Errorf("failed to send transaction: %v", err)
	}

	txHash := signedTx.Hash().Hex()

	jm.logger.WithFields(logging.Fields{
		"from_address":     fromAddr.Hex(),
		"to_address":       toAddr.Hex(),
		"amount":           amount,
		"token_amount":     tokenAmount.String(),
		"token_symbol":     tokenSymbol,
		"contract_address": contractAddr.Hex(),
		"decimals":         decimals,
		"gas_price":        gasPrice.String(),
		"gas_limit":        gasLimit,
		"nonce":            nonce,
		"chain_id":         chainID.String(),
		"tx_hash":          txHash,
	}).Info("ERC-20 token sweep transaction sent successfully")

	return txHash, nil
}

// deriveWalletPrivateKey derives a private key for a given address and asset using HD wallet derivation
func (jm *JobManager) deriveWalletPrivateKey(address, asset string) (string, error) {
	// Get master seed from secure storage (not env vars!)
	masterSeed := os.Getenv("MASTER_WALLET_SEED")
	if masterSeed == "" {
		return "", fmt.Errorf("MASTER_WALLET_SEED not configured - required for wallet key derivation")
	}

	// Use HMAC-SHA256 for key derivation (simplified HD wallet approach)
	// In production, use proper BIP32/BIP44 HD wallet derivation
	derivationData := fmt.Sprintf("%s:%s:%s", masterSeed, asset, address)
	hash := sha256.Sum256([]byte(derivationData))

	// Return hex-encoded private key
	privateKey := hex.EncodeToString(hash[:])

	jm.logger.WithFields(logging.Fields{
		"address": address,
		"asset":   asset,
		"key_len": len(privateKey),
	}).Debug("Derived wallet private key")

	return privateKey, nil
}

// runWalletCleanup cleans up expired crypto wallets
func (jm *JobManager) runWalletCleanup(ctx context.Context) {
	ticker := time.NewTicker(12 * time.Hour) // Run twice daily
	defer ticker.Stop()

	jm.logger.Info("Starting wallet cleanup job")

	for {
		select {
		case <-ctx.Done():
			return
		case <-jm.stopCh:
			return
		case <-ticker.C:
			jm.cleanupExpiredWallets()
		}
	}
}

// cleanupExpiredWallets marks expired crypto wallets as inactive
func (jm *JobManager) cleanupExpiredWallets() {
	result, err := jm.db.Exec(`
		UPDATE purser.crypto_wallets 
		SET status = 'expired', updated_at = NOW()
		WHERE status = 'active' 
		  AND expires_at < NOW()
	`)

	if err != nil {
		jm.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to cleanup expired wallets")
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		jm.logger.WithFields(logging.Fields{
			"expired_wallets": rowsAffected,
		}).Info("Cleaned up expired crypto wallets")
	}
}

// ============================================================================
// USAGE PROCESSING (Kafka ingestion)
// These methods were moved from usage.go when HTTP handlers were deleted.
// Usage ingestion flows: Periscope -> Kafka -> JobManager.handleUsageReport
// ============================================================================

// processUsageSummary processes a single usage summary and stores it in the usage records table
func (jm *JobManager) processUsageSummary(summary models.UsageSummary, source string) error {
	// Parse the period to get the actual start and end time of usage
	// Format is expected to be "start_time_rfc3339/end_time_rfc3339"
	var billingMonth string
	var periodStart, periodEnd time.Time
	parts := strings.Split(summary.Period, "/")
	if len(parts) >= 2 {
		var err error
		periodStart, err = time.Parse(time.RFC3339, parts[0])
		if err == nil {
			billingMonth = periodStart.Format("2006-01")
		}
		periodEnd, _ = time.Parse(time.RFC3339, parts[1])
	} else if len(parts) >= 1 {
		// Try to parse at least start time
		var err error
		periodStart, err = time.Parse(time.RFC3339, parts[0])
		if err == nil {
			billingMonth = periodStart.Format("2006-01")
			// Default end time to start time + 1 hour if not provided?
			// Better to leave it zero/null or set to start time if we must
			periodEnd = periodStart
		}
	}

	// Fallback if parsing fails
	if billingMonth == "" {
		billingMonth = summary.Timestamp.Format("2006-01")
		// Use timestamp for period start/end fallback to avoid NULL constraint violation if applicable
		// But ideally we want the constraint to work, so we need distinct values if possible.
		// For now, use timestamp.
		periodStart = summary.Timestamp
		periodEnd = summary.Timestamp
		
		jm.logger.WithFields(logging.Fields{
			"tenant_id": summary.TenantID,
			"period":    summary.Period,
		}).Warn("Failed to parse period for billing month/times, falling back to timestamp")
	}

	usageTypes := map[string]float64{
		"stream_hours":            summary.StreamHours,
		"viewer_hours":            summary.ViewerHours,
		"egress_gb":               summary.EgressGB,
		"recording_gb":            summary.RecordingGB,
		"peak_bandwidth_mbps":     summary.PeakBandwidthMbps,
		"storage_gb":              summary.StorageGB,
		"average_storage_gb":      summary.AverageStorageGB,
		"clips_added":             float64(summary.ClipsAdded),
		"clips_deleted":           float64(summary.ClipsDeleted),
		"clip_storage_added_gb":   summary.ClipStorageAddedGB,
		"clip_storage_deleted_gb": summary.ClipStorageDeletedGB,
		"max_viewers":             float64(summary.MaxViewers),
		"total_streams":           float64(summary.TotalStreams),
	}

	// Build usage details JSONB
	usageDetails := models.JSONB{
		"max_viewers":             summary.MaxViewers,
		"total_streams":           summary.TotalStreams,
		"unique_users":            summary.UniqueUsers,
		"avg_viewers":             summary.AvgViewers,
		"unique_countries":        summary.UniqueCountries,
		"unique_cities":           summary.UniqueCities,
		"geo_breakdown":           summary.GeoBreakdown,
		"avg_buffer_health":       summary.AvgBufferHealth,
		"avg_bitrate":             summary.AvgBitrate,
		"packet_loss_rate":        summary.PacketLossRate,
		"source":                  source,
		"clips_added":             summary.ClipsAdded,
		"clips_deleted":           summary.ClipsDeleted,
		"clip_storage_added_gb":   summary.ClipStorageAddedGB,
		"clip_storage_deleted_gb": summary.ClipStorageDeletedGB,
		"storage_gb":              summary.StorageGB,
	}

	// Upsert each usage type
	for usageType, usageValue := range usageTypes {
		if usageValue <= 0 {
			continue
		}

		_, err := jm.db.Exec(`
			INSERT INTO purser.usage_records (tenant_id, cluster_id, usage_type, usage_value, usage_details, billing_month, period_start, period_end, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
			ON CONFLICT (tenant_id, cluster_id, usage_type, period_start, period_end) DO UPDATE SET
				usage_value = purser.usage_records.usage_value + EXCLUDED.usage_value,
				usage_details = EXCLUDED.usage_details,
				updated_at = NOW()
		`, summary.TenantID, summary.ClusterID, usageType, usageValue, usageDetails, billingMonth, periodStart, periodEnd)

		if err != nil {
			return fmt.Errorf("failed to upsert %s: %w", usageType, err)
		}
	}

	return nil
}

// updateInvoiceDraft creates or updates an invoice draft for the tenant based on usage
func (jm *JobManager) updateInvoiceDraft(tenantID string) error {
	var (
		tierID              string
		subscriptionStatus  string
		basePrice           float64
		meteringEnabled     bool
		overageRates        models.OverageRates
		storageAllocation   models.AllocationDetails
		bandwidthAllocation models.AllocationDetails
		customPricing       models.CustomPricing
	)

	err := jm.db.QueryRow(`
		SELECT ts.tier_id, ts.status, bt.base_price, bt.metering_enabled,
		       bt.overage_rates, bt.storage_allocation, bt.bandwidth_allocation, ts.custom_pricing
		FROM purser.tenant_subscriptions ts
		JOIN purser.billing_tiers bt ON ts.tier_id = bt.id
		WHERE ts.tenant_id = $1 AND ts.status = 'active' AND bt.is_active = true
	`, tenantID).Scan(&tierID, &subscriptionStatus, &basePrice, &meteringEnabled,
		&overageRates, &storageAllocation, &bandwidthAllocation, &customPricing)

	if err == sql.ErrNoRows {
		jm.logger.WithField("tenant_id", tenantID).Debug("No active subscription, skipping invoice draft")
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get subscription: %w", err)
	}

	// Get current billing period
	now := time.Now()
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	periodEnd := periodStart.AddDate(0, 1, 0).Add(-time.Second)

	// Aggregate usage for current billing period using precise timestamps
	rows, err := jm.db.Query(`
		SELECT usage_type, SUM(usage_value) as total
		FROM purser.usage_records
		WHERE tenant_id = $1
		  AND period_start >= $2
		  AND period_end <= $3
		GROUP BY usage_type
	`, tenantID, periodStart, periodEnd)
	if err != nil {
		return fmt.Errorf("failed to query usage: %w", err)
	}
	defer rows.Close()

	var totalStreamHours, totalViewerHours, totalEgressGB, totalRecordingGB float64
	var maxViewers, totalStreams int64
	allUsageDetails := make(map[string]interface{})

	for rows.Next() {
		var usageType string
		var total float64
		if err := rows.Scan(&usageType, &total); err != nil {
			continue
		}
		allUsageDetails[usageType] = total

		switch usageType {
		case "stream_hours":
			totalStreamHours = total
		case "viewer_hours":
			totalViewerHours = total
		case "egress_gb":
			totalEgressGB = total
		case "recording_gb":
			totalRecordingGB = total
		}
	}

	// Calculate charges
	totalAmount := basePrice
	if customPricing.BasePrice > 0 {
		totalAmount = customPricing.BasePrice
	}

	if meteringEnabled {
		// Bandwidth overage (delivered minutes = viewer_hours * 60)
		deliveredMinutes := totalViewerHours * 60
		if bandwidthAllocation.Limit != nil && overageRates.Bandwidth.UnitPrice > 0 {
			billableMinutes := deliveredMinutes - *bandwidthAllocation.Limit
			if billableMinutes > 0 {
				totalAmount += billableMinutes * overageRates.Bandwidth.UnitPrice
			}
		}

		// Storage overage
		storageUsage := totalRecordingGB
		if storageAllocation.Limit != nil && overageRates.Storage.UnitPrice > 0 {
			billableStorage := storageUsage - *storageAllocation.Limit
			if billableStorage > 0 {
				totalAmount += billableStorage * overageRates.Storage.UnitPrice
			}
		}
	}

	draftUsageDetails, _ := json.Marshal(allUsageDetails)

	// Upsert invoice draft
	_, err = jm.db.Exec(`
		INSERT INTO purser.invoice_drafts (
			tenant_id, billing_period_start, billing_period_end,
			stream_hours, egress_gb, recording_gb, max_viewers, total_streams,
			calculated_amount, status, usage_details, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'draft', $10, NOW(), NOW())
		ON CONFLICT (tenant_id, billing_period_start) DO UPDATE SET
			stream_hours = EXCLUDED.stream_hours,
			egress_gb = EXCLUDED.egress_gb,
			recording_gb = EXCLUDED.recording_gb,
			max_viewers = EXCLUDED.max_viewers,
			total_streams = EXCLUDED.total_streams,
			calculated_amount = EXCLUDED.calculated_amount,
			usage_details = EXCLUDED.usage_details,
			updated_at = NOW()
	`, tenantID, periodStart, periodEnd, totalStreamHours, totalEgressGB,
		totalRecordingGB, maxViewers, totalStreams, totalAmount, draftUsageDetails)

	if err != nil {
		return fmt.Errorf("failed to upsert invoice draft: %w", err)
	}

	jm.logger.WithFields(logging.Fields{
		"tenant_id":         tenantID,
		"billing_period":    periodStart.Format("2006-01"),
		"calculated_amount": totalAmount,
	}).Debug("Updated invoice draft")

	return nil
}
