//nolint:govet,errcheck // Scanner branches use local error scopes; review-state writes are best-effort after durable observation.
package handlers

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/shopspring/decimal"
)

const cryptoScanBatchBlocks = int64(100)

type rpcBlock struct {
	Number       string           `json:"number"`
	Hash         string           `json:"hash"`
	ParentHash   string           `json:"parentHash"`
	Timestamp    string           `json:"timestamp"`
	Transactions []rpcTransaction `json:"transactions"`
}

type rpcTransaction struct {
	Hash  string `json:"hash"`
	From  string `json:"from"`
	To    string `json:"to"`
	Value string `json:"value"`
}

type rpcLog struct {
	Address     string   `json:"address"`
	Topics      []string `json:"topics"`
	Data        string   `json:"data"`
	BlockNumber string   `json:"blockNumber"`
	BlockHash   string   `json:"blockHash"`
	TxHash      string   `json:"transactionHash"`
	LogIndex    string   `json:"logIndex"`
	Removed     bool     `json:"removed"`
}

type observedDeposit struct {
	Asset       string
	TxHash      string
	LogIndex    int64
	BlockNumber int64
	BlockHash   string
	From        string
	To          string
	Amount      string
}

func (cm *CryptoMonitor) scanRPCDeposits(ctx context.Context) {
	for _, network := range DepositNetworks(cm.includeTestnets) {
		if network.GetRPCEndpointWithDefault() == "" {
			cm.recordScannerError(ctx, network.Name, "RPC endpoint is not configured")
			continue
		}
		if err := cm.scanRPCNetwork(ctx, network); err != nil {
			cm.recordScannerError(ctx, network.Name, err.Error())
			cm.logger.WithError(err).WithField("network", network.Name).Warn("Crypto RPC scan failed")
		}
	}
	cm.allocateConfirmedDepositEvents(ctx)
	cm.reconcileCompletedCryptoTopupInvoices(ctx)
	cm.refreshCryptoCustodyMetrics(ctx)
}

func (cm *CryptoMonitor) scanRPCNetwork(ctx context.Context, network NetworkConfig) error {
	latest, err := cm.rpcBlockNumber(ctx, network)
	if err != nil {
		return err
	}
	finality, err := GetFinalityHead(ctx, cm.rpc, network)
	if err != nil {
		return err
	}
	if cm.metrics != nil && cm.metrics.CryptoScannerBlocks != nil {
		cm.metrics.CryptoScannerBlocks.WithLabelValues(network.Name, "latest").Set(float64(latest))
		cm.metrics.CryptoScannerBlocks.WithLabelValues(network.Name, "finalized").Set(float64(finality.Number))
	}
	if err := cm.reconcileAllocatedDepositCanonicality(ctx, network); err != nil {
		return err
	}
	safeHead := finality.Number
	next, lastBlock, lastHash, err := cm.loadOrCreateScanCursor(ctx, network, safeHead)
	if err != nil {
		return err
	}
	if lastBlock.Valid && lastHash.Valid {
		canonical, blockErr := cm.rpcBlockByNumber(ctx, network, lastBlock.Int64, false)
		if blockErr != nil {
			return blockErr
		}
		if canonical == nil || !strings.EqualFold(canonical.Hash, lastHash.String) {
			rewind := lastBlock.Int64 - int64(network.Confirmations)
			if rewind < 0 {
				rewind = 0
			}
			if err := cm.rewindScanCursor(ctx, network.Name, rewind); err != nil {
				return err
			}
			next = rewind
		}
	}
	if next > safeHead {
		return cm.updateScannerLag(ctx, network.Name, safeHead, latest-next)
	}
	end := next + cryptoScanBatchBlocks - 1
	if end > safeHead {
		end = safeHead
	}

	addresses, err := cm.knownDepositAddresses(ctx, network.Name)
	if err != nil {
		return err
	}
	if len(addresses) == 0 {
		block, err := cm.rpcBlockByNumber(ctx, network, end, false)
		if err != nil {
			return err
		}
		return cm.commitScanBatch(ctx, network.Name, next, end, safeHead, block.Hash, nil)
	}

	events, err := cm.scanUSDCLogs(ctx, network, next, end, addresses)
	if err != nil {
		return err
	}
	blocks, err := cm.rpcBlocksByNumber(ctx, network, next, end, true)
	if err != nil {
		return err
	}
	for index := range blocks {
		blockNumber := next + int64(index)
		block := &blocks[index]
		for _, transaction := range block.Transactions {
			to := strings.ToLower(transaction.To)
			if _, ok := addresses[to]; !ok || isZeroHex(transaction.Value) {
				continue
			}
			amount, err := hexQuantityToDecimal(transaction.Value)
			if err != nil || amount == "0" {
				continue
			}
			events = append(events, observedDeposit{
				Asset: "ETH", TxHash: transaction.Hash, LogIndex: -1,
				BlockNumber: blockNumber, BlockHash: block.Hash,
				From: strings.ToLower(transaction.From), To: to, Amount: amount,
			})
		}
	}
	return cm.commitScanBatch(ctx, network.Name, next, end, safeHead, blocks[len(blocks)-1].Hash, events)
}

