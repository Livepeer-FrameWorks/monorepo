//nolint:govet,errcheck // Scanner branches use local error scopes; review-state writes are best-effort after durable observation.
package handlers

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/shopspring/decimal"
)

const cryptoScanBatchBlocks = int64(100)
const cryptoScanTopicBatchSize = 100

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
	queries := purserdb.New(cm.db)
	err = queries.EnsureCryptoScanCursor(ctx, purserdb.EnsureCryptoScanCursorParams{
		Network: network.Name, NextBlock: start,
		SafeHeadBlock: sql.NullInt64{Int64: safeHead, Valid: true},
	})
	if err != nil {
		return 0, sql.NullInt64{}, sql.NullString{}, err
	}
	cursor, err := queries.GetCryptoScanCursor(ctx, network.Name)
	return cursor.NextBlock, cursor.LastScannedBlock, cursor.LastScannedBlockHash, err
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
	rows, err := purserdb.New(cm.db).ListKnownCryptoDepositAddresses(ctx, network)
	if err != nil {
		return nil, err
	}
	addresses := map[string]struct{}{}
	for _, address := range rows {
		addresses[address] = struct{}{}
	}
	return addresses, nil
}

func (cm *CryptoMonitor) scanUSDCLogs(ctx context.Context, network NetworkConfig, from, to int64, addresses map[string]struct{}) ([]observedDeposit, error) {
	transferTopic := "0x" + hex.EncodeToString(crypto.Keccak256([]byte("Transfer(address,address,uint256)")))
	destinationTopics := make([]string, 0, len(addresses))
	for address := range addresses {
		destinationTopics = append(destinationTopics, "0x"+strings.Repeat("0", 24)+strings.TrimPrefix(address, "0x"))
	}
	sort.Strings(destinationTopics)
	var logs []rpcLog
	for start := 0; start < len(destinationTopics); start += cryptoScanTopicBatchSize {
		end := start + cryptoScanTopicBatchSize
		if end > len(destinationTopics) {
			end = len(destinationTopics)
		}
		shardLogs, err := cm.scanUSDCLogShard(ctx, network, from, to, transferTopic, destinationTopics[start:end])
		if err != nil {
			return nil, err
		}
		logs = append(logs, shardLogs...)
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

func (cm *CryptoMonitor) scanUSDCLogShard(ctx context.Context, network NetworkConfig, from, to int64, transferTopic string, destinationTopics []string) ([]rpcLog, error) {
	var logs []rpcLog
	err := cm.rpc.Call(ctx, network, "eth_getLogs", []any{map[string]any{
		"fromBlock": fmt.Sprintf("0x%x", from), "toBlock": fmt.Sprintf("0x%x", to),
		"address": network.USDCContract, "topics": []any{transferTopic, nil, destinationTopics},
	}}, &logs)
	if err == nil {
		return logs, nil
	}
	if len(destinationTopics) <= 1 {
		return nil, err
	}
	middle := len(destinationTopics) / 2
	left, leftErr := cm.scanUSDCLogShard(ctx, network, from, to, transferTopic, destinationTopics[:middle])
	if leftErr != nil {
		return nil, leftErr
	}
	right, rightErr := cm.scanUSDCLogShard(ctx, network, from, to, transferTopic, destinationTopics[middle:])
	if rightErr != nil {
		return nil, rightErr
	}
	return append(left, right...), nil
}

func (cm *CryptoMonitor) commitScanBatch(ctx context.Context, network string, from, to, safeHead int64, lastHash string, events []observedDeposit) error {
	tx, err := cm.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	queries := purserdb.New(tx)
	for _, event := range events {
		err := queries.UpsertObservedCryptoDeposit(ctx, purserdb.UpsertObservedCryptoDepositParams{
			Network: network, Asset: event.Asset, TxHash: event.TxHash, LogIndex: event.LogIndex,
			BlockNumber: event.BlockNumber, BlockHash: event.BlockHash,
			FromAddress: sql.NullString{String: event.From, Valid: event.From != ""},
			ToAddress:   event.To, AmountBaseUnits: event.Amount,
		})
		if err != nil {
			return err
		}
	}
	rows, err := queries.AdvanceCryptoScanCursor(ctx, purserdb.AdvanceCryptoScanCursorParams{
		LastScannedBlock:     to,
		LastScannedBlockHash: sql.NullString{String: lastHash, Valid: true},
		SafeHeadBlock:        safeHead,
		Network:              network, ExpectedNextBlock: from,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
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
	queries := purserdb.New(tx)
	if err := queries.MarkCryptoDepositsReorgedFromBlock(ctx, purserdb.MarkCryptoDepositsReorgedFromBlockParams{Network: network, BlockNumber: block}); err != nil {
		return err
	}
	if err := queries.RewindCryptoScanCursor(ctx, purserdb.RewindCryptoScanCursorParams{BlockNumber: block, Network: network}); err != nil {
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
	return purserdb.New(cm.db).UpdateCryptoScannerLag(ctx, purserdb.UpdateCryptoScannerLagParams{
		SafeHeadBlock: sql.NullInt64{Int64: safeHead, Valid: true},
		LagBlocks:     sql.NullInt64{Int64: lag, Valid: true}, Network: network,
	})
}

func (cm *CryptoMonitor) recordScannerError(ctx context.Context, network, message string) {
	if cm.metrics != nil && cm.metrics.CryptoScannerErrors != nil {
		cm.metrics.CryptoScannerErrors.WithLabelValues(network).Inc()
	}
	_ = purserdb.New(cm.db).RecordCryptoScannerError(ctx, purserdb.RecordCryptoScannerErrorParams{
		Network: network, Message: sql.NullString{String: message, Valid: true},
	})
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
	rows, err := purserdb.New(cm.db).ListConfirmedCryptoDepositAllocations(ctx)
	if err != nil {
		cm.logger.WithError(err).Warn("Failed to load confirmed crypto deposit events")
		return
	}
	for _, row := range rows {
		wallet := PendingWallet{
			ID: row.WalletID, TenantID: row.TenantID, Purpose: row.Purpose,
			Asset: row.Asset, Network: row.Network, WalletAddress: row.WalletAddress, ExpiresAt: row.ExpiresAt,
		}
		if row.InvoiceID != "" {
			wallet.InvoiceID = &row.InvoiceID
		}
		if row.ExpectedAmountCents.Valid {
			wallet.ExpectedAmountCents = &row.ExpectedAmountCents.Int64
		}
		if row.ExpectedAmountBaseUnits != "" {
			wallet.ExpectedAmountBaseUnits, _ = new(big.Int).SetString(row.ExpectedAmountBaseUnits, 10)
		}
		if row.QuotedPriceUsd != "" {
			wallet.QuotedPriceUSD, _ = decimal.NewFromString(row.QuotedPriceUsd)
		}
		if row.QuotedUsdToEurRate != "" {
			value, parseErr := decimal.NewFromString(row.QuotedUsdToEurRate)
			if parseErr == nil {
				wallet.QuotedUSDToEURRate = &value
			}
		}
		wallet.QuoteSource = row.QuoteSource
		wallet.CreditedAmountCurrency = row.CreditedAmountCurrency
		wallet.ClientIP = row.ClientIp
		if row.InvoiceCurrency != "" {
			wallet.InvoiceAmount = &row.InvoiceAmount
			wallet.InvoiceCurrency = &row.InvoiceCurrency
		}
		cm.allocateObservedDeposit(ctx, row.EventID, row.TxHash, row.BlockNumber, row.AmountBaseUnits, wallet)
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
		_ = purserdb.New(cm.db).MarkCryptoDepositAllocationReview(ctx, purserdb.MarkCryptoDepositAllocationReviewParams{
			WalletID: wallet.ID, AllocationError: sql.NullString{String: "deposit amount does not match allocatable quote", Valid: true}, EventID: eventID,
		})
		return
	}
	if wallet.Purpose == "invoice" && match.txBaseUnits.Cmp(wallet.ExpectedAmountBaseUnits) < 0 {
		cm.markDepositForReview(wallet, tx, match.txBaseUnits, "invoice payment is below the exact quote")
		_ = purserdb.New(cm.db).MarkCryptoDepositAllocationReview(ctx, purserdb.MarkCryptoDepositAllocationReviewParams{
			WalletID: wallet.ID, AllocationError: sql.NullString{String: "invoice payment is below the exact quote", Valid: true}, EventID: eventID,
		})
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
