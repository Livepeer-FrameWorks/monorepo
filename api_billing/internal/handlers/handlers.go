package handlers

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"

	// HTTP client for direct API calls (we'll use this for all payment providers)
	"github.com/go-resty/resty/v2"

	purserapi "frameworks/pkg/api/purser"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/models"
)

var (
	db           *sql.DB
	logger       logging.Logger
	emailService *EmailService
)

// Init initializes the handlers with database and logger
func Init(database *sql.DB, log logging.Logger) {
	db = database
	logger = log
	emailService = NewEmailService(log)
}

// Billing API Endpoints

// GetAvailablePaymentMethods returns payment methods available based on env vars
func GetAvailablePaymentMethods() []string {
	var methods []string

	// Check Stripe
	if os.Getenv("STRIPE_SECRET_KEY") != "" {
		methods = append(methods, "stripe")
	}

	// Check Mollie
	if os.Getenv("MOLLIE_API_KEY") != "" {
		methods = append(methods, "mollie")
	}

	// Check crypto (require at least one blockchain API)
	cryptoAvailable := false
	if os.Getenv("ETHERSCAN_API_KEY") != "" || os.Getenv("ETHEREUM_RPC_URL") != "" {
		cryptoAvailable = true
	}
	if os.Getenv("BLOCKCYPHER_API_KEY") != "" {
		cryptoAvailable = true
	}

	if cryptoAvailable {
		methods = append(methods, "crypto_btc", "crypto_eth", "crypto_usdc", "crypto_lpt")
	}

	return methods
}

// GetPlans returns all available billing plans with available payment methods
// DEPRECATED: Use GetTiers instead - kept for backwards compatibility
func GetPlans(c middleware.Context) {
	// Redirect to GetTiers for backwards compatibility
	GetTiers(c)
}

// GetTiers returns all available billing tiers with available payment methods
func GetTiers(c middleware.Context) {
	rows, err := db.Query(`
		SELECT id, tier_name, display_name, description, base_price, currency, 
		       billing_period, bandwidth_allocation, storage_allocation, compute_allocation,
		       features, support_level, sla_level, metering_enabled, overage_rates,
		       is_active, sort_order, is_enterprise, created_at, updated_at
		FROM billing_tiers
		WHERE is_active = true
		ORDER BY sort_order ASC
	`)

	if err != nil {
		logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to fetch billing tiers")
		c.JSON(http.StatusInternalServerError, purserapi.ErrorResponse{Error: "Failed to fetch tiers"})
		return
	}
	defer rows.Close()

	var tiers []models.BillingTier
	for rows.Next() {
		var tier models.BillingTier
		err := rows.Scan(&tier.ID, &tier.TierName, &tier.DisplayName, &tier.Description,
			&tier.BasePrice, &tier.Currency, &tier.BillingPeriod,
			&tier.BandwidthAllocation, &tier.StorageAllocation, &tier.ComputeAllocation,
			&tier.Features, &tier.SupportLevel, &tier.SLALevel,
			&tier.MeteringEnabled, &tier.OverageRates, &tier.IsActive,
			&tier.SortOrder, &tier.IsEnterprise, &tier.CreatedAt, &tier.UpdatedAt)

		if err != nil {
			logger.WithFields(logging.Fields{
				"error": err,
			}).Error("Error scanning tier")
			continue
		}
		tiers = append(tiers, tier)
	}

	logger.WithFields(logging.Fields{
		"tier_count": len(tiers),
		"method":     "GetTiers",
	}).Info("Retrieved billing tiers")

	c.JSON(http.StatusOK, purserapi.GetBillingTiersResponse{
		Tiers:          tiers,
		Count:          len(tiers),
		PaymentMethods: []string{"mollie", "crypto_btc", "crypto_eth", "crypto_usdc", "crypto_lpt"},
	})
}

