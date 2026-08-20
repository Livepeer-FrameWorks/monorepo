//nolint:govet,sqlclosecheck // Transactional reversal branches deliberately use local error scopes; rows close before RPC work.
package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

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
	rows, err := cm.db.QueryContext(ctx, `
		SELECT id::text, block_number, block_hash
		FROM purser.crypto_deposit_events
		WHERE network = $1 AND canonical AND status = 'allocated'
		ORDER BY last_canonical_checked_at ASC NULLS FIRST, block_number DESC
		LIMIT $2
	`, network.Name, cryptoCanonicalityBatchSize)
	if err != nil {
		return fmt.Errorf("load allocated deposits for canonicality check: %w", err)
	}
	var checks []allocatedDepositCheck
	for rows.Next() {
		var check allocatedDepositCheck
		if err := rows.Scan(&check.id, &check.blockNumber, &check.blockHash); err != nil {
			_ = rows.Close()
			return err
		}
		checks = append(checks, check)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, check := range checks {
		block, err := cm.rpcBlockByNumber(ctx, network, check.blockNumber, false)
		if err != nil {
			return fmt.Errorf("recheck allocated deposit block %d: %w", check.blockNumber, err)
		}
		if strings.EqualFold(block.Hash, check.blockHash) {
			if _, err := cm.db.ExecContext(ctx, `
				UPDATE purser.crypto_deposit_events
				SET last_canonical_checked_at = NOW()
				WHERE id = $1 AND canonical AND status = 'allocated' AND block_hash = $2
			`, check.id, check.blockHash); err != nil {
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

	var eventStatus string
	var canonical bool
	var reversal allocatedDepositReversal
	err = tx.QueryRowContext(ctx, `
		SELECT e.status, e.canonical, w.network, w.tenant_id::text, w.purpose,
		       w.invoice_id::text, w.credited_amount_cents,
		       w.credited_amount_currency, w.id::text, w.tx_hash
		FROM purser.crypto_deposit_events e
		JOIN purser.crypto_wallets w ON w.id = e.wallet_id
		WHERE e.id = $1
		FOR UPDATE OF e, w
	`, eventID).Scan(&eventStatus, &canonical, &reversal.network, &reversal.tenantID, &reversal.purpose,
		&reversal.invoiceID, &reversal.creditedCents, &reversal.creditCurrency,
		&reversal.walletID, &reversal.txHash)
	if err != nil {
		return err
	}
	if eventStatus != "allocated" || !canonical {
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

	result, err := tx.ExecContext(ctx, `
		UPDATE purser.crypto_deposit_events
		SET canonical = FALSE, status = 'reorged', reorged_at = NOW(),
		    last_canonical_checked_at = NOW(),
		    allocation_error = 'allocated deposit block is no longer canonical'
		WHERE id = $1 AND canonical AND status = 'allocated'
	`, eventID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return fmt.Errorf("allocated deposit changed concurrently")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE purser.crypto_wallets
		SET status = 'review_required', updated_at = NOW()
		WHERE id = $1 AND tenant_id = $2
	`, reversal.walletID, reversal.tenantID); err != nil {
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
	var originalTransactionID string
	var originalAmount int64
	err := tx.QueryRowContext(ctx, `
		SELECT id::text, amount_cents
		FROM purser.balance_transactions
		WHERE tenant_id = $1 AND reference_type = 'crypto_payment'
		  AND reference_id = $2 AND transaction_type = 'topup'
		FOR UPDATE
	`, reversal.tenantID, reversal.walletID).Scan(&originalTransactionID, &originalAmount)
	if err != nil {
		return fmt.Errorf("load original crypto credit: %w", err)
	}
	if originalAmount != reversal.creditedCents.Int64 {
		return fmt.Errorf("persisted wallet credit does not match balance transaction")
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM purser.balance_transactions
			WHERE tenant_id = $1 AND reference_type = 'crypto_reorg' AND reference_id = $2
		)
	`, reversal.tenantID, eventID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	var balance int64
	if err := tx.QueryRowContext(ctx, `
		SELECT balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2 FOR UPDATE
	`, reversal.tenantID, reversal.creditCurrency.String).Scan(&balance); err != nil {
		return err
	}
	newBalance := balance - reversal.creditedCents.Int64
	if _, err := tx.ExecContext(ctx, `
		UPDATE purser.prepaid_balances SET balance_cents = $1, updated_at = NOW()
		WHERE tenant_id = $2 AND currency = $3
	`, newBalance, reversal.tenantID, reversal.creditCurrency.String); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO purser.balance_transactions (
			id, tenant_id, amount_cents, balance_after_cents, transaction_type,
			description, reference_id, reference_type, actor_kind, reason,
			evidence_ref, reverses_transaction_id, created_at
		) VALUES ($1,$2,$3,$4,'reversal',$5,$6,'crypto_reorg','job',$7,$8,$9,NOW())
	`, uuid.NewString(), reversal.tenantID, -reversal.creditedCents.Int64, newBalance,
		"Crypto deposit reversed after finalized-block canonicality failure", eventID,
		"allocated deposit block is no longer canonical",
		fmt.Sprintf("old=%s canonical=%s", oldBlockHash, canonicalBlockHash), originalTransactionID)
	if err != nil {
		return err
	}
	if !reversal.txHash.Valid {
		return nil
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO purser.credit_notes (
			credit_note_number, tenant_id, source_document_type, source_document_id,
			reversal_reference_type, reversal_reference_id, amount_cents, currency,
			reason, evidence_json
		)
		SELECT 'CN-' || lpad(nextval('purser.credit_note_number_seq')::text, 10, '0'),
		       invoice.tenant_id, 'simplified_invoice', invoice.id,
		       'crypto_reorg', $3, invoice.gross_amount_cents, invoice.currency,
		       'confirmed direct crypto top-up reversed after canonicality failure',
		       jsonb_build_object('transaction_hash', $2::text, 'event_id', $3::text)
		FROM purser.simplified_invoices invoice
		WHERE invoice.tenant_id = $1 AND invoice.reference_type = 'crypto_payment'
		  AND invoice.reference_id = $2
		ON CONFLICT (source_document_type, source_document_id, reversal_reference_type, reversal_reference_id)
		DO NOTHING
	`, reversal.tenantID, reversal.txHash.String, eventID)
	return err
}

func reverseCryptoInvoiceDepositTx(ctx context.Context, tx *sql.Tx, eventID, oldBlockHash, canonicalBlockHash string, reversal allocatedDepositReversal) error {
	if !reversal.invoiceID.Valid || !reversal.txHash.Valid {
		return fmt.Errorf("invoice wallet has no invoice or transaction reference")
	}
	var paymentID, currency string
	var amountCents int64
	err := tx.QueryRowContext(ctx, `
		SELECT id::text, (amount * 100)::bigint, currency
		FROM purser.billing_payments
		WHERE invoice_id = $1 AND tx_id = $2
		  AND method IN ('crypto_eth', 'crypto_usdc') AND status = 'confirmed'
		FOR UPDATE
	`, reversal.invoiceID.String, reversal.txHash.String).Scan(&paymentID, &amountCents, &currency)
	if err != nil {
		return fmt.Errorf("load confirmed crypto invoice payment: %w", err)
	}
	providerReversalID := "crypto-reorg:" + eventID
	var paymentReversalID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO purser.payment_reversals (
			tenant_id, payment_id, invoice_id, provider, reversal_type,
			provider_reversal_id, amount_cents, currency, status, reason,
			operator_review_required, actor_kind, evidence_ref
		) VALUES ($1,$2,$3,'manual','manual',$4,$5,$6,'succeeded',$7,TRUE,'job',$8)
		ON CONFLICT (provider, provider_reversal_id) DO NOTHING
		RETURNING id::text
	`, reversal.tenantID, paymentID, reversal.invoiceID.String, providerReversalID,
		amountCents, currency, "crypto deposit block is no longer canonical",
		fmt.Sprintf("old=%s canonical=%s", oldBlockHash, canonicalBlockHash)).Scan(&paymentReversalID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := applyInvoicePaymentReversalTx(ctx, tx, paymentID, reversal.invoiceID.String, amountCents, currency); err != nil {
		return err
	}
	if err := applyOperatorCreditClawbackTx(ctx, tx, reversal.invoiceID.String, paymentReversalID, amountCents); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO purser.credit_notes (
			credit_note_number, tenant_id, source_document_type, source_document_id,
			reversal_reference_type, reversal_reference_id, amount_cents, currency,
			reason, evidence_json
		) VALUES (
			'CN-' || lpad(nextval('purser.credit_note_number_seq')::text, 10, '0'),
			$1, 'invoice', $2, 'crypto_reorg', $3, $4, $5,
			'confirmed crypto invoice payment reversed after canonicality failure',
			jsonb_build_object('payment_id', $6::text, 'transaction_hash', $7::text)
		)
		ON CONFLICT (source_document_type, source_document_id, reversal_reference_type, reversal_reference_id)
		DO NOTHING
	`, reversal.tenantID, reversal.invoiceID.String, eventID, amountCents, currency, paymentID, reversal.txHash.String); err != nil {
		return err
	}
	return reverseCryptoInvoiceOverpaymentTx(ctx, tx, eventID, oldBlockHash, canonicalBlockHash, reversal)
}

func reverseCryptoInvoiceOverpaymentTx(ctx context.Context, tx *sql.Tx, eventID, oldBlockHash, canonicalBlockHash string, reversal allocatedDepositReversal) error {
	if !reversal.creditedCents.Valid || reversal.creditedCents.Int64 <= 0 || !reversal.creditCurrency.Valid {
		return nil
	}
	var originalID string
	var amount int64
	err := tx.QueryRowContext(ctx, `
		SELECT id::text, amount_cents
		FROM purser.balance_transactions
		WHERE tenant_id = $1 AND reference_type = 'crypto_invoice_overpayment'
		  AND reference_id = $2 AND transaction_type = 'topup'
		FOR UPDATE
	`, reversal.tenantID, reversal.walletID).Scan(&originalID, &amount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if amount != reversal.creditedCents.Int64 {
		return fmt.Errorf("invoice overpayment credit does not match wallet allocation")
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM purser.balance_transactions
			WHERE tenant_id = $1 AND reference_type = 'crypto_invoice_overpayment_reorg'
			  AND reference_id = $2
		)
	`, reversal.tenantID, eventID).Scan(&exists); err != nil || exists {
		return err
	}
	var balance int64
	if err := tx.QueryRowContext(ctx, `
		SELECT balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2 FOR UPDATE
	`, reversal.tenantID, reversal.creditCurrency.String).Scan(&balance); err != nil {
		return err
	}
	newBalance := balance - amount
	if _, err := tx.ExecContext(ctx, `
		UPDATE purser.prepaid_balances SET balance_cents = $1, updated_at = NOW()
		WHERE tenant_id = $2 AND currency = $3
	`, newBalance, reversal.tenantID, reversal.creditCurrency.String); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO purser.balance_transactions (
			id, tenant_id, amount_cents, balance_after_cents, transaction_type,
			description, reference_id, reference_type, actor_kind, reason,
			evidence_ref, reverses_transaction_id, created_at
		) VALUES ($1,$2,$3,$4,'reversal',$5,$6,'crypto_invoice_overpayment_reorg',
		          'job',$7,$8,$9,NOW())
	`, uuid.NewString(), reversal.tenantID, -amount, newBalance,
		"Crypto invoice overpayment reversed after canonicality failure", eventID,
		"allocated deposit block is no longer canonical",
		fmt.Sprintf("old=%s canonical=%s", oldBlockHash, canonicalBlockHash), originalID)
	return err
}
