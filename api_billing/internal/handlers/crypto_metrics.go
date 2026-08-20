//nolint:sqlclosecheck // Each short-lived optional metric query is explicitly closed before the next query.
package handlers

import (
	"context"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
)

func (cm *CryptoMonitor) refreshCryptoCustodyMetrics(ctx context.Context) {
	if cm.metrics == nil {
		return
	}
	if cm.metrics.CryptoUnsweptBaseUnits != nil && cm.metrics.CryptoOldestUnswept != nil {
		cm.metrics.CryptoUnsweptBaseUnits.Reset()
		cm.metrics.CryptoOldestUnswept.Reset()
		rows, err := cm.db.QueryContext(ctx, `
			WITH unswept AS (
				SELECT w.network, w.asset, w.received_amount_base_units AS amount, w.completed_at AS received_at
				FROM purser.crypto_wallets w
				WHERE w.status = 'completed' AND w.received_amount_base_units > 0
				UNION ALL
				SELECT ca.network, 'USDC', q.amount_atomic, q.confirmed_at
				FROM purser.x402_payment_quotes q
				JOIN purser.crypto_custody_addresses ca
				  ON ca.source_kind = 'x402' AND ca.tenant_id = q.tenant_id
				 AND LOWER(ca.address) = LOWER(q.pay_to)
				 AND q.network = CASE ca.network
					WHEN 'base' THEN 'eip155:8453'
					WHEN 'arbitrum' THEN 'eip155:42161'
					WHEN 'base-sepolia' THEN 'eip155:84532'
					WHEN 'arbitrum-sepolia' THEN 'eip155:421614'
				 END
				WHERE q.status = 'confirmed'
				  AND NOT EXISTS (
					SELECT 1 FROM purser.crypto_sweep_sources ss
					JOIN purser.crypto_sweep_items si ON si.id = ss.item_id
					WHERE ss.source_type = 'x402_quote' AND ss.source_id = q.id
					  AND si.status = 'confirmed'
				  )
			)
			SELECT network, asset, SUM(amount)::float8,
			       EXTRACT(EPOCH FROM (NOW() - MIN(received_at)))::float8
			FROM unswept GROUP BY network, asset
		`)
		if err == nil {
			for rows.Next() {
				var network, asset string
				var amount, age float64
				if rows.Scan(&network, &asset, &amount, &age) == nil {
					cm.metrics.CryptoUnsweptBaseUnits.WithLabelValues(network, asset).Set(amount)
					cm.metrics.CryptoOldestUnswept.WithLabelValues(network, asset).Set(age)
				}
			}
			_ = rows.Close()
		}
	}
	if cm.metrics.CryptoFailedSweepItems != nil {
		cm.metrics.CryptoFailedSweepItems.Reset()
		rows, err := cm.db.QueryContext(ctx, `
			SELECT network, asset, COUNT(*)::float8
			FROM purser.crypto_sweep_items WHERE status = 'failed'
			GROUP BY network, asset
		`)
		if err == nil {
			for rows.Next() {
				var network, asset string
				var count float64
				if rows.Scan(&network, &asset, &count) == nil {
					cm.metrics.CryptoFailedSweepItems.WithLabelValues(network, asset).Set(count)
				}
			}
			_ = rows.Close()
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
	if cm.metrics.X402QuoteConversion != nil || cm.metrics.X402SettlementLatency != nil {
		if cm.metrics.X402QuoteConversion != nil {
			cm.metrics.X402QuoteConversion.Reset()
		}
		if cm.metrics.X402SettlementLatency != nil {
			cm.metrics.X402SettlementLatency.Reset()
		}
		rows, err := cm.db.QueryContext(ctx, `
			SELECT q.network,
			       COUNT(*) FILTER (WHERE q.status = 'confirmed')::float8 / NULLIF(COUNT(*), 0)::float8,
			       COALESCE(percentile_cont(0.95) WITHIN GROUP (
			           ORDER BY EXTRACT(EPOCH FROM (n.confirmed_at - n.settled_at))
			       ) FILTER (WHERE n.confirmed_at IS NOT NULL), 0)::float8
			FROM purser.x402_payment_quotes q
			LEFT JOIN purser.x402_nonces n ON n.quote_id = q.id
			WHERE q.created_at >= NOW() - INTERVAL '24 hours'
			GROUP BY q.network
		`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var network string
				var ratio, latency float64
				if rows.Scan(&network, &ratio, &latency) == nil {
					if cm.metrics.X402QuoteConversion != nil {
						cm.metrics.X402QuoteConversion.WithLabelValues(network).Set(ratio)
					}
					if cm.metrics.X402SettlementLatency != nil {
						cm.metrics.X402SettlementLatency.WithLabelValues(network).Set(latency)
					}
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
		rows, err := cm.db.QueryContext(ctx, `
			SELECT e.network, e.status, COUNT(*)::float8,
			       COUNT(*) FILTER (WHERE e.status = 'review_required' AND w.purpose = 'invoice')::float8
			FROM purser.crypto_deposit_events e
			LEFT JOIN purser.crypto_wallets w ON w.id = e.wallet_id
			WHERE e.status IN ('observed', 'confirmed', 'review_required')
			GROUP BY e.network, e.status
		`)
		if err == nil {
			defer rows.Close()
			invoiceReview := map[string]float64{}
			for rows.Next() {
				var network, status string
				var count, review float64
				if rows.Scan(&network, &status, &count, &review) == nil {
					if cm.metrics.CryptoPendingDeposits != nil {
						cm.metrics.CryptoPendingDeposits.WithLabelValues(network, status).Set(count)
					}
					invoiceReview[network] += review
				}
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
		rows, err := cm.db.QueryContext(ctx, `
			SELECT kind, COUNT(*)::float8,
			       EXTRACT(EPOCH FROM (NOW() - MIN(first_seen_at)))::float8
			FROM purser.crypto_accounting_anomalies
			WHERE status = 'open' GROUP BY kind
		`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var kind string
				var count, age float64
				if rows.Scan(&kind, &count, &age) == nil {
					if cm.metrics.CryptoAccountingAnomalies != nil {
						cm.metrics.CryptoAccountingAnomalies.WithLabelValues(kind).Set(count)
					}
					if cm.metrics.CryptoAnomalyOldest != nil {
						cm.metrics.CryptoAnomalyOldest.WithLabelValues(kind).Set(age)
					}
				}
			}
		}
	}
	if cm.metrics.CryptoLedgerReversals != nil {
		cm.metrics.CryptoLedgerReversals.Reset()
		rows, err := cm.db.QueryContext(ctx, `
			SELECT reference_type, COUNT(*)::float8
			FROM purser.balance_transactions
			WHERE transaction_type = 'reversal'
			  AND reference_type IN ('x402_failed', 'crypto_reorg', 'crypto_invoice_overpayment_reorg')
			GROUP BY reference_type
		`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var referenceType string
				var count float64
				if rows.Scan(&referenceType, &count) == nil {
					cm.metrics.CryptoLedgerReversals.WithLabelValues(referenceType).Set(count)
				}
			}
		}
	}
}