// GetInvoice returns a specific invoice with usage details
func GetInvoice(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, purserapi.ErrorResponse{Error: "Tenant context required"})
		return
	}

	invoiceID := c.Param("invoice_id")
	if invoiceID == "" {
		c.JSON(http.StatusBadRequest, purserapi.ErrorResponse{Error: "Invoice ID required"})
		return
	}

	// Get invoice with usage details
	var invoice models.Invoice

	err := db.QueryRow(`
		SELECT id, tenant_id, amount, base_amount, metered_amount, currency, status, 
		       due_date, paid_at, usage_details, created_at
		FROM billing_invoices
		WHERE id = $1 AND tenant_id = $2
	`, invoiceID, tenantID).Scan(
		&invoice.ID,
		&invoice.TenantID,
		&invoice.Amount,
		&invoice.BaseAmount,
		&invoice.MeteredAmount,
		&invoice.Currency,
		&invoice.Status,
		&invoice.DueDate,
		&invoice.PaidAt,
		&invoice.UsageDetails,
		&invoice.CreatedAt,
	)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, purserapi.ErrorResponse{Error: "Invoice not found"})
		return
	}

	if err != nil {
		logger.WithError(err).Error("Failed to fetch invoice")
		c.JSON(http.StatusInternalServerError, purserapi.ErrorResponse{Error: "Failed to fetch invoice"})
		return
	}

	// Get tier details for the invoice period
	var tier models.BillingTier

	err = db.QueryRow(`
		SELECT bt.tier_name, bt.display_name, bt.metering_enabled, bt.overage_rates
		FROM billing_tiers bt
		JOIN tenant_subscriptions ts ON ts.tier_id = bt.id
		WHERE ts.tenant_id = $1
		AND ts.created_at <= $2
		ORDER BY ts.created_at DESC
		LIMIT 1
	`, tenantID, invoice.CreatedAt).Scan(
		&tier.TierName,
		&tier.DisplayName,
		&tier.MeteringEnabled,
		&tier.OverageRates,
	)

	if err != nil && err != sql.ErrNoRows {
		logger.WithError(err).Error("Failed to fetch tier details")
		// Continue without tier details
	}

	// Return invoice with tier details
	c.JSON(http.StatusOK, purserapi.GetInvoiceResponse{
		Invoice: invoice,
		Tier:    tier,
	})
}

// GetInvoices returns all invoices for a tenant
func GetInvoices(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, purserapi.ErrorResponse{Error: "Tenant context required"})
		return
	}

	// Optional query parameters
	limit := c.DefaultQuery("limit", "50")
	offset := c.DefaultQuery("offset", "0")
	status := c.DefaultQuery("status", "")

	// Build query
	query := `
		SELECT id, tenant_id, amount, base_amount, metered_amount, currency, status, 
		       due_date, paid_at, usage_details, created_at
		FROM billing_invoices
		WHERE tenant_id = $1
	`
	args := []interface{}{tenantID}
	argCount := 1

	if status != "" {
		argCount++
		query += fmt.Sprintf(" AND status = $%d", argCount)
		args = append(args, status)
	}

	query += " ORDER BY created_at DESC LIMIT $" + strconv.Itoa(argCount+1) + " OFFSET $" + strconv.Itoa(argCount+2)
	args = append(args, limit, offset)

	// Execute query
	rows, err := db.Query(query, args...)
	if err != nil {
		logger.WithError(err).Error("Failed to fetch invoices")
		c.JSON(http.StatusInternalServerError, purserapi.ErrorResponse{Error: "Failed to fetch invoices"})
		return
	}
	defer rows.Close()

	// Scan results
	var invoices []models.Invoice

	for rows.Next() {
		var invoice models.Invoice

		err := rows.Scan(
			&invoice.ID,
			&invoice.TenantID,
			&invoice.Amount,
			&invoice.BaseAmount,
			&invoice.MeteredAmount,
			&invoice.Currency,
			&invoice.Status,
			&invoice.DueDate,
			&invoice.PaidAt,
			&invoice.UsageDetails,
			&invoice.CreatedAt,
		)

		if err != nil {
			logger.WithError(err).Error("Failed to scan invoice")
			continue
		}

		invoices = append(invoices, invoice)
	}

	// Get total count
	var totalCount int
	countQuery := `
		SELECT COUNT(*) 
		FROM billing_invoices 
		WHERE tenant_id = $1
	`
	countArgs := []interface{}{tenantID}
	if status != "" {
		countQuery += " AND status = $2"
		countArgs = append(countArgs, status)
	}

	err = db.QueryRow(countQuery, countArgs...).Scan(&totalCount)
	if err != nil {
		logger.WithError(err).Error("Failed to count invoices")
		// Continue without total count
	}

	limitInt, _ := strconv.Atoi(limit)
	offsetInt, _ := strconv.Atoi(offset)

	c.JSON(http.StatusOK, purserapi.GetInvoicesResponse{
		Invoices: invoices,
		Total:    totalCount,
		Limit:    limitInt,
		Offset:   offsetInt,
	})
}

