package handlers

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/google/uuid"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// X402Reconciler monitors pending x402 settlements and confirms or fails them
// based on on-chain transaction receipts.
type X402Reconciler struct {
	db                  *sql.DB
	logger              logging.Logger
	stopCh              chan struct{}
	includeTestnets     bool
	rebroadcastTransfer func(context.Context, string, NetworkConfig) (string, error)
	finalizeSettlement  func(context.Context, SettlementRow) string
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
	ClientIP    string
	AuthPayload string
}

// TransactionReceipt represents an Ethereum transaction receipt
type TransactionReceipt struct {
	Status      string           `json:"status"`      // "0x1" for success, "0x0" for revert
	BlockNumber string           `json:"blockNumber"` // hex
	BlockHash   string           `json:"blockHash"`
	GasUsed     string           `json:"gasUsed"` // hex
	Logs        []TransactionLog `json:"logs"`
}

type TransactionLog struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

// NewX402Reconciler creates a new x402 settlement reconciler
func NewX402Reconciler(database *sql.DB, log logging.Logger, includeTestnets bool, handlers ...*X402Handler) *X402Reconciler {
	var rebroadcastTransfer func(context.Context, string, NetworkConfig) (string, error)
	var finalizeSettlement func(context.Context, SettlementRow) string
	if len(handlers) > 0 && handlers[0] != nil {
		if handlers[0].facilitatorProvider == "self" {
			rebroadcastTransfer = handlers[0].rebroadcastPreparedSettlementAttempt
		}
		finalizeSettlement = handlers[0].finalizeConfirmedSettlementEffects
	}
	return &X402Reconciler{
		db:                  database,
		logger:              log,
		stopCh:              make(chan struct{}),
		includeTestnets:     includeTestnets,
		rebroadcastTransfer: rebroadcastTransfer,
		finalizeSettlement:  finalizeSettlement,
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
	rows, err := purserdb.New(r.db).ListSubmittingX402Intents(ctx)
	if err != nil {
		r.logger.WithError(err).Error("Failed to query submitting x402 intents")
		return
	}
	type intent struct {
		ID           string
		Network      string
		PayerAddress string
		Nonce        string
		TenantID     string
		AmountCents  int64
		SettledAt    time.Time
		AuthPayload  string
		TxHash       string
	}
	intents := make([]intent, 0, len(rows))
	for _, row := range rows {
		intents = append(intents, intent{
			ID: row.ID, Network: row.Network, PayerAddress: row.PayerAddress, Nonce: row.Nonce,
			TenantID: row.TenantID, AmountCents: row.AmountCents, SettledAt: row.SettledAt.Time,
			AuthPayload: row.AuthPayload, TxHash: row.TxHash,
		})
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

		validBefore := r.parseAuthValidBefore(it.AuthPayload)
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
		case used && it.TxHash != "":
			if err := purserdb.New(r.db).AdvanceConsumedX402Attempt(ctx, purserdb.AdvanceConsumedX402AttemptParams{
				TxHash: it.TxHash, ID: it.ID,
			}); err != nil {
				r.logger.WithError(err).WithField("nonce_id", it.ID).Warn("Failed to advance consumed durable x402 attempt")
			}
		case used:
			// The authorization was consumed on-chain but no tx_hash made it
			// to the row. USDC's authorizationState is a bool, so the txHash
			// is not deterministically recoverable from this signal alone.
			// Flag for manual reconciliation.
			r.markFailed(ctx, it.ID, "manual reconciliation required: authorization consumed without recorded tx_hash")
			recordCryptoAccountingAnomaly(ctx, r.db, r.logger, it.TenantID,
				"x402_consumed_authorization_missing_transaction", it.Network, "x402_nonce", it.ID,
				it.AmountCents, "authorization consumed without recorded tx_hash", map[string]any{
					"payer_address": it.PayerAddress, "nonce": it.Nonce,
				})
			emitBillingEvent(r.db, r.logger, eventX402AccountingAnomaly, it.TenantID, "x402_nonce", it.ID, &ipcpb.BillingEvent{
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
			if r.rebroadcastTransfer == nil {
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
			txHash, err := r.rebroadcastTransfer(ctx, it.ID, network)
			if err != nil {
				r.logger.WithError(err).WithFields(logging.Fields{
					"nonce_id":  it.ID,
					"tenant_id": it.TenantID,
					"network":   it.Network,
				}).Warn("Failed to resubmit x402 authorization")
				continue
			}
			r.logger.WithFields(logging.Fields{"nonce_id": it.ID, "tx_hash": txHash}).Info("Rebroadcast durable x402 settlement attempt")
			// A successful broadcast is not spendable credit. The pending
			// reconciler will wait for the canonical receipt and confirmation
			// depth before atomically confirming and crediting this settlement.
		}
	}
}

func (r *X402Reconciler) claimSubmittingIntent(ctx context.Context, id string) (bool, error) {
	rows, err := purserdb.New(r.db).ClaimSubmittingX402Intent(ctx, id)
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
	payerBytes, err := padAddress(payer)
	if err != nil {
		return false, fmt.Errorf("payer: %w", err)
	}
	nonceBytes, err := padBytes32(nonce)
	if err != nil {
		return false, fmt.Errorf("nonce: %w", err)
	}
	callData := methodID
	callData = append(callData, payerBytes...)
	callData = append(callData, nonceBytes...)

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
	rows, err := purserdb.New(r.db).ListPendingX402Settlements(ctx)
	if err != nil {
		r.logger.WithError(err).Error("Failed to query pending x402 settlements")
		return
	}
	settlements := make([]PendingSettlement, 0, len(rows))
	for _, row := range rows {
		settlements = append(settlements, PendingSettlement{
			ID: row.ID, Network: row.Network, TxHash: row.TxHash.String, TenantID: row.TenantID,
			AmountCents: row.AmountCents, SettledAt: row.SettledAt.Time, ClientIP: row.ClientIp, AuthPayload: row.AuthPayload,
		})
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

		// A provider timeout is an unknown outcome. Never mark a broadcast
		// failed (or submit a replacement) solely because receipt lookup is
		// unavailable; keep reconciling the durable tx hash.
		if time.Since(s.SettledAt) > 2*time.Minute {
			r.logger.WithFields(logging.Fields{
				"tx_hash":   s.TxHash,
				"tenant_id": s.TenantID,
				"age":       time.Since(s.SettledAt).String(),
			}).Warn("X402 settlement still pending beyond expected confirmation window")
		}
		return
	}

	r.clearRPCError(s.Network)

	if receipt == nil {
		// Transaction still pending. Age alone cannot distinguish a dropped
		// transaction from a successful submission whose receipt is temporarily
		// unavailable, so this remains pending for deterministic recovery.
		if time.Since(s.SettledAt) > 2*time.Minute {
			r.logger.WithFields(logging.Fields{
				"tx_hash":   s.TxHash,
				"tenant_id": s.TenantID,
			}).Warn("X402 settlement has no receipt beyond expected confirmation window")
		}
		return
	}

	// Check receipt status
	if receipt.Status == "0x1" {
		blockNum := parseHexInt64(receipt.BlockNumber)
		gasUsed := parseHexInt64(receipt.GasUsed)
		confirmed, err := r.hasReachedFinalizedHead(ctx, network, blockNum)
		if err != nil {
			r.logger.WithError(err).WithFields(logging.Fields{
				"tx_hash": s.TxHash,
				"network": s.Network,
			}).Warn("Failed to read consensus-labelled finalized head")
			return
		}

		if !confirmed {
			r.updatePendingReceipt(ctx, s.ID, blockNum, gasUsed)
			return
		}
		auth, authErr := authorizationFromStoredPayload(s.AuthPayload)
		if authErr != nil || validateX402TransferReceipt(receipt, network, auth) != nil {
			r.logger.WithFields(logging.Fields{"tx_hash": s.TxHash, "tenant_id": s.TenantID}).Error("X402 receipt does not contain the authorized USDC transfer")
			r.markFailed(ctx, s.ID, "confirmed transaction does not match authorization")
			return
		}

		if _, err := confirmAndCreditX402Settlement(ctx, r.db, s.TenantID, s.AmountCents, s.ID, s.TxHash, blockNum, gasUsed); err != nil {
			r.logger.WithError(err).WithFields(logging.Fields{
				"tx_hash":   s.TxHash,
				"tenant_id": s.TenantID,
				"nonce_id":  s.ID,
			}).Error("Failed to ensure x402 credit before confirming settlement")
			return
		}
		if r.finalizeSettlement != nil {
			r.finalizeSettlement(ctx, SettlementRow{
				ID:          s.ID,
				Network:     s.Network,
				TxHash:      s.TxHash,
				TenantID:    s.TenantID,
				AmountCents: s.AmountCents,
				Status:      "confirmed",
				ClientIP:    s.ClientIP,
			})
		}
		r.logger.WithFields(logging.Fields{
			"tx_hash":      s.TxHash,
			"tenant_id":    s.TenantID,
			"block_number": blockNum,
			"gas_used":     gasUsed,
		}).Info("X402 settlement confirmed on-chain")

	} else {
		// Reverted
		r.logger.WithFields(logging.Fields{
			"tx_hash":   s.TxHash,
			"tenant_id": s.TenantID,
			"status":    receipt.Status,
		}).Error("X402 settlement reverted on-chain")
		r.markFailed(ctx, s.ID, "transaction reverted on-chain")
		if r.debitBalance(ctx, s.TenantID, s.AmountCents, s.ID, s.TxHash) {
			r.reverseSettlementRollup(ctx, s)
		}
	}
}

func (r *X402Reconciler) reconcileFailedTimeouts(ctx context.Context) {
	rows, err := purserdb.New(r.db).ListRecoverableFailedX402Settlements(ctx, r.recoveryWindowHours)
	if err != nil {
		r.logger.WithError(err).Error("Failed to query failed x402 settlements")
		return
	}
	for _, row := range rows {
		s := PendingSettlement{ID: row.ID, Network: row.Network, TxHash: row.TxHash.String,
			TenantID: row.TenantID, AmountCents: row.AmountCents, AuthPayload: row.AuthPayload}

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
		confirmed, err := r.hasReachedFinalizedHead(ctx, network, blockNum)
		if err != nil || !confirmed {
			continue
		}
		auth, authErr := authorizationFromStoredPayload(s.AuthPayload)
		if authErr != nil || validateX402TransferReceipt(receipt, network, auth) != nil {
			r.logger.WithFields(logging.Fields{"tx_hash": s.TxHash, "tenant_id": s.TenantID}).Error("Late x402 receipt does not contain the authorized USDC transfer")
			continue
		}

		// Only re-credit if we previously debited the tenant due to timeout.
		// Otherwise a transient debit failure would result in double-credit.
		reversalExists, err := purserdb.New(r.db).CryptoReversalBalanceTransactionExists(ctx, purserdb.CryptoReversalBalanceTransactionExistsParams{
			TenantID: s.TenantID, ReferenceType: sql.NullString{String: "x402_failed", Valid: true}, ReferenceID: s.ID,
		})
		if err != nil {
			r.logger.WithError(err).WithField("tenant_id", s.TenantID).Error("Failed to check timeout reversal before re-credit")
			continue
		}
		if !reversalExists {
			r.logger.WithFields(logging.Fields{
				"tenant_id": s.TenantID,
				"tx_hash":   s.TxHash,
			}).Warn("Skipping late-settlement re-credit: no prior reversal recorded")
			recordCryptoAccountingAnomaly(ctx, r.db, r.logger, s.TenantID,
				"x402_late_settlement_missing_reversal", s.Network, "x402_nonce", s.ID,
				s.AmountCents, "missing reversal for late-settlement credit", map[string]any{
					"tx_hash": s.TxHash,
				})
			emitBillingEvent(r.db, r.logger, eventX402AccountingAnomaly, s.TenantID, "x402_nonce", s.TxHash, &ipcpb.BillingEvent{
				Amount:   float64(s.AmountCents) / 100,
				Currency: "EUR",
				Status:   "missing reversal for late-settlement credit",
				Provider: s.Network,
			})
			continue
		}
		if err := r.recoverReversedBalance(ctx, s.TenantID, s.AmountCents, s.ID, s.TxHash); err != nil {
			r.logger.WithError(err).WithField("tenant_id", s.TenantID).Error("Failed to re-credit balance after late settlement")
			continue
		}

		emitBillingEvent(r.db, r.logger, eventX402LateRecovery, s.TenantID, "x402_nonce", s.TxHash, &ipcpb.BillingEvent{
			Amount:   float64(s.AmountCents) / 100,
			Currency: "EUR",
			Status:   "late settlement recovered",
			Provider: s.Network,
		})

		r.markConfirmed(ctx, s.ID, blockNum, gasUsed)
		emitBillingEvent(r.db, r.logger, eventX402SettlementConfirm, s.TenantID, "x402_nonce", s.TxHash, &ipcpb.BillingEvent{
			Amount:   float64(s.AmountCents) / 100,
			Currency: billing.DefaultCurrency(),
			Status:   "confirmed",
		})
	}
}

func (r *X402Reconciler) reconcileConfirmedSettlements(ctx context.Context) {
	rows, err := purserdb.New(r.db).ListConfirmedX402SettlementsForReconciliation(ctx)
	if err != nil {
		r.logger.WithError(err).Error("Failed to query confirmed x402 settlements")
		return
	}
	for _, row := range rows {
		s := PendingSettlement{ID: row.ID, Network: row.Network, TxHash: row.TxHash.String,
			TenantID: row.TenantID, AmountCents: row.AmountCents, SettledAt: row.SettledAt.Time,
			BlockNumber: row.BlockNumber, ClientIP: row.ClientIp}

		network, ok := Networks[s.Network]
		if !ok {
			continue
		}
		if r.finalizeSettlement != nil {
			r.finalizeSettlement(ctx, SettlementRow{
				ID:          s.ID,
				Network:     s.Network,
				TxHash:      s.TxHash,
				TenantID:    s.TenantID,
				AmountCents: s.AmountCents,
				Status:      "confirmed",
				ClientIP:    s.ClientIP,
			})
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

			creditExists, qErr := purserdb.New(r.db).CryptoReversalBalanceTransactionExists(ctx, purserdb.CryptoReversalBalanceTransactionExistsParams{
				TenantID: s.TenantID, ReferenceType: sql.NullString{String: "x402_payment", Valid: true}, ReferenceID: s.ID,
			})
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

			if r.debitBalance(ctx, s.TenantID, s.AmountCents, s.ID, s.TxHash) {
				r.reverseSettlementRollup(ctx, s)
			}
			emitBillingEvent(r.db, r.logger, eventX402ReorgDetected, s.TenantID, "x402_nonce", s.TxHash, &ipcpb.BillingEvent{
				Amount:   float64(s.AmountCents) / 100,
				Currency: "EUR",
				Status:   "receipt missing after reorg depth",
				Provider: s.Network,
			})
			continue
		}

		if receipt.Status != "0x1" {
			creditExists, qErr := purserdb.New(r.db).CryptoReversalBalanceTransactionExists(ctx, purserdb.CryptoReversalBalanceTransactionExistsParams{
				TenantID: s.TenantID, ReferenceType: sql.NullString{String: "x402_payment", Valid: true}, ReferenceID: s.ID,
			})
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

			if r.debitBalance(ctx, s.TenantID, s.AmountCents, s.ID, s.TxHash) {
				r.reverseSettlementRollup(ctx, s)
			}
			emitBillingEvent(r.db, r.logger, eventX402ReorgDetected, s.TenantID, "x402_nonce", s.TxHash, &ipcpb.BillingEvent{
				Amount:   float64(s.AmountCents) / 100,
				Currency: "EUR",
				Status:   "transaction reverted on-chain",
				Provider: s.Network,
			})
		}
	}
}

func (r *X402Reconciler) hasReachedFinalizedHead(ctx context.Context, network NetworkConfig, blockNum int64) (bool, error) {
	if blockNum == 0 {
		return false, nil
	}

	finality, err := GetFinalityHead(ctx, NewRPCClient(), network)
	if err != nil {
		return false, err
	}
	return finality.Number >= blockNum, nil
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
	err := purserdb.New(r.db).UpdatePendingX402Receipt(ctx, purserdb.UpdatePendingX402ReceiptParams{
		BlockNumber: sql.NullInt64{Int64: blockNumber, Valid: true}, GasUsed: sql.NullInt64{Int64: gasUsed, Valid: true}, ID: id,
	})
	if err != nil {
		r.logger.WithError(err).WithField("id", id).Error("Failed to update pending receipt metadata")
	}
}

// recoverReversedBalance restores a credit that has an x402_failed reversal.
// It uses a distinct unique ledger reference so the original x402_payment row
// does not suppress recovery and concurrent/crash retries cannot double-credit.
func (r *X402Reconciler) recoverReversedBalance(ctx context.Context, tenantID string, amountCents int64, nonceID, txHash string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	currency := billing.DefaultCurrency()

	queries := purserdb.New(tx)
	recoveryExists, err := queries.CryptoReversalBalanceTransactionExists(ctx, purserdb.CryptoReversalBalanceTransactionExistsParams{
		TenantID: tenantID, ReferenceType: sql.NullString{String: "x402_recovery", Valid: true}, ReferenceID: nonceID,
	})
	if err != nil {
		return err
	}
	if recoveryExists {
		return nil
	}

	err = queries.EnsurePrepaidBalanceRow(ctx, purserdb.EnsurePrepaidBalanceRowParams{TenantID: tenantID, Currency: currency})
	if err != nil {
		return err
	}

	newBalance, err := queries.AddPrepaidBalance(ctx, purserdb.AddPrepaidBalanceParams{
		AmountCents: amountCents, TenantID: tenantID, Currency: currency,
	})
	if err != nil {
		return err
	}

	err = queries.InsertBalanceTransaction(ctx, purserdb.InsertBalanceTransactionParams{
		ID: uuid.New(), TenantID: tenantID, AmountCents: amountCents, BalanceAfterCents: newBalance,
		TransactionType: "topup", Description: sql.NullString{String: fmt.Sprintf("x402 settlement recovered (%s)", truncateTxHash(txHash)), Valid: true},
		ReferenceID: sql.NullString{String: nonceID, Valid: true}, ReferenceType: sql.NullString{String: "x402_recovery", Valid: true},
		CreatedAt: sql.NullTime{Time: time.Now(), Valid: true},
	})
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
	err := purserdb.New(r.db).MarkX402SettlementConfirmed(ctx, purserdb.MarkX402SettlementConfirmedParams{
		BlockNumber: sql.NullInt64{Int64: blockNumber, Valid: true}, GasUsed: sql.NullInt64{Int64: gasUsed, Valid: true}, ID: id,
	})
	if err != nil {
		r.logger.WithError(err).WithField("id", id).Error("Failed to mark settlement as confirmed")
	}
}

// markFailed updates the settlement status to failed
func (r *X402Reconciler) markFailed(ctx context.Context, id, reason string) {
	err := purserdb.New(r.db).MarkX402SettlementFailed(ctx, purserdb.MarkX402SettlementFailedParams{
		Reason: sql.NullString{String: reason, Valid: reason != ""}, ID: id,
	})
	if err != nil {
		r.logger.WithError(err).WithField("id", id).Error("Failed to mark settlement as failed")
	}
}

// debitBalance reverses the balance credit for a failed settlement
func (r *X402Reconciler) debitBalance(ctx context.Context, tenantID string, amountCents int64, nonceID, txHash string) bool {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		r.logger.WithError(err).Error("Failed to begin transaction for balance debit")
		return false
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	currency := billing.DefaultCurrency()

	queries := purserdb.New(tx)
	creditExists, err := queries.CryptoReversalBalanceTransactionExists(ctx, purserdb.CryptoReversalBalanceTransactionExistsParams{
		TenantID: tenantID, ReferenceType: sql.NullString{String: "x402_payment", Valid: true}, ReferenceID: nonceID,
	})
	if err != nil {
		r.logger.WithError(err).Error("Failed to check original x402 credit before debit")
		return false
	}
	if !creditExists {
		r.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"nonce_id":  nonceID,
			"tx_hash":   txHash,
		}).Warn("Skipping x402 debit: no original credit found")
		return false
	}

	reversalExists, err := queries.CryptoReversalBalanceTransactionExists(ctx, purserdb.CryptoReversalBalanceTransactionExistsParams{
		TenantID: tenantID, ReferenceType: sql.NullString{String: "x402_failed", Valid: true}, ReferenceID: nonceID,
	})
	if err != nil {
		r.logger.WithError(err).Error("Failed to check existing x402 reversal before debit")
		return false
	}
	if reversalExists {
		return true
	}

	newBalance, err := queries.AddPrepaidBalance(ctx, purserdb.AddPrepaidBalanceParams{
		AmountCents: -amountCents, TenantID: tenantID, Currency: currency,
	})
	if err != nil {
		r.logger.WithError(err).Error("Failed to get current balance for debit")
		return false
	}

	// Record reversal transaction
	err = queries.InsertBalanceTransaction(ctx, purserdb.InsertBalanceTransactionParams{
		ID: uuid.New(), TenantID: tenantID, AmountCents: -amountCents, BalanceAfterCents: newBalance,
		TransactionType: "reversal", Description: sql.NullString{String: fmt.Sprintf("x402 settlement failed: %s", truncateTxHash(txHash)), Valid: true},
		ReferenceID: sql.NullString{String: nonceID, Valid: true}, ReferenceType: sql.NullString{String: "x402_failed", Valid: true},
		CreatedAt: sql.NullTime{Time: time.Now(), Valid: true},
	})
	if err != nil {
		r.logger.WithError(err).Error("Failed to record reversal transaction")
		return false
	}

	err = queries.InsertX402ReversalCreditNote(ctx, purserdb.InsertX402ReversalCreditNoteParams{
		NonceID: nonceID, TxHash: txHash, TenantID: tenantID,
	})
	if err != nil {
		r.logger.WithError(err).Error("Failed to issue x402 reversal credit note")
		return false
	}

	if err := tx.Commit(); err != nil {
		r.logger.WithError(err).Error("Failed to commit balance debit transaction")
		return false
	}

	r.logger.WithFields(logging.Fields{
		"tenant_id":   tenantID,
		"amount":      amountCents,
		"new_balance": newBalance,
		"tx_hash":     txHash,
	}).Warn("Debited balance due to failed x402 settlement")

	// Emit billing event for failed settlement
	emitBillingEvent(r.db, r.logger, eventX402SettlementFailed, tenantID, "x402_nonce", txHash, &ipcpb.BillingEvent{
		Amount:   float64(amountCents) / 100,
		Currency: billing.DefaultCurrency(),
		Status:   "failed",
	})
	return true
}

func (r *X402Reconciler) reverseSettlementRollup(ctx context.Context, settlement PendingSettlement) {
	if _, err := reverseX402RollupOnce(ctx, r.db, settlement.TenantID, settlement.ID, settlement.AmountCents); err != nil {
		r.logger.WithError(err).WithFields(logging.Fields{
			"nonce_id":  settlement.ID,
			"tenant_id": settlement.TenantID,
		}).Error("Failed to reverse x402 balance rollup")
	}
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
		emitBillingEvent(r.db, r.logger, eventX402RPCError, tenantID, "x402_network", network, &ipcpb.BillingEvent{
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
