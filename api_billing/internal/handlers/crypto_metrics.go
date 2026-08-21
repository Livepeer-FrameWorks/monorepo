package handlers

import (
	"context"
	"math/big"
	"os"
	"strings"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/ethereum/go-ethereum/crypto"
)

func (cm *CryptoMonitor) refreshCryptoCustodyMetrics(ctx context.Context) {
	if cm.metrics == nil {
		return
	}
	queries := purserdb.New(cm.db)
	if cm.metrics.CryptoUnsweptBaseUnits != nil && cm.metrics.CryptoOldestUnswept != nil {
		cm.metrics.CryptoUnsweptBaseUnits.Reset()
		cm.metrics.CryptoOldestUnswept.Reset()
		rows, err := queries.ListCryptoUnsweptMetrics(ctx)
		if err == nil {
			for _, row := range rows {
				cm.metrics.CryptoUnsweptBaseUnits.WithLabelValues(row.Network, row.Asset).Set(row.Amount)
				cm.metrics.CryptoOldestUnswept.WithLabelValues(row.Network, row.Asset).Set(row.AgeSeconds)
			}
		}
	}
	if cm.metrics.CryptoFailedSweepItems != nil {
		cm.metrics.CryptoFailedSweepItems.Reset()
		rows, err := queries.ListCryptoFailedSweepMetrics(ctx)
		if err == nil {
			for _, row := range rows {
				cm.metrics.CryptoFailedSweepItems.WithLabelValues(row.Network, row.Asset).Set(row.FailedCount)
			}
		}
	}
	if cm.metrics.CryptoRelayerBalanceETH != nil {
		cm.metrics.CryptoRelayerBalanceETH.Reset()
		for _, network := range DepositNetworks(cm.includeTestnets) {
			keyName := cryptoNetworkEnvKey("CRYPTO_SWEEP_RELAYER_PRIVATE_KEY", network.Name)
			key, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(os.Getenv(keyName)), "0x"))
			if err != nil {
				continue
			}
			address := crypto.PubkeyToAddress(key.PublicKey).Hex()
			var encoded string
			if err := cm.rpc.Call(ctx, network, "eth_getBalance", []any{address, "latest"}, &encoded); err != nil {
				continue
			}
			wei := new(big.Int)
			if _, ok := wei.SetString(strings.TrimPrefix(encoded, "0x"), 16); !ok {
				continue
			}
			balance, _ := new(big.Float).Quo(new(big.Float).SetInt(wei), big.NewFloat(1e18)).Float64()
			cm.metrics.CryptoRelayerBalanceETH.WithLabelValues(network.Name).Set(balance)
		}
	}
	cm.refreshCryptoPaymentStateMetrics(ctx)
}

func (cm *CryptoMonitor) refreshCryptoPaymentStateMetrics(ctx context.Context) {
	queries := purserdb.New(cm.db)
	if cm.metrics.X402QuoteConversion != nil || cm.metrics.X402SettlementLatency != nil {
		if cm.metrics.X402QuoteConversion != nil {
			cm.metrics.X402QuoteConversion.Reset()
		}
		if cm.metrics.X402SettlementLatency != nil {
			cm.metrics.X402SettlementLatency.Reset()
		}
		rows, err := queries.ListX402QuoteMetrics(ctx)
		if err == nil {
			for _, row := range rows {
				if cm.metrics.X402QuoteConversion != nil {
					cm.metrics.X402QuoteConversion.WithLabelValues(row.Network).Set(row.ConversionRatio)
				}
				if cm.metrics.X402SettlementLatency != nil {
					cm.metrics.X402SettlementLatency.WithLabelValues(row.Network).Set(row.SettlementLatencySeconds)
				}
			}
		}
	}
	if cm.metrics.CryptoPendingDeposits != nil || cm.metrics.CryptoInvoiceReview != nil {
		if cm.metrics.CryptoPendingDeposits != nil {
			cm.metrics.CryptoPendingDeposits.Reset()
		}
		if cm.metrics.CryptoInvoiceReview != nil {
			cm.metrics.CryptoInvoiceReview.Reset()
		}
		rows, err := queries.ListCryptoPendingDepositMetrics(ctx)
		if err == nil {
			invoiceReview := map[string]float64{}
			for _, row := range rows {
				if cm.metrics.CryptoPendingDeposits != nil {
					cm.metrics.CryptoPendingDeposits.WithLabelValues(row.Network, row.Status).Set(row.EventCount)
				}
				invoiceReview[row.Network] += row.InvoiceReviewCount
			}
			if cm.metrics.CryptoInvoiceReview != nil {
				for network, count := range invoiceReview {
					cm.metrics.CryptoInvoiceReview.WithLabelValues(network).Set(count)
				}
			}
		}
	}
	if cm.metrics.CryptoAccountingAnomalies != nil || cm.metrics.CryptoAnomalyOldest != nil {
		if cm.metrics.CryptoAccountingAnomalies != nil {
			cm.metrics.CryptoAccountingAnomalies.Reset()
		}
		if cm.metrics.CryptoAnomalyOldest != nil {
			cm.metrics.CryptoAnomalyOldest.Reset()
		}
		rows, err := queries.ListCryptoAccountingAnomalyMetrics(ctx)
		if err == nil {
			for _, row := range rows {
				if cm.metrics.CryptoAccountingAnomalies != nil {
					cm.metrics.CryptoAccountingAnomalies.WithLabelValues(row.Kind).Set(row.AnomalyCount)
				}
				if cm.metrics.CryptoAnomalyOldest != nil {
					cm.metrics.CryptoAnomalyOldest.WithLabelValues(row.Kind).Set(row.AgeSeconds)
				}
			}
		}
	}
	if cm.metrics.CryptoLedgerReversals != nil {
		cm.metrics.CryptoLedgerReversals.Reset()
		rows, err := queries.ListCryptoLedgerReversalMetrics(ctx)
		if err == nil {
			for _, row := range rows {
				cm.metrics.CryptoLedgerReversals.WithLabelValues(row.ReferenceType).Set(row.ReversalCount)
			}
		}
	}
}