// CreatePayment creates a payment request for an invoice
func CreatePayment(c middleware.Context) {
	tenantID := c.GetString("tenant_id")

	var req purserapi.PaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, purserapi.ErrorResponse{Error: err.Error()})
		return
	}

	// Check if requested payment method is available
	availableMethods := GetAvailablePaymentMethods()
	methodAvailable := false
	for _, method := range availableMethods {
		if method == req.Method {
			methodAvailable = true
			break
		}
	}

	if !methodAvailable {
		c.JSON(http.StatusBadRequest, purserapi.PaymentMethodErrorResponse{
			Error:            "Payment method not available",
			AvailableMethods: availableMethods,
		})
		return
	}

	// Verify invoice belongs to tenant and is unpaid
	var invoice models.Invoice
	err := db.QueryRow(`
		SELECT id, tenant_id, amount, currency, status, due_date, created_at
		FROM billing_invoices 
		WHERE id = $1 AND tenant_id = $2 AND status = 'pending'
	`, req.InvoiceID, tenantID).Scan(&invoice.ID, &invoice.TenantID,
		&invoice.Amount, &invoice.Currency, &invoice.Status,
		&invoice.DueDate, &invoice.CreatedAt)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, purserapi.ErrorResponse{Error: "Invoice not found or already paid"})
		return
	} else if err != nil {
		logger.WithFields(logging.Fields{
			"error":      err,
			"invoice_id": req.InvoiceID,
			"tenant_id":  tenantID,
		}).Error("Database error fetching invoice")
		c.JSON(http.StatusInternalServerError, purserapi.ErrorResponse{Error: "Database error"})
		return
	}

	// Create payment record
	paymentID := uuid.New().String()
	var paymentResponse purserapi.PaymentResponse

	// Route to appropriate payment handler
	switch {
	case req.Method == "stripe" || req.Method == "mollie":
		// Traditional payment processing
		paymentURL, txID, err := createTraditionalPayment(req.Method, invoice)
		if err != nil {
			logger.WithFields(logging.Fields{
				"error":      err,
				"method":     req.Method,
				"invoice_id": req.InvoiceID,
				"tenant_id":  tenantID,
			}).Error("Failed to create traditional payment")
			c.JSON(http.StatusInternalServerError, purserapi.ErrorResponse{Error: "Failed to create payment"})
			return
		}

		// Store payment record
		_, err = db.Exec(`
			INSERT INTO billing_payments (id, invoice_id, method, amount, currency, tx_id, status, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'pending', NOW(), NOW())
		`, paymentID, req.InvoiceID, req.Method, invoice.Amount, invoice.Currency, txID)

		if err != nil {
			logger.WithFields(logging.Fields{
				"error":      err,
				"payment_id": paymentID,
				"invoice_id": req.InvoiceID,
			}).Error("Failed to store payment record")
			c.JSON(http.StatusInternalServerError, purserapi.ErrorResponse{Error: "Failed to store payment"})
			return
		}

		paymentResponse = purserapi.PaymentResponse{
			ID:         paymentID,
			PaymentURL: paymentURL,
			Amount:     invoice.Amount,
			Currency:   invoice.Currency,
			Status:     "pending",
		}

	case req.Method == "crypto_btc" || req.Method == "crypto_eth" || req.Method == "crypto_usdc" || req.Method == "crypto_lpt":
		// Crypto payment processing
		asset := extractCryptoAsset(req.Method)
		walletAddress, expiresAt, err := createCryptoPayment(asset, invoice)
		if err != nil {
			logger.WithFields(logging.Fields{
				"error":      err,
				"method":     req.Method,
				"asset":      asset,
				"invoice_id": req.InvoiceID,
				"tenant_id":  tenantID,
			}).Error("Failed to create crypto payment")
			c.JSON(http.StatusInternalServerError, purserapi.ErrorResponse{Error: "Failed to create crypto payment"})
			return
		}

		// Store payment record
		_, err = db.Exec(`
			INSERT INTO billing_payments (id, invoice_id, method, amount, currency, status, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, 'pending', NOW(), NOW())
		`, paymentID, req.InvoiceID, req.Method, invoice.Amount, invoice.Currency)

		if err != nil {
			logger.WithFields(logging.Fields{
				"error":      err,
				"payment_id": paymentID,
				"invoice_id": req.InvoiceID,
			}).Error("Failed to store crypto payment record")
			c.JSON(http.StatusInternalServerError, purserapi.ErrorResponse{Error: "Failed to store payment"})
			return
		}

		paymentResponse = purserapi.PaymentResponse{
			ID:            paymentID,
			WalletAddress: walletAddress,
			Amount:        invoice.Amount,
			Currency:      invoice.Currency,
			ExpiresAt:     &expiresAt,
			Status:        "pending",
		}

	default:
		c.JSON(http.StatusBadRequest, purserapi.ErrorResponse{Error: "Unsupported payment method"})
		return
	}

	logger.WithFields(logging.Fields{
		"payment_id": paymentID,
		"invoice_id": req.InvoiceID,
		"tenant_id":  tenantID,
		"method":     req.Method,
		"amount":     invoice.Amount,
	}).Info("Payment created successfully")

	c.JSON(http.StatusCreated, paymentResponse)
}

