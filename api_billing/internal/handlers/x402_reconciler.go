package handlers

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"frameworks/pkg/billing"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

// X402Reconciler monitors pending x402 settlements and confirms or fails them
// based on on-chain transaction receipts.
type X402Reconciler struct {
	db              *sql.DB
	logger          logging.Logger
	stopCh          chan struct{}
	includeTestnets bool
}

// PendingSettlement represents an x402 settlement awaiting confirmation
type PendingSettlement struct {
	ID          string
	Network     string
	TxHash      string
	TenantID    string
	AmountCents int64
	SettledAt   time.Time
}

// TransactionReceipt represents an Ethereum transaction receipt
type TransactionReceipt struct {
	Status      string `json:"status"`      // "0x1" for success, "0x0" for revert
	BlockNumber string `json:"blockNumber"` // hex
	GasUsed     string `json:"gasUsed"`     // hex
}

// NewX402Reconciler creates a new x402 settlement reconciler
func NewX402Reconciler(database *sql.DB, log logging.Logger, includeTestnets bool) *X402Reconciler {
	return &X402Reconciler{
		db:              database,
		logger:          log,
		stopCh:          make(chan struct{}),
		includeTestnets: includeTestnets,
	}
}

// Start begins the reconciliation loop
func (r *X402Reconciler) Start(ctx context.Context) {
	r.logger.Info("Starting x402 settlement reconciler")

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("X402 reconciler stopping due to context cancellation")
			return
		case <-r.stopCh:
			r.logger.Info("X402 reconciler stopping")
			return
		case <-ticker.C:
			r.reconcilePendingSettlements(ctx)
			r.reconcileFailedTimeouts(ctx)
			r.reconcileConfirmedSettlements(ctx)
		}
	}
}

// Stop stops the reconciler
func (r *X402Reconciler) Stop() {
	close(r.stopCh)
}