func (cm *CryptoMonitor) loadOrCreateScanCursor(ctx context.Context, network NetworkConfig, safeHead int64) (int64, sql.NullInt64, sql.NullString, error) {
	start, err := cryptoScannerStartBlock(network.Name, safeHead)
	if err != nil {
		return 0, sql.NullInt64{}, sql.NullString{}, err
	}
	_, err = cm.db.ExecContext(ctx, `
		INSERT INTO purser.crypto_scan_cursors (network, next_block, safe_head_block, updated_at)
		VALUES ($1, $2, $3, NOW()) ON CONFLICT (network) DO NOTHING
	`, network.Name, start, safeHead)
	if err != nil {
		return 0, sql.NullInt64{}, sql.NullString{}, err
	}
	var next int64
	var last sql.NullInt64
	var hash sql.NullString
	err = cm.db.QueryRowContext(ctx, `
		SELECT next_block, last_scanned_block, last_scanned_block_hash
		FROM purser.crypto_scan_cursors WHERE network = $1
	`, network.Name).Scan(&next, &last, &hash)
	return next, last, hash, err
}

func cryptoScannerStartBlock(network string, safeHead int64) (int64, error) {
	key := "CRYPTO_SCAN_START_BLOCK_" + strings.ToUpper(strings.ReplaceAll(network, "-", "_"))
	value := strings.TrimSpace(os.Getenv(key))
	if value != "" {
		start, err := strconv.ParseInt(value, 10, 64)
		if err != nil || start < 0 {
			return 0, fmt.Errorf("%s must be a non-negative block number", key)
		}
		return start, nil
	}
	if config.IsProduction() {
		return 0, fmt.Errorf("%s is required in production", key)
	}
	start := safeHead - 1000
	if start < 0 {
		start = 0
	}
	return start, nil
}

func ValidateCryptoScannerStart(network string) error {
	_, err := cryptoScannerStartBlock(network, 0)
	return err
}

func (cm *CryptoMonitor) knownDepositAddresses(ctx context.Context, network string) (map[string]struct{}, error) {
	rows, err := cm.db.QueryContext(ctx, `
		SELECT DISTINCT LOWER(wallet_address)
		FROM purser.crypto_wallets
		WHERE network = $1
	`, network)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	addresses := map[string]struct{}{}
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			return nil, err
		}
		addresses[address] = struct{}{}
	}
	return addresses, rows.Err()
}

func (cm *CryptoMonitor) scanUSDCLogs(ctx context.Context, network NetworkConfig, from, to int64, addresses map[string]struct{}) ([]observedDeposit, error) {
	transferTopic := "0x" + hex.EncodeToString(crypto.Keccak256([]byte("Transfer(address,address,uint256)")))
	destinationTopics := make([]string, 0, len(addresses))
	for address := range addresses {
		destinationTopics = append(destinationTopics, "0x"+strings.Repeat("0", 24)+strings.TrimPrefix(address, "0x"))
	}
	var logs []rpcLog
	err := cm.rpc.Call(ctx, network, "eth_getLogs", []any{map[string]any{
		"fromBlock": fmt.Sprintf("0x%x", from), "toBlock": fmt.Sprintf("0x%x", to),
		"address": network.USDCContract, "topics": []any{transferTopic, nil, destinationTopics},
	}}, &logs)
	if err != nil {
		return nil, err
	}
	events := make([]observedDeposit, 0, len(logs))
	for _, log := range logs {
		if log.Removed || len(log.Topics) < 3 || len(log.Topics[1]) < 42 || len(log.Topics[2]) < 42 {
			continue
		}
		toAddress := "0x" + log.Topics[2][len(log.Topics[2])-40:]
		if _, ok := addresses[strings.ToLower(toAddress)]; !ok {
			continue
		}
		amount, err := hexQuantityToDecimal(log.Data)
		if err != nil || amount == "0" {
			continue
		}
		events = append(events, observedDeposit{
			Asset: "USDC", TxHash: log.TxHash, LogIndex: parseHexInt64(log.LogIndex),
			BlockNumber: parseHexInt64(log.BlockNumber), BlockHash: log.BlockHash,
			From: "0x" + log.Topics[1][len(log.Topics[1])-40:], To: strings.ToLower(toAddress), Amount: amount,
		})
	}
	return events, nil
}