// GetBillingStatus returns current billing status for the tenant
func GetBillingStatus(c middleware.Context) {
	tenantID := c.GetString("tenant_id")

	// Get tenant's current subscription and tier
	var subscription models.TenantSubscription
	var tier models.BillingTier

	err := db.QueryRow(`
		SELECT 
			ts.id, ts.tenant_id, ts.tier_id, ts.status, ts.billing_email,
			ts.started_at, ts.trial_ends_at, ts.next_billing_date, ts.cancelled_at,
			ts.custom_pricing, ts.custom_features, ts.custom_allocations,
			ts.payment_method, ts.payment_reference, ts.billing_address,
			ts.tax_id, ts.tax_rate, ts.created_at, ts.updated_at,
			bt.id, bt.tier_name, bt.display_name, bt.description,
			bt.base_price, bt.currency, bt.billing_period,
			bt.bandwidth_allocation, bt.storage_allocation, bt.compute_allocation,
			bt.features, bt.support_level, bt.sla_level,
			bt.metering_enabled, bt.overage_rates, bt.is_active,
			bt.sort_order, bt.is_enterprise, bt.created_at, bt.updated_at
		FROM tenant_subscriptions ts
		JOIN billing_tiers bt ON ts.tier_id = bt.id
		WHERE ts.tenant_id = $1 AND ts.status != 'cancelled'
		ORDER BY ts.created_at DESC
		LIMIT 1
	`, tenantID).Scan(
		&subscription.ID, &subscription.TenantID, &subscription.TierID, &subscription.Status, &subscription.BillingEmail,
		&subscription.StartedAt, &subscription.TrialEndsAt, &subscription.NextBillingDate, &subscription.CancelledAt,
		&subscription.CustomPricing, &subscription.CustomFeatures, &subscription.CustomAllocations,
		&subscription.PaymentMethod, &subscription.PaymentReference, &subscription.BillingAddress,
		&subscription.TaxID, &subscription.TaxRate, &subscription.CreatedAt, &subscription.UpdatedAt,
		&tier.ID, &tier.TierName, &tier.DisplayName, &tier.Description,
		&tier.BasePrice, &tier.Currency, &tier.BillingPeriod,
		&tier.BandwidthAllocation, &tier.StorageAllocation, &tier.ComputeAllocation,
		&tier.Features, &tier.SupportLevel, &tier.SLALevel,
		&tier.MeteringEnabled, &tier.OverageRates, &tier.IsActive,
		&tier.SortOrder, &tier.IsEnterprise, &tier.CreatedAt, &tier.UpdatedAt)

	if err != nil {
		logger.WithFields(logging.Fields{
			"error":     err,
			"tenant_id": tenantID,
		}).Error("Failed to fetch tenant subscription")
		c.JSON(http.StatusInternalServerError, purserapi.ErrorResponse{Error: "Failed to fetch billing status"})
		return
	}

	// Get pending invoices
	pendingRows, err := db.Query(`
		SELECT id, tenant_id, amount, currency, status, due_date, created_at, updated_at
		FROM billing_invoices 
		WHERE tenant_id = $1 AND status = 'pending'
		ORDER BY due_date ASC
	`, tenantID)

	var pendingInvoices []models.Invoice
	if err == nil {
		defer pendingRows.Close()
		for pendingRows.Next() {
			var invoice models.Invoice
			err := pendingRows.Scan(&invoice.ID, &invoice.TenantID, &invoice.Amount,
				&invoice.Currency, &invoice.Status, &invoice.DueDate,
				&invoice.CreatedAt, &invoice.UpdatedAt)
			if err == nil {
				pendingInvoices = append(pendingInvoices, invoice)
			}
		}
	}

	// Get recent payments
	paymentRows, err := db.Query(`
		SELECT bp.id, bp.invoice_id, bp.method, bp.amount, bp.currency, 
			   bp.tx_id, bp.status, bp.confirmed_at, bp.created_at, bp.updated_at
		FROM billing_payments bp
		JOIN billing_invoices bi ON bp.invoice_id = bi.id
		WHERE bi.tenant_id = $1
		ORDER BY bp.created_at DESC
		LIMIT 5
	`, tenantID)

	var recentPayments []models.Payment
	if err == nil {
		defer paymentRows.Close()
		for paymentRows.Next() {
			var payment models.Payment
			err := paymentRows.Scan(&payment.ID, &payment.InvoiceID, &payment.Method,
				&payment.Amount, &payment.Currency, &payment.TxID, &payment.Status,
				&payment.ConfirmedAt, &payment.CreatedAt, &payment.UpdatedAt)
			if err == nil {
				recentPayments = append(recentPayments, payment)
			}
		}
	}

	// Get usage summary for current month
	var usageSummary models.JSONB
	err = db.QueryRow(`
		SELECT 
			json_build_object(
				'stream_hours', COALESCE(SUM(CASE WHEN usage_type = 'stream_hours' THEN usage_value ELSE 0 END), 0),
				'egress_gb', COALESCE(SUM(CASE WHEN usage_type = 'egress_gb' THEN usage_value ELSE 0 END), 0),
				'recording_gb', COALESCE(SUM(CASE WHEN usage_type = 'recording_gb' THEN usage_value ELSE 0 END), 0),
				'peak_bandwidth_mbps', COALESCE(MAX(CASE WHEN usage_type = 'peak_bandwidth_mbps' THEN usage_value ELSE 0 END), 0)
			) as usage_summary
		FROM usage_records
		WHERE tenant_id = $1 
		AND billing_month = to_char(CURRENT_DATE, 'YYYY-MM')
	`, tenantID).Scan(&usageSummary)

	if err != nil {
		logger.WithError(err).Debug("Failed to get usage summary, defaulting to empty")
		usageSummary = models.JSONB{}
	}

	billingStatus := models.BillingStatus{
		TenantID:        tenantID,
		Subscription:    subscription,
		Tier:            tier,
		Status:          subscription.Status,
		NextBillingDate: subscription.NextBillingDate,
		PendingInvoices: pendingInvoices,
		RecentPayments:  recentPayments,
		UsageSummary:    usageSummary,
	}

	logger.WithFields(logging.Fields{
		"tenant_id":        tenantID,
		"tier":             tier.TierName,
		"status":           subscription.Status,
		"pending_invoices": len(pendingInvoices),
		"recent_payments":  len(recentPayments),
	}).Info("Retrieved billing status")

	c.JSON(http.StatusOK, billingStatus)
}