// reconcilePendingSettlements checks all pending settlements and confirms or fails them
func (r *X402Reconciler) reconcilePendingSettlements(ctx context.Context) {
	// Query pending settlements older than 15 seconds (give tx time to propagate)
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, network, tx_hash, tenant_id, amount_cents, settled_at
		FROM purser.x402_nonces
		WHERE status = 'pending'
		AND settled_at < NOW() - INTERVAL '15 seconds'
		ORDER BY settled_at ASC
		LIMIT 50
	`)
	if err != nil {
		r.logger.WithError(err).Error("Failed to query pending x402 settlements")
		return
	}
	defer rows.Close()

	var settlements []PendingSettlement
	for rows.Next() {
		var s PendingSettlement
		if err := rows.Scan(&s.ID, &s.Network, &s.TxHash, &s.TenantID, &s.AmountCents, &s.SettledAt); err != nil {
			r.logger.WithError(err).Error("Failed to scan pending settlement")
			continue
		}
		settlements = append(settlements, s)
	}

	if len(settlements) == 0 {
		return
	}

	r.logger.WithField("count", len(settlements)).Debug("Reconciling pending x402 settlements")

	for _, s := range settlements {
		r.reconcileSettlement(ctx, s)
	}
}

// reconcileSettlement checks a single settlement and updates its status
func (r *X402Reconciler) reconcileSettlement(ctx context.Context, s PendingSettlement) {
	network, ok := Networks[s.Network]
	if !ok {
		r.logger.WithField("network", s.Network).Error("Unknown network for settlement")
		r.markFailed(ctx, s.ID, "unknown network")
		return
	}

	if network.IsTestnet && !r.includeTestnets {
		// Skip testnet settlements if testnets disabled
		return
	}

	receipt, err := r.getTransactionReceipt(ctx, network, s.TxHash)
	if err != nil {
		r.logger.WithError(err).WithFields(logging.Fields{
			"tx_hash": s.TxHash,
			"network": s.Network,
		}).Warn("Failed to get transaction receipt")

		// Check if timed out (2 minutes)
		if time.Since(s.SettledAt) > 2*time.Minute {
			r.logger.WithFields(logging.Fields{
				"tx_hash":   s.TxHash,
				"tenant_id": s.TenantID,
				"age":       time.Since(s.SettledAt).String(),
			}).Error("X402 settlement timed out - transaction not mined")
			r.markFailed(ctx, s.ID, "timeout - transaction not mined within 2 minutes")
			r.debitBalance(ctx, s.TenantID, s.AmountCents, s.TxHash)
		}
		return
	}

	if receipt == nil {
		// Transaction still pending
		if time.Since(s.SettledAt) > 2*time.Minute {
			r.logger.WithFields(logging.Fields{
				"tx_hash":   s.TxHash,
				"tenant_id": s.TenantID,
			}).Error("X402 settlement timed out - no receipt after 2 minutes")
			r.markFailed(ctx, s.ID, "timeout - no receipt after 2 minutes")
			r.debitBalance(ctx, s.TenantID, s.AmountCents, s.TxHash)
		}
		return
	}

	// Check receipt status
	if receipt.Status == "0x1" {
		blockNum := parseHexInt64(receipt.BlockNumber)
		gasUsed := parseHexInt64(receipt.GasUsed)
		confirmed, err := r.hasRequiredConfirmations(ctx, network, blockNum)
		if err != nil {
			r.logger.WithError(err).WithFields(logging.Fields{
				"tx_hash": s.TxHash,
				"network": s.Network,
			}).Warn("Failed to determine confirmation depth")
			return
		}

		if !confirmed {
			r.updatePendingReceipt(ctx, s.ID, blockNum, gasUsed)
			return
		}

		r.markConfirmed(ctx, s.ID, blockNum, gasUsed)
		r.logger.WithFields(logging.Fields{
			"tx_hash":      s.TxHash,
			"tenant_id":    s.TenantID,
			"block_number": blockNum,
			"gas_used":     gasUsed,
		}).Info("X402 settlement confirmed on-chain")

		emitBillingEvent(eventX402SettlementConfirm, s.TenantID, "x402_nonce", s.TxHash, &pb.BillingEvent{
			Amount:   float64(s.AmountCents) / 100,
			Currency: "EUR",
			Status:   "confirmed",
		})
	} else {
		// Reverted
		r.logger.WithFields(logging.Fields{
			"tx_hash":   s.TxHash,
			"tenant_id": s.TenantID,
			"status":    receipt.Status,
		}).Error("X402 settlement reverted on-chain")
		r.markFailed(ctx, s.ID, "transaction reverted on-chain")
		r.debitBalance(ctx, s.TenantID, s.AmountCents, s.TxHash)
	}
}

func (r *X402Reconciler) reconcileFailedTimeouts(ctx context.Context) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, network, tx_hash, tenant_id, amount_cents, settled_at
		FROM purser.x402_nonces
		WHERE status = 'failed'
		AND failure_reason LIKE 'timeout%'
		AND settled_at > NOW() - INTERVAL '1 hour'
		ORDER BY settled_at ASC
		LIMIT 50
	`)
	if err != nil {
		r.logger.WithError(err).Error("Failed to query failed x402 settlements")
		return
	}
	defer rows.Close()

	for rows.Next() {
		var s PendingSettlement
		if err := rows.Scan(&s.ID, &s.Network, &s.TxHash, &s.TenantID, &s.AmountCents, &s.SettledAt); err != nil {
			r.logger.WithError(err).Error("Failed to scan failed settlement")
			continue
		}

		network, ok := Networks[s.Network]
		if !ok {
			r.logger.WithField("network", s.Network).Error("Unknown network for settlement")
			continue
		}

		receipt, err := r.getTransactionReceipt(ctx, network, s.TxHash)
		if err != nil || receipt == nil || receipt.Status != "0x1" {
			continue
		}

		blockNum := parseHexInt64(receipt.BlockNumber)
		gasUsed := parseHexInt64(receipt.GasUsed)
		confirmed, err := r.hasRequiredConfirmations(ctx, network, blockNum)
		if err != nil || !confirmed {
			continue
		}

		if err := r.creditBalance(ctx, s.TenantID, s.AmountCents, s.TxHash); err != nil {
			r.logger.WithError(err).WithField("tenant_id", s.TenantID).Error("Failed to re-credit balance after late settlement")
			continue
		}

		r.markConfirmed(ctx, s.ID, blockNum, gasUsed)
		emitBillingEvent(eventX402SettlementConfirm, s.TenantID, "x402_nonce", s.TxHash, &pb.BillingEvent{
			Amount:   float64(s.AmountCents) / 100,
			Currency: "EUR",
			Status:   "confirmed",
		})
	}
}