func (cm *CryptoMonitor) commitScanBatch(ctx context.Context, network string, from, to, safeHead int64, lastHash string, events []observedDeposit) error {
	tx, err := cm.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, event := range events {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO purser.crypto_deposit_events (
				network, asset, tx_hash, log_index, block_number, block_hash,
				from_address, to_address, amount_base_units, status,
				confirmations, confirmed_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,'confirmed',1,NOW())
			ON CONFLICT (network, tx_hash, log_index) DO UPDATE
			SET canonical = TRUE, block_hash = EXCLUDED.block_hash,
			    block_number = EXCLUDED.block_number,
			    confirmations = GREATEST(purser.crypto_deposit_events.confirmations, EXCLUDED.confirmations),
			    status = CASE WHEN purser.crypto_deposit_events.status = 'reorged' THEN 'confirmed' ELSE purser.crypto_deposit_events.status END,
			    reorged_at = NULL
		`, network, event.Asset, event.TxHash, event.LogIndex, event.BlockNumber,
			event.BlockHash, event.From, event.To, event.Amount)
		if err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE purser.crypto_scan_cursors
		SET next_block = $3 + 1, last_scanned_block = $3,
		    last_scanned_block_hash = $4, safe_head_block = $5,
		    lag_blocks = GREATEST($5 - $3, 0), last_error = NULL,
		    error_count = 0, scanned_at = NOW(), updated_at = NOW()
		WHERE network = $1 AND next_block = $2
	`, network, from, to, lastHash, safeHead)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return fmt.Errorf("crypto scan cursor changed concurrently")
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if cm.metrics != nil && cm.metrics.CryptoScannerBlocks != nil {
		cm.metrics.CryptoScannerBlocks.WithLabelValues(network, "cursor").Set(float64(to))
	}
	return nil
}

