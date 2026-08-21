//nolint:govet,sqlclosecheck // Transactional reversal branches deliberately use local error scopes; rows close before RPC work.
package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
)

const cryptoCanonicalityBatchSize = 100

type allocatedDepositCheck struct {
	id          string
	blockNumber int64
	blockHash   string
}

// reconcileAllocatedDepositCanonicality continuously revisits allocated
// deposits instead of assuming a consensus-labelled finalized block can never
// change. NULLS FIRST makes the pass restart-safe and eventually covers every
// credited event without holding a database cursor during RPC calls.
func (cm *CryptoMonitor) reconcileAllocatedDepositCanonicality(ctx context.Context, network NetworkConfig) error {
	queries := purserdb.New(cm.db)
	rows, err := queries.ListAllocatedDepositsForCanonicalityCheck(ctx, purserdb.ListAllocatedDepositsForCanonicalityCheckParams{
		Network: network.Name, RowLimit: cryptoCanonicalityBatchSize,
	})
	if err != nil {
		return fmt.Errorf("load allocated deposits for canonicality check: %w", err)
	}
	checks := make([]allocatedDepositCheck, 0, len(rows))
	for _, row := range rows {
		checks = append(checks, allocatedDepositCheck{id: row.ID, blockNumber: row.BlockNumber, blockHash: row.BlockHash})
	}

	for _, check := range checks {
		block, err := cm.rpcBlockByNumber(ctx, network, check.blockNumber, false)
		if err != nil {
			return fmt.Errorf("recheck allocated deposit block %d: %w", check.blockNumber, err)
		}
		if strings.EqualFold(block.Hash, check.blockHash) {
			if err := queries.TouchAllocatedDepositCanonicality(ctx, purserdb.TouchAllocatedDepositCanonicalityParams{
				EventID: check.id, BlockHash: check.blockHash,
			}); err != nil {
				return fmt.Errorf("record allocated deposit canonicality check: %w", err)
			}
			continue
		}
		if err := cm.reverseAllocatedDeposit(ctx, check.id, check.blockHash, block.Hash); err != nil {
			return fmt.Errorf("reverse reorged deposit %s: %w", check.id, err)
		}
	}
	return nil
}

type allocatedDepositReversal struct {
	network        string
	tenantID       string
	purpose        string
	invoiceID      sql.NullString
	creditedCents  sql.NullInt64
	creditCurrency sql.NullString
	walletID       string
	txHash         sql.NullString
}