func (r *X402Reconciler) reconcileConfirmedSettlements(ctx context.Context) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, network, tx_hash, tenant_id, amount_cents, settled_at
		FROM purser.x402_nonces
		WHERE status = 'confirmed'
		AND confirmed_at > NOW() - INTERVAL '1 hour'
		ORDER BY confirmed_at ASC
		LIMIT 50
	`)
	if err != nil {
		r.logger.WithError(err).Error("Failed to query confirmed x402 settlements")
		return
	}
	defer rows.Close()

	for rows.Next() {
		var s PendingSettlement
		if err := rows.Scan(&s.ID, &s.Network, &s.TxHash, &s.TenantID, &s.AmountCents, &s.SettledAt); err != nil {
			r.logger.WithError(err).Error("Failed to scan confirmed settlement")
			continue
		}

		network, ok := Networks[s.Network]
		if !ok {
			continue
		}

		receipt, err := r.getTransactionReceipt(ctx, network, s.TxHash)
		if err != nil || receipt == nil || receipt.Status != "0x1" {
			r.markFailed(ctx, s.ID, "transaction reorged or missing")
			r.debitBalance(ctx, s.TenantID, s.AmountCents, s.TxHash)
		}
	}
}

func (r *X402Reconciler) hasRequiredConfirmations(ctx context.Context, network NetworkConfig, blockNum int64) (bool, error) {
	if blockNum == 0 {
		return false, nil
	}

	latest, err := r.getLatestBlockNumber(ctx, network)
	if err != nil {
		return false, err
	}

	if latest < blockNum {
		return false, nil
	}

	return (latest - blockNum) >= int64(network.Confirmations), nil
}

func (r *X402Reconciler) getLatestBlockNumber(ctx context.Context, network NetworkConfig) (int64, error) {
	rpcEndpoint := network.GetRPCEndpointWithDefault()
	if rpcEndpoint == "" {
		return 0, fmt.Errorf("no RPC endpoint for network %s", network.Name)
	}

	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "eth_blockNumber",
		"params":  []interface{}{},
		"id":      1,
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", rpcEndpoint, strings.NewReader(string(reqJSON)))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var rpcResp struct {
		Result string           `json:"result"`
		Error  *json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return 0, err
	}
	if rpcResp.Error != nil {
		return 0, fmt.Errorf("RPC error: %s", string(*rpcResp.Error))
	}

	return parseHexInt64(rpcResp.Result), nil
}

func (r *X402Reconciler) updatePendingReceipt(ctx context.Context, id string, blockNumber, gasUsed int64) {
	_, err := r.db.ExecContext(ctx, `
		UPDATE purser.x402_nonces
		SET block_number = $2, gas_used = $3
		WHERE id = $1
	`, id, blockNumber, gasUsed)
	if err != nil {
		r.logger.WithError(err).WithField("id", id).Error("Failed to update pending receipt metadata")
	}
}

func (r *X402Reconciler) creditBalance(ctx context.Context, tenantID string, amountCents int64, txHash string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	var balance int64
	currency := billing.DefaultCurrency()
	err = tx.QueryRowContext(ctx, `
		SELECT balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2
	`, tenantID, currency).Scan(&balance)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	newBalance := balance + amountCents

	_, err = tx.ExecContext(ctx, `
		INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (tenant_id, currency)
		DO UPDATE SET balance_cents = $2, updated_at = NOW()
	`, tenantID, newBalance, currency)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO purser.balance_transactions (
			id, tenant_id, amount_cents, balance_after_cents,
			transaction_type, description, reference_id, reference_type, created_at
		) VALUES ($1, $2, $3, $4, 'topup', $5, $6, 'x402_payment', NOW())
	`, uuid.New().String(), tenantID, amountCents, newBalance,
		fmt.Sprintf("x402 settlement recovered (%s)", truncateTxHash(txHash)), txHash)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// getTransactionReceipt fetches the transaction receipt from the network RPC
func (r *X402Reconciler) getTransactionReceipt(ctx context.Context, network NetworkConfig, txHash string) (*TransactionReceipt, error) {
	rpcEndpoint := network.GetRPCEndpointWithDefault()
	if rpcEndpoint == "" {
		return nil, fmt.Errorf("no RPC endpoint for network %s", network.Name)
	}

	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "eth_getTransactionReceipt",
		"params":  []string{txHash},
		"id":      1,
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", rpcEndpoint, strings.NewReader(string(reqJSON)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var rpcResp struct {
		Result *TransactionReceipt `json:"result"`
		Error  *json.RawMessage    `json:"error"`
	}
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, err
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error: %s", string(*rpcResp.Error))
	}

	// Returns nil if transaction not yet mined (receipt is null)
	return rpcResp.Result, nil
}

