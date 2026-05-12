package handlers

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// X402Reconciler monitors pending x402 settlements and confirms or fails them
// based on on-chain transaction receipts.
type X402Reconciler struct {
	db                  *sql.DB
	logger              logging.Logger
	stopCh              chan struct{}
	includeTestnets     bool
	submitTransfer      func(context.Context, *X402PaymentPayload, NetworkConfig) (string, error)
	recoveryWindowHours int
	reorgDepthBlocks    int
	rpcErrorLimit       int
	rpcErrorCounts      map[string]int
	rpcErrorMu          sync.Mutex
}

// PendingSettlement represents an x402 settlement awaiting confirmation
type PendingSettlement struct {
	ID          string
	Network     string
	TxHash      string
	TenantID    string
	AmountCents int64
	SettledAt   time.Time
	BlockNumber sql.NullInt64
}

// TransactionReceipt represents an Ethereum transaction receipt
type TransactionReceipt struct {
	Status      string `json:"status"`      // "0x1" for success, "0x0" for revert
	BlockNumber string `json:"blockNumber"` // hex
	GasUsed     string `json:"gasUsed"`     // hex
}

// NewX402Reconciler creates a new x402 settlement reconciler
func NewX402Reconciler(database *sql.DB, log logging.Logger, includeTestnets bool, submitter ...func(context.Context, *X402PaymentPayload, NetworkConfig) (string, error)) *X402Reconciler {
	var submitTransfer func(context.Context, *X402PaymentPayload, NetworkConfig) (string, error)
	if len(submitter) > 0 {
		submitTransfer = submitter[0]
	}
	return &X402Reconciler{
		db:                  database,
		logger:              log,
		stopCh:              make(chan struct{}),
		includeTestnets:     includeTestnets,
		submitTransfer:      submitTransfer,
		recoveryWindowHours: readEnvInt("X402_RECOVERY_WINDOW_HOURS", 168),
		reorgDepthBlocks:    readEnvInt("X402_REORG_DEPTH_BLOCKS", 50),
		rpcErrorLimit:       readEnvInt("X402_RPC_ERROR_LIMIT", 5),
		rpcErrorCounts:      make(map[string]int),
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
			r.reconcileSubmittingIntents(ctx)
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

// reconcileSubmittingIntents handles rows stuck in 'submitting': either the
// chain broadcast failed before recording, or it succeeded but the DB update
// for tx_hash did not land. authorizationState on the USDC contract is the
// oracle: unused = nothing on-chain, used = an unrecorded tx settled it.
func (r *X402Reconciler) reconcileSubmittingIntents(ctx context.Context) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, network, payer_address, nonce, tenant_id, amount_cents, settled_at, auth_payload
		FROM purser.x402_nonces
		WHERE status = 'submitting'
		AND settled_at < NOW() - INTERVAL '30 seconds'
		ORDER BY settled_at ASC
		LIMIT 50
	`)
	if err != nil {
		r.logger.WithError(err).Error("Failed to query submitting x402 intents")
		return
	}
	defer func() { _ = rows.Close() }()

	type intent struct {
		ID           string
		Network      string
		PayerAddress string
		Nonce        string
		TenantID     string
		AmountCents  int64
		SettledAt    time.Time
		AuthPayload  sql.NullString
	}
	var intents []intent
	for rows.Next() {
		var it intent
		if err := rows.Scan(&it.ID, &it.Network, &it.PayerAddress, &it.Nonce, &it.TenantID, &it.AmountCents, &it.SettledAt, &it.AuthPayload); err != nil {
			r.logger.WithError(err).Error("Failed to scan submitting intent")
			continue
		}
		intents = append(intents, it)
	}

	for _, it := range intents {
		network, ok := Networks[it.Network]
		if !ok {
			r.markFailed(ctx, it.ID, "unknown network")
			continue
		}
		if network.IsTestnet && !r.includeTestnets {
			continue
		}

		validBefore := r.parseAuthValidBefore(it.AuthPayload.String)
		expired := validBefore > 0 && time.Now().Unix() > validBefore

		used, callErr := r.callAuthorizationState(ctx, network, it.PayerAddress, it.Nonce)
		if callErr != nil {
			r.trackRPCError(it.Network, callErr, "", it.TenantID)
			r.logger.WithError(callErr).WithFields(logging.Fields{
				"nonce_id":  it.ID,
				"tenant_id": it.TenantID,
				"network":   it.Network,
			}).Warn("Failed to read authorizationState for submitting intent")
			continue
		}
		r.clearRPCError(it.Network)

		switch {
		case used:
			// The authorization was consumed on-chain but no tx_hash made it
			// to the row. USDC's authorizationState is a bool, so the txHash
			// is not deterministically recoverable from this signal alone.
			// Flag for manual reconciliation.
			r.markFailed(ctx, it.ID, "manual reconciliation required: authorization consumed without recorded tx_hash")
			emitBillingEvent(eventX402AccountingAnomaly, it.TenantID, "x402_nonce", it.ID, &pb.BillingEvent{
				Amount:   float64(it.AmountCents) / 100,
				Currency: billing.DefaultCurrency(),
				Status:   "authorization consumed without recorded tx_hash",
				Provider: it.Network,
			})
		case expired:
			// Authorization window passed without a successful broadcast;
			// safe to fail because no balance was credited.
			r.markFailed(ctx, it.ID, "authorization expired before broadcast")
		default:
			if r.submitTransfer == nil {
				r.logger.WithFields(logging.Fields{
					"nonce_id":  it.ID,
					"tenant_id": it.TenantID,
					"network":   it.Network,
					"age":       time.Since(it.SettledAt).String(),
				}).Warn("x402 intent stuck in submitting; no submitter configured")
				continue
			}
			acquired, err := r.claimSubmittingIntent(ctx, it.ID)
			if err != nil {
				r.logger.WithError(err).WithField("nonce_id", it.ID).Warn("Failed to claim x402 submit retry")
				continue
			}
			if !acquired {
				continue
			}
			var payload X402PaymentPayload
			if unmarshalErr := json.Unmarshal([]byte(it.AuthPayload.String), &payload); unmarshalErr != nil {
				r.markFailed(ctx, it.ID, "invalid stored x402 authorization payload")
				continue
			}
			txHash, err := r.submitTransfer(ctx, &payload, network)
			if err != nil {
				r.logger.WithError(err).WithFields(logging.Fields{
					"nonce_id":  it.ID,
					"tenant_id": it.TenantID,
					"network":   it.Network,
				}).Warn("Failed to resubmit x402 authorization")
				continue
			}
			if err := r.markSubmitted(ctx, it.ID, txHash); err != nil {
				r.logger.WithError(err).WithFields(logging.Fields{
					"nonce_id": it.ID,
					"tx_hash":  txHash,
				}).Error("Resubmitted x402 authorization but failed to record tx hash")
				continue
			}
			if err := r.creditBalance(ctx, it.TenantID, it.AmountCents, it.ID, txHash); err != nil {
				r.logger.WithError(err).WithFields(logging.Fields{
					"nonce_id":  it.ID,
					"tenant_id": it.TenantID,
					"tx_hash":   txHash,
				}).Error("Resubmitted x402 authorization but failed to credit tenant")
			}
		}
	}
}

func (r *X402Reconciler) claimSubmittingIntent(ctx context.Context, id string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE purser.x402_nonces
		SET last_submit_attempt_at = NOW()
		WHERE id = $1
		  AND status = 'submitting'
		  AND (last_submit_attempt_at IS NULL OR last_submit_attempt_at < NOW() - INTERVAL '2 minutes')
	`, id)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

func (r *X402Reconciler) parseAuthValidBefore(rawJSON string) int64 {
	if rawJSON == "" {
		return 0
	}
	var p struct {
		Payload struct {
			Authorization struct {
				ValidBefore string `json:"validBefore"`
			} `json:"authorization"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &p); err != nil {
		return 0
	}
	v, err := strconv.ParseInt(p.Payload.Authorization.ValidBefore, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func (r *X402Reconciler) callAuthorizationState(ctx context.Context, network NetworkConfig, payer, nonce string) (bool, error) {
	rpcEndpoint := network.GetRPCEndpointWithDefault()
	if rpcEndpoint == "" {
		return false, fmt.Errorf("no RPC endpoint for network %s", network.Name)
	}

	methodID := keccak256([]byte("authorizationState(address,bytes32)"))[0:4]
	callData := methodID
	callData = append(callData, padAddress(payer)...)
	callData = append(callData, padBytes32(nonce)...)

	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"method":  "eth_call",
		"params": []any{
			map[string]string{
				"to":   network.USDCContract,
				"data": "0x" + hex.EncodeToString(callData),
			},
			"latest",
		},
		"id": 1,
	}
	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", rpcEndpoint, strings.NewReader(string(reqJSON)))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}

	var rpcResp struct {
		Result string           `json:"result"`
		Error  *json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return false, err
	}
	if rpcResp.Error != nil {
		return false, fmt.Errorf("RPC error: %s", string(*rpcResp.Error))
	}
	return rpcResp.Result != "0x0000000000000000000000000000000000000000000000000000000000000000", nil
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
	defer func() { _ = rows.Close() }()

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
		r.trackRPCError(s.Network, err, s.TxHash, s.TenantID)
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
			r.debitBalance(ctx, s.TenantID, s.AmountCents, s.ID, s.TxHash)
		}
		return
	}

	r.clearRPCError(s.Network)

	if receipt == nil {
		// Transaction still pending
		if time.Since(s.SettledAt) > 2*time.Minute {
			r.logger.WithFields(logging.Fields{
				"tx_hash":   s.TxHash,
				"tenant_id": s.TenantID,
			}).Error("X402 settlement timed out - no receipt after 2 minutes")
			r.markFailed(ctx, s.ID, "timeout - no receipt after 2 minutes")
			r.debitBalance(ctx, s.TenantID, s.AmountCents, s.ID, s.TxHash)
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

		if err := r.creditBalance(ctx, s.TenantID, s.AmountCents, s.ID, s.TxHash); err != nil {
			r.logger.WithError(err).WithFields(logging.Fields{
				"tx_hash":   s.TxHash,
				"tenant_id": s.TenantID,
				"nonce_id":  s.ID,
			}).Error("Failed to ensure x402 credit before confirming settlement")
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
			Currency: billing.DefaultCurrency(),
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
		r.debitBalance(ctx, s.TenantID, s.AmountCents, s.ID, s.TxHash)
	}
}

func (r *X402Reconciler) reconcileFailedTimeouts(ctx context.Context) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, network, tx_hash, tenant_id, amount_cents, settled_at
		FROM purser.x402_nonces
		WHERE status = 'failed'
		AND (failure_reason LIKE 'timeout%' OR failure_reason = 'transaction reorged or missing')
		AND settled_at > NOW() - ($1 * INTERVAL '1 hour')
		ORDER BY settled_at ASC
		LIMIT 50
	`, r.recoveryWindowHours)
	if err != nil {
		r.logger.WithError(err).Error("Failed to query failed x402 settlements")
		return
	}
	defer func() { _ = rows.Close() }()

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
			if err != nil {
				r.trackRPCError(s.Network, err, s.TxHash, s.TenantID)
			}
			continue
		}

		r.clearRPCError(s.Network)

		blockNum := parseHexInt64(receipt.BlockNumber)
		gasUsed := parseHexInt64(receipt.GasUsed)
		confirmed, err := r.hasRequiredConfirmations(ctx, network, blockNum)
		if err != nil || !confirmed {
			continue
		}

		// Only re-credit if we previously debited the tenant due to timeout.
		// Otherwise a transient debit failure would result in double-credit.
		var reversalExists bool
		err = r.db.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1
				FROM purser.balance_transactions
				WHERE tenant_id = $1
				  AND reference_id = $2
				  AND reference_type = 'x402_failed'
				  AND transaction_type = 'reversal'
			)
		`, s.TenantID, s.ID).Scan(&reversalExists)
		if err != nil {
			r.logger.WithError(err).WithField("tenant_id", s.TenantID).Error("Failed to check timeout reversal before re-credit")
			continue
		}
		if !reversalExists {
			r.logger.WithFields(logging.Fields{
				"tenant_id": s.TenantID,
				"tx_hash":   s.TxHash,
			}).Warn("Skipping late-settlement re-credit: no prior reversal recorded")
			emitBillingEvent(eventX402AccountingAnomaly, s.TenantID, "x402_nonce", s.TxHash, &pb.BillingEvent{
				Amount:   float64(s.AmountCents) / 100,
				Currency: "EUR",
				Status:   "missing reversal for late-settlement credit",
				Provider: s.Network,
			})
			continue
		}
		if err := r.creditBalance(ctx, s.TenantID, s.AmountCents, s.ID, s.TxHash); err != nil {
			r.logger.WithError(err).WithField("tenant_id", s.TenantID).Error("Failed to re-credit balance after late settlement")
			continue
		}

		emitBillingEvent(eventX402LateRecovery, s.TenantID, "x402_nonce", s.TxHash, &pb.BillingEvent{
			Amount:   float64(s.AmountCents) / 100,
			Currency: "EUR",
			Status:   "late settlement recovered",
			Provider: s.Network,
		})

		r.markConfirmed(ctx, s.ID, blockNum, gasUsed)
		emitBillingEvent(eventX402SettlementConfirm, s.TenantID, "x402_nonce", s.TxHash, &pb.BillingEvent{
			Amount:   float64(s.AmountCents) / 100,
			Currency: billing.DefaultCurrency(),
			Status:   "confirmed",
		})
	}
}

func (r *X402Reconciler) reconcileConfirmedSettlements(ctx context.Context) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, network, tx_hash, tenant_id, amount_cents, settled_at, block_number
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
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var s PendingSettlement
		if err := rows.Scan(&s.ID, &s.Network, &s.TxHash, &s.TenantID, &s.AmountCents, &s.SettledAt, &s.BlockNumber); err != nil {
			r.logger.WithError(err).Error("Failed to scan confirmed settlement")
			continue
		}

		network, ok := Networks[s.Network]
		if !ok {
			continue
		}

		if !s.BlockNumber.Valid || s.BlockNumber.Int64 == 0 {
			r.logger.WithFields(logging.Fields{
				"tx_hash":   s.TxHash,
				"tenant_id": s.TenantID,
			}).Warn("Confirmed settlement missing block number, skipping reorg check")
			continue
		}

		receipt, err := r.getTransactionReceipt(ctx, network, s.TxHash)
		if err != nil {
			r.trackRPCError(s.Network, err, s.TxHash, s.TenantID)
			continue
		}

		r.clearRPCError(s.Network)

		if receipt == nil {
			latest, err := r.getLatestBlockNumber(ctx, network)
			if err != nil {
				r.trackRPCError(s.Network, err, s.TxHash, s.TenantID)
				continue
			}
			if latest-s.BlockNumber.Int64 < int64(r.reorgDepthBlocks) {
				continue
			}

			var creditExists bool
			qErr := r.db.QueryRowContext(ctx, `
				SELECT EXISTS(
					SELECT 1
					FROM purser.balance_transactions
					WHERE tenant_id = $1
					  AND reference_id = $2
					  AND reference_type = 'x402_payment'
					  AND transaction_type = 'topup'
				)
			`, s.TenantID, s.ID).Scan(&creditExists)
			if qErr != nil {
				r.logger.WithError(qErr).WithFields(logging.Fields{
					"tenant_id": s.TenantID,
					"tx_hash":   s.TxHash,
				}).Error("Failed to verify credit existence before reorg debit")
				continue
			}

			r.markFailed(ctx, s.ID, "transaction reorged or missing")

			if !creditExists {
				r.logger.WithFields(logging.Fields{
					"tenant_id": s.TenantID,
					"tx_hash":   s.TxHash,
				}).Warn("Skipping reorg debit: no original credit found")
				continue
			}

			r.debitBalance(ctx, s.TenantID, s.AmountCents, s.ID, s.TxHash)
			emitBillingEvent(eventX402ReorgDetected, s.TenantID, "x402_nonce", s.TxHash, &pb.BillingEvent{
				Amount:   float64(s.AmountCents) / 100,
				Currency: "EUR",
				Status:   "receipt missing after reorg depth",
				Provider: s.Network,
			})
			continue
		}

		if receipt.Status != "0x1" {
			var creditExists bool
			qErr := r.db.QueryRowContext(ctx, `
				SELECT EXISTS(
					SELECT 1
					FROM purser.balance_transactions
					WHERE tenant_id = $1
					  AND reference_id = $2
					  AND reference_type = 'x402_payment'
					  AND transaction_type = 'topup'
				)
			`, s.TenantID, s.ID).Scan(&creditExists)
			if qErr != nil {
				r.logger.WithError(qErr).WithFields(logging.Fields{
					"tenant_id": s.TenantID,
					"tx_hash":   s.TxHash,
				}).Error("Failed to verify credit existence before reorg debit")
				continue
			}

			r.markFailed(ctx, s.ID, "transaction reverted on-chain")

			if !creditExists {
				r.logger.WithFields(logging.Fields{
					"tenant_id": s.TenantID,
					"tx_hash":   s.TxHash,
				}).Warn("Skipping reorg debit: no original credit found")
				continue
			}

			r.debitBalance(ctx, s.TenantID, s.AmountCents, s.ID, s.TxHash)
			emitBillingEvent(eventX402ReorgDetected, s.TenantID, "x402_nonce", s.TxHash, &pb.BillingEvent{
				Amount:   float64(s.AmountCents) / 100,
				Currency: "EUR",
				Status:   "transaction reverted on-chain",
				Provider: s.Network,
			})
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

	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"method":  "eth_blockNumber",
		"params":  []any{},
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
	defer func() { _ = resp.Body.Close() }()

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

func (r *X402Reconciler) markSubmitted(ctx context.Context, id, txHash string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE purser.x402_nonces
		SET tx_hash = $2, submitted_at = NOW(), status = 'pending'
		WHERE id = $1 AND status = 'submitting'
	`, id, txHash)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("intent %s not in submitting state", id)
	}
	return nil
}