// Helper functions for payment processing

func createTraditionalPayment(method string, invoice models.Invoice) (string, string, error) {
	switch method {
	case "stripe":
		return createStripePayment(invoice)
	case "mollie":
		return createMolliePayment(invoice)
	default:
		return "", "", fmt.Errorf("unsupported payment method: %s", method)
	}
}

func createStripePayment(invoice models.Invoice) (string, string, error) {
	// Check if Stripe is configured
	stripeKey := os.Getenv("STRIPE_SECRET_KEY")
	if stripeKey == "" {
		return "", "", fmt.Errorf("Stripe not configured")
	}

	client := resty.New()

	// Create Stripe Payment Intent via API
	// Stripe payment is created via direct API call

	var result map[string]interface{}
	resp, err := client.R().
		SetHeader("Content-Type", "application/x-www-form-urlencoded").
		SetHeader("Authorization", "Bearer "+stripeKey).
		SetFormData(map[string]string{
			"amount":               fmt.Sprintf("%d", int64(invoice.Amount*100)),
			"currency":             invoice.Currency,
			"metadata[invoice_id]": invoice.ID,
			"metadata[tenant_id]":  invoice.TenantID,
		}).
		SetResult(&result).
		Post("https://api.stripe.com/v1/payment_intents")

	if err != nil {
		return "", "", fmt.Errorf("stripe API request failed: %v", err)
	}

	if resp.StatusCode() != 200 {
		return "", "", fmt.Errorf("stripe API returned status %d: %s", resp.StatusCode(), string(resp.Body()))
	}

	paymentIntentID, ok := result["id"].(string)
	if !ok {
		return "", "", fmt.Errorf("invalid stripe response: missing payment intent ID")
	}

	clientSecret, ok := result["client_secret"].(string)
	if !ok {
		return "", "", fmt.Errorf("invalid stripe response: missing client secret")
	}

	paymentURL := fmt.Sprintf("%s/payment/stripe?client_secret=%s", os.Getenv("BASE_URL"), clientSecret)

	logger.WithFields(logging.Fields{
		"method":         "stripe",
		"invoice_id":     invoice.ID,
		"payment_intent": paymentIntentID,
		"amount":         invoice.Amount,
	}).Info("Created Stripe payment")

	return paymentURL, paymentIntentID, nil
}