func (cm *CryptoMonitor) reverseAllocatedDeposit(ctx context.Context, eventID, oldBlockHash, canonicalBlockHash string) error {
	tx, err := cm.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	queries := purserdb.New(tx)
	row, err := queries.LockAllocatedDepositReversal(ctx, eventID)
	if err != nil {
		return err
	}
	reversal := allocatedDepositReversal{
		network: row.Network, tenantID: row.TenantID, purpose: row.Purpose,
		invoiceID:     sql.NullString{String: row.InvoiceID, Valid: row.InvoiceID != ""},
		creditedCents: row.CreditedAmountCents, creditCurrency: row.CreditedAmountCurrency,
		walletID: row.WalletID, txHash: row.TxHash,
	}
	if row.EventStatus != "allocated" || !row.Canonical {
		return nil
	}

	switch reversal.purpose {
	case "prepaid":
		if err := reverseCryptoPrepaidDepositTx(ctx, tx, eventID, oldBlockHash, canonicalBlockHash, reversal); err != nil {
			return err
		}
	case "invoice":
		if err := reverseCryptoInvoiceDepositTx(ctx, tx, eventID, oldBlockHash, canonicalBlockHash, reversal); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported crypto wallet purpose %q", reversal.purpose)
	}

	affected, err := queries.MarkAllocatedDepositReorged(ctx, eventID)
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("allocated deposit changed concurrently")
	}
	if err := queries.MarkReorgedCryptoWalletForReview(ctx, purserdb.MarkReorgedCryptoWalletForReviewParams{
		WalletID: reversal.walletID, TenantID: reversal.tenantID,
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	cm.logger.WithFields(logging.Fields{
		"event_id": eventID, "tenant_id": reversal.tenantID,
		"wallet_id": reversal.walletID, "old_block_hash": oldBlockHash,
		"canonical_block_hash": canonicalBlockHash,
	}).Error("Reversed allocated crypto deposit after canonical block mismatch")
	if cm.metrics != nil && cm.metrics.CryptoDepositReorgs != nil {
		cm.metrics.CryptoDepositReorgs.WithLabelValues(reversal.network, reversal.purpose).Inc()
	}
	emitBillingEvent(cm.db, cm.logger, eventCryptoDepositReorg, reversal.tenantID, "crypto_deposit_event", eventID, &ipcpb.BillingEvent{
		Status: "allocated deposit reorged and reversed", Provider: "crypto", TxHash: reversal.txHash.String,
	})
	return nil
}

func reverseCryptoPrepaidDepositTx(ctx context.Context, tx *sql.Tx, eventID, oldBlockHash, canonicalBlockHash string, reversal allocatedDepositReversal) error {
	if !reversal.creditedCents.Valid || reversal.creditedCents.Int64 <= 0 || !reversal.creditCurrency.Valid {
		return fmt.Errorf("prepaid wallet has no persisted credit")
	}
	queries := purserdb.New(tx)
	original, err := queries.LockCryptoTopupBalanceTransaction(ctx, purserdb.LockCryptoTopupBalanceTransactionParams{
		TenantID: reversal.tenantID, ReferenceType: sql.NullString{String: "crypto_payment", Valid: true}, ReferenceID: reversal.walletID,
	})
	if err != nil {
		return fmt.Errorf("load original crypto credit: %w", err)
	}
	if original.AmountCents != reversal.creditedCents.Int64 {
		return fmt.Errorf("persisted wallet credit does not match balance transaction")
	}
	exists, err := queries.CryptoReversalBalanceTransactionExists(ctx, purserdb.CryptoReversalBalanceTransactionExistsParams{
		TenantID: reversal.tenantID, ReferenceType: sql.NullString{String: "crypto_reorg", Valid: true}, ReferenceID: eventID,
	})
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	newBalance, err := queries.AddPrepaidBalance(ctx, purserdb.AddPrepaidBalanceParams{
		AmountCents: -reversal.creditedCents.Int64, TenantID: reversal.tenantID, Currency: reversal.creditCurrency.String,
	})
	if err != nil {
		return err
	}
	err = queries.InsertCryptoReversalBalanceTransaction(ctx, purserdb.InsertCryptoReversalBalanceTransactionParams{
		ID: uuid.NewString(), TenantID: reversal.tenantID, AmountCents: -reversal.creditedCents.Int64, BalanceAfterCents: newBalance,
		Description: sql.NullString{String: "Crypto deposit reversed after finalized-block canonicality failure", Valid: true},
		ReferenceID: eventID, ReferenceType: sql.NullString{String: "crypto_reorg", Valid: true},
		Reason:                sql.NullString{String: "allocated deposit block is no longer canonical", Valid: true},
		EvidenceRef:           sql.NullString{String: fmt.Sprintf("old=%s canonical=%s", oldBlockHash, canonicalBlockHash), Valid: true},
		ReversesTransactionID: original.ID,
	})
	if err != nil {
		return err
	}
	if !reversal.txHash.Valid {
		return nil
	}
	return queries.InsertReorgedCryptoTopupCreditNote(ctx, purserdb.InsertReorgedCryptoTopupCreditNoteParams{
		EventID: eventID, TxHash: reversal.txHash.String, TenantID: reversal.tenantID,
	})
}

func reverseCryptoInvoiceDepositTx(ctx context.Context, tx *sql.Tx, eventID, oldBlockHash, canonicalBlockHash string, reversal allocatedDepositReversal) error {
	if !reversal.invoiceID.Valid || !reversal.txHash.Valid {
		return fmt.Errorf("invoice wallet has no invoice or transaction reference")
	}
	queries := purserdb.New(tx)
	payment, err := queries.LockConfirmedCryptoInvoicePayment(ctx, purserdb.LockConfirmedCryptoInvoicePaymentParams{
		InvoiceID: reversal.invoiceID.String, TxHash: reversal.txHash,
	})
	if err != nil {
		return fmt.Errorf("load confirmed crypto invoice payment: %w", err)
	}
	providerReversalID := "crypto-reorg:" + eventID
	paymentReversalID, err := queries.InsertCryptoReorgPaymentReversal(ctx, purserdb.InsertCryptoReorgPaymentReversalParams{
		TenantID: reversal.tenantID, PaymentID: payment.ID, InvoiceID: reversal.invoiceID.String,
		ProviderReversalID: providerReversalID, AmountCents: payment.AmountCents, Currency: payment.Currency,
		Reason:      sql.NullString{String: "crypto deposit block is no longer canonical", Valid: true},
		EvidenceRef: sql.NullString{String: fmt.Sprintf("old=%s canonical=%s", oldBlockHash, canonicalBlockHash), Valid: true},
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := applyInvoicePaymentReversalTx(ctx, tx, payment.ID, reversal.invoiceID.String, payment.AmountCents, payment.Currency); err != nil {
		return err
	}
	if err := applyOperatorCreditClawbackTx(ctx, tx, reversal.invoiceID.String, paymentReversalID, payment.AmountCents); err != nil {
		return err
	}
	if err := queries.InsertReorgedCryptoInvoiceCreditNote(ctx, purserdb.InsertReorgedCryptoInvoiceCreditNoteParams{
		TenantID: reversal.tenantID, InvoiceID: reversal.invoiceID.String, EventID: eventID,
		AmountCents: payment.AmountCents, Currency: payment.Currency, PaymentID: payment.ID, TxHash: reversal.txHash.String,
	}); err != nil {
		return err
	}
	return reverseCryptoInvoiceOverpaymentTx(ctx, tx, eventID, oldBlockHash, canonicalBlockHash, reversal)
}

func reverseCryptoInvoiceOverpaymentTx(ctx context.Context, tx *sql.Tx, eventID, oldBlockHash, canonicalBlockHash string, reversal allocatedDepositReversal) error {
	if !reversal.creditedCents.Valid || reversal.creditedCents.Int64 <= 0 || !reversal.creditCurrency.Valid {
		return nil
	}
	queries := purserdb.New(tx)
	original, err := queries.LockCryptoTopupBalanceTransaction(ctx, purserdb.LockCryptoTopupBalanceTransactionParams{
		TenantID: reversal.tenantID, ReferenceType: sql.NullString{String: "crypto_invoice_overpayment", Valid: true}, ReferenceID: reversal.walletID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if original.AmountCents != reversal.creditedCents.Int64 {
		return fmt.Errorf("invoice overpayment credit does not match wallet allocation")
	}
	exists, err := queries.CryptoReversalBalanceTransactionExists(ctx, purserdb.CryptoReversalBalanceTransactionExistsParams{
		TenantID: reversal.tenantID, ReferenceType: sql.NullString{String: "crypto_invoice_overpayment_reorg", Valid: true}, ReferenceID: eventID,
	})
	if err != nil || exists {
		return err
	}
	newBalance, err := queries.AddPrepaidBalance(ctx, purserdb.AddPrepaidBalanceParams{
		AmountCents: -original.AmountCents, TenantID: reversal.tenantID, Currency: reversal.creditCurrency.String,
	})
	if err != nil {
		return err
	}
	return queries.InsertCryptoReversalBalanceTransaction(ctx, purserdb.InsertCryptoReversalBalanceTransactionParams{
		ID: uuid.NewString(), TenantID: reversal.tenantID, AmountCents: -original.AmountCents, BalanceAfterCents: newBalance,
		Description: sql.NullString{String: "Crypto invoice overpayment reversed after canonicality failure", Valid: true},
		ReferenceID: eventID, ReferenceType: sql.NullString{String: "crypto_invoice_overpayment_reorg", Valid: true},
		Reason:                sql.NullString{String: "allocated deposit block is no longer canonical", Valid: true},
		EvidenceRef:           sql.NullString{String: fmt.Sprintf("old=%s canonical=%s", oldBlockHash, canonicalBlockHash), Valid: true},
		ReversesTransactionID: original.ID,
	})
}