// markConfirmed updates the settlement status to confirmed
func (r *X402Reconciler) markConfirmed(ctx context.Context, id string, blockNumber, gasUsed int64) {
	_, err := r.db.ExecContext(ctx, `
		UPDATE purser.x402_nonces
		SET status = 'confirmed', confirmed_at = NOW(), block_number = $2, gas_used = $3
		WHERE id = $1
	`, id, blockNumber, gasUsed)
	if err != nil {
		r.logger.WithError(err).WithField("id", id).Error("Failed to mark settlement as confirmed")
	}
}

// markFailed updates the settlement status to failed
func (r *X402Reconciler) markFailed(ctx context.Context, id, reason string) {
	_, err := r.db.ExecContext(ctx, `
		UPDATE purser.x402_nonces
		SET status = 'failed', failure_reason = $2
		WHERE id = $1
	`, id, reason)
	if err != nil {
		r.logger.WithError(err).WithField("id", id).Error("Failed to mark settlement as failed")
	}
}

// debitBalance reverses the balance credit for a failed settlement
func (r *X402Reconciler) debitBalance(ctx context.Context, tenantID string, amountCents int64, txHash string) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		r.logger.WithError(err).Error("Failed to begin transaction for balance debit")
		return
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	// Get current balance
	var balance int64
	err = tx.QueryRowContext(ctx, `
		SELECT balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = 'EUR'
	`, tenantID).Scan(&balance)
	if err != nil && err != sql.ErrNoRows {
		r.logger.WithError(err).Error("Failed to get current balance for debit")
		return
	}

	// Deduct from balance (can go negative - accumulate debt, per existing pattern)
	newBalance := balance - amountCents

	// Update balance
	_, err = tx.ExecContext(ctx, `
		UPDATE purser.prepaid_balances
		SET balance_cents = $1, updated_at = NOW()
		WHERE tenant_id = $2 AND currency = 'EUR'
	`, newBalance, tenantID)
	if err != nil {
		r.logger.WithError(err).Error("Failed to update balance for debit")
		return
	}

	// Record reversal transaction
	_, err = tx.ExecContext(ctx, `
		INSERT INTO purser.balance_transactions (
			id, tenant_id, amount_cents, balance_after_cents,
			transaction_type, description, reference_id, reference_type, created_at
		) VALUES ($1, $2, $3, $4, 'reversal', $5, $6, 'x402_failed', NOW())
	`, uuid.New().String(), tenantID, -amountCents, newBalance,
		fmt.Sprintf("x402 settlement failed: %s", truncateTxHash(txHash)), txHash)
	if err != nil {
		r.logger.WithError(err).Error("Failed to record reversal transaction")
		return
	}

	if err := tx.Commit(); err != nil {
		r.logger.WithError(err).Error("Failed to commit balance debit transaction")
		return
	}

	r.logger.WithFields(logging.Fields{
		"tenant_id":   tenantID,
		"amount":      amountCents,
		"new_balance": newBalance,
		"tx_hash":     txHash,
	}).Warn("Debited balance due to failed x402 settlement")

	// Emit billing event for failed settlement
	emitBillingEvent(eventX402SettlementFailed, tenantID, "x402_nonce", txHash, &pb.BillingEvent{
		Amount:   float64(amountCents) / 100,
		Currency: "EUR",
		Status:   "failed",
	})
}

// parseHexInt64 parses a hex string to int64
func parseHexInt64(hexStr string) int64 {
	hexStr = strings.TrimPrefix(hexStr, "0x")
	if hexStr == "" {
		return 0
	}
	b, err := hex.DecodeString(padHexString(hexStr))
	if err != nil {
		return 0
	}
	var result int64
	for _, v := range b {
		result = result<<8 | int64(v)
	}
	return result
}

// padHexString pads a hex string to even length
func padHexString(s string) string {
	if len(s)%2 != 0 {
		return "0" + s
	}
	return s
}

// truncateTxHash returns a shortened tx hash for display
func truncateTxHash(txHash string) string {
	if len(txHash) > 16 {
		return txHash[:16] + "..."
	}
	return txHash
}