func createMolliePayment(invoice models.Invoice) (string, string, error) {
	// Check if Mollie is configured
	mollieKey := os.Getenv("MOLLIE_API_KEY")
	if mollieKey == "" {
		return "", "", fmt.Errorf("Mollie not configured")
	}

	client := resty.New()

	// Create Mollie Payment via API
	payload := map[string]interface{}{
		"amount": map[string]string{
			"currency": invoice.Currency,
			"value":    fmt.Sprintf("%.2f", invoice.Amount),
		},
		"description": fmt.Sprintf("Invoice %s", invoice.ID),
		"redirectUrl": fmt.Sprintf("%s/payment/success", os.Getenv("BASE_URL")),
		"webhookUrl":  fmt.Sprintf("%s/api/v1/webhooks/mollie", os.Getenv("BASE_URL")),
		"metadata": map[string]string{
			"invoice_id": invoice.ID,
			"tenant_id":  invoice.TenantID,
		},
	}

	var result map[string]interface{}
	resp, err := client.R().
		SetHeader("Content-Type", "application/json").
		SetHeader("Authorization", "Bearer "+mollieKey).
		SetBody(payload).
		SetResult(&result).
		Post("https://api.mollie.com/v2/payments")

	if err != nil {
		return "", "", fmt.Errorf("mollie API request failed: %v", err)
	}

	if resp.StatusCode() != 201 {
		return "", "", fmt.Errorf("mollie API returned status %d: %s", resp.StatusCode(), string(resp.Body()))
	}

	paymentID, ok := result["id"].(string)
	if !ok {
		return "", "", fmt.Errorf("invalid mollie response: missing payment ID")
	}

	// Get checkout URL from _links
	links, ok := result["_links"].(map[string]interface{})
	if !ok {
		return "", "", fmt.Errorf("invalid mollie response: missing links")
	}

	checkout, ok := links["checkout"].(map[string]interface{})
	if !ok {
		return "", "", fmt.Errorf("invalid mollie response: missing checkout link")
	}

	paymentURL, ok := checkout["href"].(string)
	if !ok {
		return "", "", fmt.Errorf("invalid mollie response: missing checkout URL")
	}

	logger.WithFields(logging.Fields{
		"method":      "mollie",
		"invoice_id":  invoice.ID,
		"payment_id":  paymentID,
		"payment_url": paymentURL,
		"amount":      invoice.Amount,
	}).Info("Created Mollie payment")

	return paymentURL, paymentID, nil
}

