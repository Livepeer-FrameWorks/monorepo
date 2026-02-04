package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/logging"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/prometheus/client_golang/prometheus"
)

// GasWalletMonitor monitors the gas wallet balance across all x402-enabled networks
type GasWalletMonitor struct {
	logger          logging.Logger
	address         string
	includeTestnets bool
	stopCh          chan struct{}

	// Cached balances
	mu       sync.RWMutex
	balances map[string]*GasWalletBalance

	// Prometheus metrics
	balanceGauge    *prometheus.GaugeVec
	lowBalanceGauge *prometheus.GaugeVec
}

// GasWalletBalance represents the balance on a specific network
type GasWalletBalance struct {
	Network     string    `json:"network"`
	DisplayName string    `json:"display_name"`
	ChainID     int64     `json:"chain_id"`
	Address     string    `json:"address"`
	BalanceWei  string    `json:"balance_wei"`
	BalanceETH  float64   `json:"balance_eth"`
	IsLow       bool      `json:"is_low"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// LowBalanceThreshold is the minimum ETH balance before alerting (0.005 ETH)
const LowBalanceThreshold = 0.005

// NewGasWalletMonitor creates a new gas wallet monitor
func NewGasWalletMonitor(log logging.Logger) *GasWalletMonitor {
	privKey := os.Getenv("X402_GAS_WALLET_PRIVKEY")
	address := os.Getenv("X402_GAS_WALLET_ADDRESS")
	includeTestnets := os.Getenv("X402_INCLUDE_TESTNETS") == "true"

	// Derive address from private key if not provided
	if address == "" && privKey != "" {
		privKeyHex := strings.TrimPrefix(privKey, "0x")
		key, err := crypto.HexToECDSA(privKeyHex)
		if err == nil {
			address = strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
		}
	}

	// Create Prometheus gauge for balance monitoring
	balanceGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gas_wallet_balance_eth",
			Help: "Gas wallet ETH balance by network",
		},
		[]string{"network", "chain_id"},
	)
	// Register metric (ignore if already registered)
	prometheus.Register(balanceGauge) //nolint:errcheck // duplicate registration is fine
	lowBalanceGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gas_wallet_balance_low",
			Help: "Gas wallet low balance indicator (1=low, 0=ok)",
		},
		[]string{"network", "chain_id"},
	)
	prometheus.Register(lowBalanceGauge) //nolint:errcheck // duplicate registration is fine

	return &GasWalletMonitor{
		logger:          log,
		address:         address,
		includeTestnets: includeTestnets,
		stopCh:          make(chan struct{}),
		balances:        make(map[string]*GasWalletBalance),
		balanceGauge:    balanceGauge,
		lowBalanceGauge: lowBalanceGauge,
	}
}

// Start begins monitoring gas wallet balances
func (m *GasWalletMonitor) Start(ctx context.Context) {
	if m.address == "" {
		m.logger.Warn("Gas wallet address not configured - monitoring disabled")
		return
	}

	networks := X402Networks(m.includeTestnets)
	m.logger.WithFields(logging.Fields{
		"address":       m.address,
		"network_count": len(networks),
	}).Info("Starting gas wallet monitor")

	// Initial check
	m.checkAllBalances()

	// Periodic checks every 5 minutes
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("Gas wallet monitor stopping due to context cancellation")
			return
		case <-m.stopCh:
			m.logger.Info("Gas wallet monitor stopping")
			return
		case <-ticker.C:
			m.checkAllBalances()
		}
	}
}

// Stop stops the gas wallet monitor
func (m *GasWalletMonitor) Stop() {
	close(m.stopCh)
}

// GetBalances returns the current cached balances for all networks
func (m *GasWalletMonitor) GetBalances() []*GasWalletBalance {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*GasWalletBalance
	for _, b := range m.balances {
		result = append(result, b)
	}
	return result
}

// GetBalance returns the cached balance for a specific network
func (m *GasWalletMonitor) GetBalance(network string) (*GasWalletBalance, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.balances[network]
	return b, ok
}

// HasLowBalance returns true if any network has a low balance
func (m *GasWalletMonitor) HasLowBalance() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, b := range m.balances {
		if b.IsLow {
			return true
		}
	}
	return false
}

// checkAllBalances checks balances on all x402-enabled networks
func (m *GasWalletMonitor) checkAllBalances() {
	networks := X402Networks(m.includeTestnets)

	for _, network := range networks {
		balance, err := m.getBalance(network)
		if err != nil {
			m.logger.WithFields(logging.Fields{
				"error":   err,
				"network": network.Name,
			}).Error("Failed to get gas wallet balance")
			continue
		}

		m.mu.Lock()
		m.balances[network.Name] = balance
		m.mu.Unlock()

		// Update Prometheus metric
		m.balanceGauge.WithLabelValues(network.Name, fmt.Sprintf("%d", network.ChainID)).Set(balance.BalanceETH)
		lowValue := 0.0
		if balance.IsLow {
			lowValue = 1.0
		}
		m.lowBalanceGauge.WithLabelValues(network.Name, fmt.Sprintf("%d", network.ChainID)).Set(lowValue)

		// Log warning if balance is low
		if balance.IsLow {
			m.logger.WithFields(logging.Fields{
				"network":     network.Name,
				"balance_eth": balance.BalanceETH,
				"threshold":   LowBalanceThreshold,
			}).Warn("Gas wallet balance is LOW - needs refill")
		} else {
			m.logger.WithFields(logging.Fields{
				"network":     network.Name,
				"balance_eth": balance.BalanceETH,
			}).Debug("Gas wallet balance checked")
		}
	}
}

// getBalance fetches the ETH balance for the gas wallet on a specific network
func (m *GasWalletMonitor) getBalance(network NetworkConfig) (*GasWalletBalance, error) {
	rpcEndpoint := network.GetRPCEndpointWithDefault()
	if rpcEndpoint == "" {
		return nil, fmt.Errorf("no RPC endpoint for network %s", network.Name)
	}

	// Call eth_getBalance via JSON-RPC
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "eth_getBalance",
		"params":  []interface{}{m.address, "latest"},
		"id":      1,
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

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
		Result string           `json:"result"`
		Error  *json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, err
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error: %s", string(*rpcResp.Error))
	}

	// Parse hex balance
	balanceWei := new(big.Int)
	balanceWei.SetString(strings.TrimPrefix(rpcResp.Result, "0x"), 16)

	// Convert to ETH (float64)
	balanceFloat := new(big.Float).SetInt(balanceWei)
	divisor := new(big.Float).SetFloat64(1e18)
	balanceFloat.Quo(balanceFloat, divisor)
	balanceETH, _ := balanceFloat.Float64()

	return &GasWalletBalance{
		Network:     network.Name,
		DisplayName: network.DisplayName,
		ChainID:     network.ChainID,
		Address:     m.address,
		BalanceWei:  balanceWei.String(),
		BalanceETH:  balanceETH,
		IsLow:       balanceETH < LowBalanceThreshold,
		UpdatedAt:   time.Now(),
	}, nil
}

// RefreshBalances forces an immediate balance check
func (m *GasWalletMonitor) RefreshBalances() {
	m.checkAllBalances()
}