func (r *X402Reconciler) creditBalance(ctx context.Context, tenantID string, amountCents int64, nonceID, txHash string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	currency := billing.DefaultCurrency()

	var existingBalanceAfter int64
	err = tx.QueryRowContext(ctx, `
		SELECT balance_after_cents FROM purser.balance_transactions
		WHERE tenant_id = $1 AND reference_type = 'x402_payment' AND reference_id = $2
	`, tenantID, nonceID).Scan(&existingBalanceAfter)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency, updated_at)
		VALUES ($1, 0, $2, NOW())
		ON CONFLICT (tenant_id, currency) DO NOTHING
	`, tenantID, currency)
	if err != nil {
		return err
	}

	var balance int64
	err = tx.QueryRowContext(ctx, `
		SELECT balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2
		FOR UPDATE
	`, tenantID, currency).Scan(&balance)
	if err != nil {
		return err
	}

	newBalance := balance + amountCents

	_, err = tx.ExecContext(ctx, `
		UPDATE purser.prepaid_balances
		SET balance_cents = $1, updated_at = NOW()
		WHERE tenant_id = $2 AND currency = $3
	`, newBalance, tenantID, currency)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO purser.balance_transactions (
			id, tenant_id, amount_cents, balance_after_cents,
			transaction_type, description, reference_id, reference_type, created_at
		) VALUES ($1, $2, $3, $4, 'topup', $5, $6, 'x402_payment', NOW())
	`, uuid.New().String(), tenantID, amountCents, newBalance,
		fmt.Sprintf("x402 settlement recovered (%s)", truncateTxHash(txHash)), nonceID)
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

	reqBody := map[string]any{
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
	defer func() { _ = resp.Body.Close() }()

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
func (r *X402Reconciler) debitBalance(ctx context.Context, tenantID string, amountCents int64, nonceID, txHash string) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		r.logger.WithError(err).Error("Failed to begin transaction for balance debit")
		return
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	currency := billing.DefaultCurrency()

	var creditExists bool
	err = tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM purser.balance_transactions
			WHERE tenant_id = $1 AND reference_id = $2
			  AND reference_type = 'x402_payment' AND transaction_type = 'topup'
		)
	`, tenantID, nonceID).Scan(&creditExists)
	if err != nil {
		r.logger.WithError(err).Error("Failed to check original x402 credit before debit")
		return
	}
	if !creditExists {
		r.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"nonce_id":  nonceID,
			"tx_hash":   txHash,
		}).Warn("Skipping x402 debit: no original credit found")
		return
	}

	var reversalExists bool
	err = tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM purser.balance_transactions
			WHERE tenant_id = $1 AND reference_id = $2
			  AND reference_type = 'x402_failed' AND transaction_type = 'reversal'
		)
	`, tenantID, nonceID).Scan(&reversalExists)
	if err != nil {
		r.logger.WithError(err).Error("Failed to check existing x402 reversal before debit")
		return
	}
	if reversalExists {
		return
	}

	var balance int64
	err = tx.QueryRowContext(ctx, `
		SELECT balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2
		FOR UPDATE
	`, tenantID, currency).Scan(&balance)
	if err != nil {
		r.logger.WithError(err).Error("Failed to get current balance for debit")
		return
	}

	// Deduct from balance (can go negative - accumulate debt, per existing pattern)
	newBalance := balance - amountCents

	// Update balance
	_, err = tx.ExecContext(ctx, `
		UPDATE purser.prepaid_balances
		SET balance_cents = $1, updated_at = NOW()
		WHERE tenant_id = $2 AND currency = $3
	`, newBalance, tenantID, currency)
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
		fmt.Sprintf("x402 settlement failed: %s", truncateTxHash(txHash)), nonceID)
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
		Currency: billing.DefaultCurrency(),
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

func readEnvInt(key string, defaultValue int) int {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return defaultValue
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return defaultValue
	}
	return parsed
}

func (r *X402Reconciler) trackRPCError(network string, err error, txHash, tenantID string) {
	if r.rpcErrorLimit <= 0 {
		return
	}
	r.rpcErrorMu.Lock()
	r.rpcErrorCounts[network]++
	count := r.rpcErrorCounts[network]
	r.rpcErrorMu.Unlock()

	if count == r.rpcErrorLimit {
		r.logger.WithError(err).WithFields(logging.Fields{
			"network": network,
			"tx_hash": txHash,
		}).Warn("X402 RPC error limit reached")
		emitBillingEvent(eventX402RPCError, tenantID, "x402_network", network, &pb.BillingEvent{
			Status:   "rpc error limit reached",
			Provider: network,
		})
	}
}

func (r *X402Reconciler) clearRPCError(network string) {
	r.rpcErrorMu.Lock()
	r.rpcErrorCounts[network] = 0
	r.rpcErrorMu.Unlock()
}