func createCryptoPayment(asset string, invoice models.Invoice) (string, time.Time, error) {
	walletAddress, err := generateRealCryptoAddress(asset)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to generate %s address: %v", asset, err)
	}

	expiresAt := time.Now().Add(24 * time.Hour) // 24 hour expiry

	// Store crypto wallet record
	_, err = db.Exec(`
		INSERT INTO crypto_wallets (id, tenant_id, invoice_id, asset, wallet_address, status, expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'active', $6, NOW(), NOW())
		ON CONFLICT (invoice_id, asset) DO UPDATE SET
			wallet_address = EXCLUDED.wallet_address,
			expires_at = EXCLUDED.expires_at,
			updated_at = NOW()
	`, uuid.New().String(), invoice.TenantID, invoice.ID, asset, walletAddress, expiresAt)

	if err != nil {
		return "", time.Time{}, err
	}

	logger.WithFields(logging.Fields{
		"asset":          asset,
		"invoice_id":     invoice.ID,
		"wallet_address": walletAddress,
		"expires_at":     expiresAt,
		"amount":         invoice.Amount,
	}).Info("Created crypto payment")

	return walletAddress, expiresAt, nil
}

func generateTxID(method string) string {
	prefix := map[string]string{
		"mollie": "tr_",
	}

	bytes := make([]byte, 16)
	rand.Read(bytes)
	return prefix[method] + hex.EncodeToString(bytes)
}

func extractCryptoAsset(method string) string {
	assets := map[string]string{
		"crypto_btc":  "BTC",
		"crypto_eth":  "ETH",
		"crypto_usdc": "USDC",
		"crypto_lpt":  "LPT",
	}
	return assets[method]
}

// Crypto address generation functions are implemented in crypto.go
// Webhook handlers are implemented in webhooks.go