func (cm *CryptoMonitor) rewindScanCursor(ctx context.Context, network string, block int64) error {
	tx, err := cm.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `
		UPDATE purser.crypto_deposit_events
		SET canonical = FALSE, status = 'reorged', reorged_at = NOW()
		WHERE network = $1 AND block_number >= $2 AND status != 'allocated'
	`, network, block); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE purser.crypto_scan_cursors
		SET next_block = $2, last_scanned_block = NULL,
		    last_scanned_block_hash = NULL, updated_at = NOW()
		WHERE network = $1
	`, network, block); err != nil {
		return err
	}
	return tx.Commit()
}

func (cm *CryptoMonitor) rpcBlockNumber(ctx context.Context, network NetworkConfig) (int64, error) {
	var value string
	if err := cm.rpc.Call(ctx, network, "eth_blockNumber", []any{}, &value); err != nil {
		return 0, err
	}
	return parseHexInt64(value), nil
}

func (cm *CryptoMonitor) rpcBlockByNumber(ctx context.Context, network NetworkConfig, number int64, transactions bool) (*rpcBlock, error) {
	var block rpcBlock
	if err := cm.rpc.Call(ctx, network, "eth_getBlockByNumber", []any{fmt.Sprintf("0x%x", number), transactions}, &block); err != nil {
		return nil, err
	}
	if block.Hash == "" {
		return nil, fmt.Errorf("block %d is unavailable", number)
	}
	return &block, nil
}

func (cm *CryptoMonitor) rpcBlocksByNumber(ctx context.Context, network NetworkConfig, from, to int64, transactions bool) ([]rpcBlock, error) {
	if from < 0 || to < from || to-from+1 > cryptoScanBatchBlocks {
		return nil, fmt.Errorf("invalid RPC block batch %d..%d", from, to)
	}
	if from == to {
		block, err := cm.rpcBlockByNumber(ctx, network, from, transactions)
		if err != nil {
			return nil, err
		}
		return []rpcBlock{*block}, nil
	}
	blocks := make([]rpcBlock, to-from+1)
	calls := make([]RPCBatchCall, len(blocks))
	for i := range blocks {
		calls[i] = RPCBatchCall{
			Method: "eth_getBlockByNumber",
			Params: []any{fmt.Sprintf("0x%x", from+int64(i)), transactions},
			Result: &blocks[i],
		}
	}
	if err := cm.rpc.BatchCall(ctx, network, calls); err != nil {
		return nil, err
	}
	for i := range blocks {
		if blocks[i].Hash == "" {
			return nil, fmt.Errorf("block %d is unavailable", from+int64(i))
		}
	}
	return blocks, nil
}

func (cm *CryptoMonitor) updateScannerLag(ctx context.Context, network string, safeHead, lag int64) error {
	_, err := cm.db.ExecContext(ctx, `
		UPDATE purser.crypto_scan_cursors
		SET safe_head_block = $2, lag_blocks = GREATEST($3, 0),
		    last_error = NULL, error_count = 0, updated_at = NOW()
		WHERE network = $1
	`, network, safeHead, lag)
	return err
}

func (cm *CryptoMonitor) recordScannerError(ctx context.Context, network, message string) {
	if cm.metrics != nil && cm.metrics.CryptoScannerErrors != nil {
		cm.metrics.CryptoScannerErrors.WithLabelValues(network).Inc()
	}
	_, _ = cm.db.ExecContext(ctx, `
		INSERT INTO purser.crypto_scan_cursors (network, next_block, last_error, error_count)
		VALUES ($1, 0, $2, 1)
		ON CONFLICT (network) DO UPDATE
		SET last_error = EXCLUDED.last_error,
		    error_count = purser.crypto_scan_cursors.error_count + 1,
		    updated_at = NOW()
	`, network, message)
}

func hexQuantityToDecimal(value string) (string, error) {
	parsed := new(big.Int)
	if _, ok := parsed.SetString(strings.TrimPrefix(value, "0x"), 16); !ok {
		return "", fmt.Errorf("invalid hex quantity")
	}
	return parsed.String(), nil
}

func isZeroHex(value string) bool {
	parsed, err := hexQuantityToDecimal(value)
	return err != nil || parsed == "0"
}

func (cm *CryptoMonitor) allocateConfirmedDepositEvents(ctx context.Context) {
	rows, err := cm.db.QueryContext(ctx, `
		SELECT e.id::text, e.tx_hash, e.block_number, e.amount_base_units::text,
		       w.id::text, w.tenant_id::text, w.purpose, w.invoice_id,
		       w.expected_amount_cents, w.asset, w.network, w.wallet_address,
		       w.expected_amount_base_units::text, w.quoted_price_usd::text,
		       w.quoted_usd_to_eur_rate::text, COALESCE(w.quote_source, ''),
		       COALESCE(w.credited_amount_currency, ''), COALESCE(w.client_ip, ''), w.expires_at,
		       i.amount, i.currency
		FROM purser.crypto_deposit_events e
		JOIN purser.crypto_wallets w
		  ON w.network = e.network AND LOWER(w.wallet_address) = LOWER(e.to_address)
		 AND w.asset = e.asset
		LEFT JOIN purser.billing_invoices i
		  ON i.id = w.invoice_id AND i.tenant_id = w.tenant_id
		WHERE e.canonical AND e.status = 'confirmed'
		  AND w.status IN ('pending', 'confirming')
		ORDER BY e.block_number, e.log_index
		LIMIT 100
	`)
	if err != nil {
		cm.logger.WithError(err).Warn("Failed to load confirmed crypto deposit events")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var eventID, txHash, amount, walletID, tenantID, purpose, asset, network, address string
		var blockNumber int64
		var invoiceID, quoteSource, currency, clientIP sql.NullString
		var expectedCents sql.NullInt64
		var expectedBase, price, fx sql.NullString
		var expires time.Time
		var invoiceAmount sql.NullFloat64
		var invoiceCurrency sql.NullString
		if err := rows.Scan(&eventID, &txHash, &blockNumber, &amount,
			&walletID, &tenantID, &purpose, &invoiceID, &expectedCents,
			&asset, &network, &address, &expectedBase, &price, &fx,
			&quoteSource, &currency, &clientIP, &expires, &invoiceAmount, &invoiceCurrency); err != nil {
			cm.logger.WithError(err).Warn("Failed to scan crypto deposit allocation")
			continue
		}
		wallet := PendingWallet{ID: walletID, TenantID: tenantID, Purpose: purpose, Asset: asset, Network: network, WalletAddress: address, ExpiresAt: expires}
		if invoiceID.Valid {
			wallet.InvoiceID = &invoiceID.String
		}
		if expectedCents.Valid {
			wallet.ExpectedAmountCents = &expectedCents.Int64
		}
		if expectedBase.Valid {
			wallet.ExpectedAmountBaseUnits, _ = new(big.Int).SetString(expectedBase.String, 10)
		}
		if price.Valid {
			wallet.QuotedPriceUSD, _ = decimal.NewFromString(price.String)
		}
		if fx.Valid {
			value, parseErr := decimal.NewFromString(fx.String)
			if parseErr == nil {
				wallet.QuotedUSDToEURRate = &value
			}
		}
		wallet.QuoteSource = quoteSource.String
		wallet.CreditedAmountCurrency = currency.String
		wallet.ClientIP = clientIP.String
		if invoiceAmount.Valid {
			wallet.InvoiceAmount = &invoiceAmount.Float64
		}
		if invoiceCurrency.Valid {
			wallet.InvoiceCurrency = &invoiceCurrency.String
		}
		cm.allocateObservedDeposit(ctx, eventID, txHash, blockNumber, amount, wallet)
	}
}

func (cm *CryptoMonitor) allocateObservedDeposit(ctx context.Context, eventID, txHash string, blockNumber int64, amount string, wallet PendingWallet) {
	network, ok := Networks[wallet.Network]
	if !ok || wallet.ExpectedAmountBaseUnits == nil {
		return
	}
	baseUnits, ok := new(big.Int).SetString(amount, 10)
	if !ok || baseUnits.Sign() <= 0 {
		return
	}
	decimals, ok := TokenDecimals(wallet.Asset)
	if !ok {
		return
	}
	expectedAmount, _ := decimal.NewFromBigInt(wallet.ExpectedAmountBaseUnits, -decimals).Float64()
	tx := CryptoTransaction{
		EventID: eventID, Hash: txHash, To: wallet.WalletAddress, Value: amount,
		Confirmations: network.Confirmations, BlockNumber: blockNumber, BlockTime: time.Now().UTC(),
	}
	match := cm.evaluatePayment(tx, wallet, expectedAmount, network)
	if !match.amountSeen {
		_, _ = cm.db.ExecContext(ctx, `
			UPDATE purser.crypto_deposit_events
			SET status = 'review_required', wallet_id = $2, allocation_error = $3
			WHERE id = $1 AND status = 'confirmed'
		`, eventID, wallet.ID, "deposit amount does not match allocatable quote")
		return
	}
	if wallet.Purpose == "invoice" && match.txBaseUnits.Cmp(wallet.ExpectedAmountBaseUnits) < 0 {
		cm.markDepositForReview(wallet, tx, match.txBaseUnits, "invoice payment is below the exact quote")
		_, _ = cm.db.ExecContext(ctx, `
			UPDATE purser.crypto_deposit_events
			SET status = 'review_required', wallet_id = $2, allocation_error = $3
			WHERE id = $1 AND status = 'confirmed'
		`, eventID, wallet.ID, "invoice payment is below the exact quote")
		return
	}
	if time.Now().After(wallet.ExpiresAt) {
		if wallet.Purpose == "invoice" {
			cm.markDepositForReview(wallet, tx, match.txBaseUnits, "invoice payment arrived after quote expiry")
			return
		}
		if err := cm.refreshLatePrepaidValuation(ctx, &wallet, network); err != nil {
			return
		}
	}
	cm.confirmPayment(wallet, tx, match.txBaseUnits, match.txAmount)
}
