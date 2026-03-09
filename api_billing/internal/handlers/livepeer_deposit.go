package handlers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/logging"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/prometheus/client_golang/prometheus"
)

// LivepeerDepositMonitor monitors Livepeer gateway TicketBroker deposits on Arbitrum.
// When a gateway's on-chain deposit drops below depositLowThreshold (0.1 ETH),
// Purser sends topupAmount (0.2 ETH) of native ETH to the gateway address.
// The gateway itself then auto-deposits balance minus a dust reserve into TicketBroker.
//
// Follows the GasWalletMonitor pattern.
type LivepeerDepositMonitor struct {
	logger logging.Logger
	qm     *qmclient.GRPCClient

	// Funding wallet (same as x402 gas wallet)
	gasWalletPrivKey string
	gasWalletAddress string

	// Config
	depositLowThreshold float64  // TicketBroker deposit below this triggers Purser top-up (default 0.1 ETH)
	topupAmountWei      *big.Int // How much ETH Purser sends per top-up (default 0.2 ETH)
	pollInterval        time.Duration
	clusterID           string
	rpcEndpoint         string

	stopCh chan struct{}

	// Cached state
	mu       sync.RWMutex
	gateways map[string]*GatewayDepositState

	// Prometheus metrics
	depositGauge *prometheus.GaugeVec
	reserveGauge *prometheus.GaugeVec
	ethGauge     *prometheus.GaugeVec
	topupCounter prometheus.Counter
}

// GatewayDepositState tracks the on-chain state for a single gateway
type GatewayDepositState struct {
	Address    string    `json:"address"`
	Host       string    `json:"host"`
	Port       int32     `json:"port"`
	DepositETH float64   `json:"deposit_eth"`
	ReserveETH float64   `json:"reserve_eth"`
	BalanceETH float64   `json:"balance_eth"`
	DepositLow bool      `json:"deposit_low"` // TicketBroker deposit below threshold
	UpdatedAt  time.Time `json:"updated_at"`
}

// TicketBroker contract on Arbitrum One (Livepeer protocol)
const ticketBrokerAddress = "0xa8bB618B1520E284046F3dFc448851A1Ff26e41B"

// getSenderInfo(address) selector: keccak256("getSenderInfo(address)")[:4]
var getSenderInfoSelector = common.Hex2Bytes("e7a47fa1")

// NewLivepeerDepositMonitor creates a deposit monitor from environment configuration.
func NewLivepeerDepositMonitor(log logging.Logger, qm *qmclient.GRPCClient) *LivepeerDepositMonitor {
	privKey := os.Getenv("X402_GAS_WALLET_PRIVKEY")
	address := os.Getenv("X402_GAS_WALLET_ADDRESS")

	if address == "" && privKey != "" {
		privKeyHex := strings.TrimPrefix(privKey, "0x")
		key, err := crypto.HexToECDSA(privKeyHex)
		if err == nil {
			address = strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
		}
	}

	depositThreshold := 0.1
	if v := os.Getenv("LIVEPEER_DEPOSIT_LOW_THRESHOLD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			depositThreshold = f
		} else {
			log.Warn("invalid LIVEPEER_DEPOSIT_LOW_THRESHOLD, using default", "value", v, "error", err)
		}
	}

	topupETH := 0.2
	if v := os.Getenv("LIVEPEER_TOPUP_AMOUNT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			topupETH = f
		} else {
			log.Warn("invalid LIVEPEER_TOPUP_AMOUNT, using default", "value", v, "error", err)
		}
	}
	topupWei := ethToWei(topupETH)

	rpcEndpoint := os.Getenv("ARBITRUM_RPC_ENDPOINT")
	clusterID := os.Getenv("CLUSTER_ID")

	depositGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "livepeer_deposit_eth",
			Help: "Livepeer gateway TicketBroker deposit balance in ETH",
		},
		[]string{"gateway"},
	)
	prometheus.Register(depositGauge) //nolint:errcheck

	reserveGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "livepeer_reserve_eth",
			Help: "Livepeer gateway TicketBroker reserve balance in ETH",
		},
		[]string{"gateway"},
	)
	prometheus.Register(reserveGauge) //nolint:errcheck

	ethGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "livepeer_eth_balance",
			Help: "Livepeer gateway native ETH balance on Arbitrum",
		},
		[]string{"gateway"},
	)
	prometheus.Register(ethGauge) //nolint:errcheck

	topupCounter := prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "livepeer_eth_topups_total",
			Help: "Number of ETH top-up transactions sent to Livepeer gateways",
		},
	)
	prometheus.Register(topupCounter) //nolint:errcheck

	return &LivepeerDepositMonitor{
		logger:              log,
		qm:                  qm,
		gasWalletPrivKey:    privKey,
		gasWalletAddress:    address,
		depositLowThreshold: depositThreshold,
		topupAmountWei:      topupWei,
		pollInterval:        5 * time.Minute,
		clusterID:           clusterID,
		rpcEndpoint:         rpcEndpoint,
		stopCh:              make(chan struct{}),
		gateways:            make(map[string]*GatewayDepositState),
		depositGauge:        depositGauge,
		reserveGauge:        reserveGauge,
		ethGauge:            ethGauge,
		topupCounter:        topupCounter,
	}
}

// Start begins the deposit monitoring loop.
func (m *LivepeerDepositMonitor) Start(ctx context.Context) {
	if m.rpcEndpoint == "" {
		m.logger.Warn("ARBITRUM_RPC_ENDPOINT not set - Livepeer deposit monitor disabled")
		return
	}

	m.logger.WithFields(logging.Fields{
		"deposit_low_threshold": m.depositLowThreshold,
		"topup_wei":             m.topupAmountWei.String(),
		"cluster_id":            m.clusterID,
	}).Info("Starting Livepeer deposit monitor")

	m.checkAll(ctx)

	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.checkAll(ctx)
		}
	}
}

// Stop stops the monitor.
func (m *LivepeerDepositMonitor) Stop() {
	close(m.stopCh)
}

// GetGateways returns cached gateway deposit states.
func (m *LivepeerDepositMonitor) GetGateways() []*GatewayDepositState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []*GatewayDepositState
	for _, g := range m.gateways {
		result = append(result, g)
	}
	return result
}

func (m *LivepeerDepositMonitor) checkAll(ctx context.Context) {
	addresses := m.discoverGatewayAddresses(ctx)
	if len(addresses) == 0 {
		m.logger.Debug("No Livepeer gateway instances found")
		return
	}

	for _, gw := range addresses {
		state, err := m.queryGatewayState(ctx, gw.address)
		if err != nil {
			m.logger.WithFields(logging.Fields{
				"error":   err,
				"gateway": gw.host,
			}).Error("Failed to query gateway on-chain state")
			continue
		}

		state.Host = gw.host
		state.Port = gw.port
		label := fmt.Sprintf("%s:%d", gw.host, gw.port)

		m.mu.Lock()
		m.gateways[label] = state
		m.mu.Unlock()

		m.depositGauge.WithLabelValues(label).Set(state.DepositETH)
		m.reserveGauge.WithLabelValues(label).Set(state.ReserveETH)
		m.ethGauge.WithLabelValues(label).Set(state.BalanceETH)

		if state.DepositLow {
			m.logger.WithFields(logging.Fields{
				"gateway":     label,
				"deposit_eth": state.DepositETH,
				"balance_eth": state.BalanceETH,
				"threshold":   m.depositLowThreshold,
			}).Warn("Livepeer gateway TicketBroker deposit is LOW")

			if m.gasWalletPrivKey != "" {
				m.sendTopup(ctx, state.Address, label)
			}
		} else {
			m.logger.WithFields(logging.Fields{
				"gateway":     label,
				"balance_eth": state.BalanceETH,
				"deposit_eth": state.DepositETH,
				"reserve_eth": state.ReserveETH,
			}).Debug("Livepeer gateway state checked")
		}
	}
}

type discoveredGateway struct {
	host    string
	port    int32
	address string // ETH address from /status
}

// discoverGatewayAddresses finds livepeer-gateway instances via Quartermaster
// and queries each one's /status to get its ETH address.
func (m *LivepeerDepositMonitor) discoverGatewayAddresses(ctx context.Context) []discoveredGateway {
	resp, err := m.qm.DiscoverServices(ctx, "livepeer-gateway", m.clusterID, nil)
	if err != nil {
		m.logger.WithError(err).Error("Failed to discover livepeer-gateway instances")
		return nil
	}

	var gateways []discoveredGateway
	for _, inst := range resp.Instances {
		if inst.Status != "running" {
			continue
		}
		host := inst.GetHost()
		port := inst.GetPort()
		if host == "" || port == 0 {
			continue
		}

		addr, err := m.getGatewayETHAddress(ctx, host, port)
		if err != nil {
			m.logger.WithFields(logging.Fields{
				"error":   err,
				"gateway": fmt.Sprintf("%s:%d", host, port),
			}).Warn("Could not get gateway ETH address from /status")
			continue
		}

		gateways = append(gateways, discoveredGateway{
			host:    host,
			port:    port,
			address: addr,
		})
	}

	return gateways
}

// getGatewayETHAddress queries the gateway's CLI/management port for its ETH address.
// go-livepeer's /status returns JSON with EthereumAddr field.
func (m *LivepeerDepositMonitor) getGatewayETHAddress(ctx context.Context, host string, port int32) (string, error) {
	url := fmt.Sprintf("http://%s:%d/status", host, port)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var status struct {
		EthereumAddr string `json:"EthereumAddr"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return "", err
	}
	if status.EthereumAddr == "" {
		return "", fmt.Errorf("empty EthereumAddr in /status response")
	}

	return status.EthereumAddr, nil
}

// queryGatewayState reads on-chain balance and TicketBroker deposit/reserve.
func (m *LivepeerDepositMonitor) queryGatewayState(ctx context.Context, ethAddress string) (*GatewayDepositState, error) {
	balance, err := m.getETHBalance(ctx, ethAddress)
	if err != nil {
		return nil, fmt.Errorf("eth_getBalance: %w", err)
	}

	deposit, reserve, err := m.getSenderInfo(ctx, ethAddress)
	if err != nil {
		return nil, fmt.Errorf("getSenderInfo: %w", err)
	}

	balanceETH := weiToETH(balance)
	depositETH := weiToETH(deposit)
	reserveETH := weiToETH(reserve)

	return &GatewayDepositState{
		Address:    ethAddress,
		DepositETH: depositETH,
		ReserveETH: reserveETH,
		BalanceETH: balanceETH,
		DepositLow: depositETH < m.depositLowThreshold,
		UpdatedAt:  time.Now(),
	}, nil
}

func (m *LivepeerDepositMonitor) getETHBalance(ctx context.Context, address string) (*big.Int, error) {
	var result string
	if err := m.arbRPCCall(ctx, "eth_getBalance", []interface{}{address, "latest"}, &result); err != nil {
		return nil, err
	}

	balance := new(big.Int)
	balance.SetString(strings.TrimPrefix(result, "0x"), 16)
	return balance, nil
}

// getSenderInfo calls TicketBroker.getSenderInfo(address) to get deposit and reserve.
// Returns (deposit, reserve, err).
// ABI: getSenderInfo(address) returns (SenderInfo { deposit, withdrawRound, ... }, ReserveInfo { funds, ... })
func (m *LivepeerDepositMonitor) getSenderInfo(ctx context.Context, address string) (*big.Int, *big.Int, error) {
	// ABI-encode: selector + padded address
	paddedAddr := common.LeftPadBytes(common.HexToAddress(address).Bytes(), 32)
	callData := append(getSenderInfoSelector, paddedAddr...)
	dataHex := "0x" + hex.EncodeToString(callData)

	var result string
	err := m.arbRPCCall(ctx, "eth_call", []interface{}{
		map[string]string{
			"to":   ticketBrokerAddress,
			"data": dataHex,
		},
		"latest",
	}, &result)
	if err != nil {
		return nil, nil, err
	}

	// Decode ABI response — getSenderInfo returns a tuple of two structs.
	// SenderInfo: (uint256 deposit, uint256 withdrawRound)
	// ReserveInfo: (uint256 fundsRemaining, uint256 claimedInCurrentRound)
	// ABI encoding puts all fields sequentially as 32-byte words.
	data := common.FromHex(result)
	if len(data) < 128 {
		return big.NewInt(0), big.NewInt(0), nil
	}

	deposit := new(big.Int).SetBytes(data[0:32])
	// data[32:64] = withdrawRound (skip)
	reserve := new(big.Int).SetBytes(data[64:96])
	// data[96:128] = claimedInCurrentRound (skip)

	return deposit, reserve, nil
}

// sendTopup sends native ETH from the gas wallet to the gateway address.
func (m *LivepeerDepositMonitor) sendTopup(ctx context.Context, toAddress string, label string) {
	m.logger.WithFields(logging.Fields{
		"gateway":    label,
		"to":         toAddress,
		"amount_wei": m.topupAmountWei.String(),
	}).Info("Sending ETH top-up to Livepeer gateway")

	txHash, err := m.sendETH(ctx, toAddress, m.topupAmountWei)
	if err != nil {
		m.logger.WithFields(logging.Fields{
			"error":   err,
			"gateway": label,
		}).Error("Failed to send ETH top-up")
		return
	}

	m.topupCounter.Inc()
	m.logger.WithFields(logging.Fields{
		"gateway": label,
		"tx_hash": txHash,
		"amount":  weiToETH(m.topupAmountWei),
	}).Info("ETH top-up sent to Livepeer gateway")
}

// sendETH signs and broadcasts a native ETH transfer on Arbitrum.
func (m *LivepeerDepositMonitor) sendETH(ctx context.Context, to string, value *big.Int) (string, error) {
	privKeyHex := strings.TrimPrefix(m.gasWalletPrivKey, "0x")
	privKey, err := crypto.HexToECDSA(privKeyHex)
	if err != nil {
		return "", fmt.Errorf("invalid private key: %w", err)
	}

	// Get nonce
	var nonceHex string
	if err = m.arbRPCCall(ctx, "eth_getTransactionCount", []interface{}{m.gasWalletAddress, "pending"}, &nonceHex); err != nil {
		return "", fmt.Errorf("get nonce: %w", err)
	}
	nonce := new(big.Int)
	nonce.SetString(strings.TrimPrefix(nonceHex, "0x"), 16)

	// Get gas price
	var gasPriceHex string
	if err = m.arbRPCCall(ctx, "eth_gasPrice", []interface{}{}, &gasPriceHex); err != nil {
		return "", fmt.Errorf("get gas price: %w", err)
	}
	gasPrice := new(big.Int)
	gasPrice.SetString(strings.TrimPrefix(gasPriceHex, "0x"), 16)

	// Simple ETH transfer: 21000 gas
	gasLimit := uint64(21000)
	chainID := big.NewInt(42161) // Arbitrum One

	toAddr := common.HexToAddress(to)
	tx := types.NewTransaction(nonce.Uint64(), toAddr, value, gasLimit, gasPrice, nil)
	signer := types.NewEIP155Signer(chainID)
	signedTx, err := types.SignTx(tx, signer, privKey)
	if err != nil {
		return "", fmt.Errorf("sign tx: %w", err)
	}

	raw, err := rlp.EncodeToBytes(signedTx)
	if err != nil {
		return "", fmt.Errorf("rlp encode: %w", err)
	}

	var txHash string
	if err := m.arbRPCCall(ctx, "eth_sendRawTransaction", []interface{}{"0x" + hex.EncodeToString(raw)}, &txHash); err != nil {
		return "", fmt.Errorf("send tx: %w", err)
	}

	return txHash, nil
}

// arbRPCCall makes a JSON-RPC call to the Arbitrum endpoint.
func (m *LivepeerDepositMonitor) arbRPCCall(ctx context.Context, method string, params interface{}, result interface{}) error {
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
		"id":      1,
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", m.rpcEndpoint, strings.NewReader(string(reqJSON)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var rpcResp struct {
		Result json.RawMessage  `json:"result"`
		Error  *json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return err
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("RPC error: %s", string(*rpcResp.Error))
	}

	return json.Unmarshal(rpcResp.Result, result)
}

func ethToWei(eth float64) *big.Int {
	f := new(big.Float).SetFloat64(eth)
	f.Mul(f, new(big.Float).SetFloat64(1e18))
	wei, _ := f.Int(nil)
	return wei
}

func weiToETH(wei *big.Int) float64 {
	f := new(big.Float).SetInt(wei)
	f.Quo(f, new(big.Float).SetFloat64(1e18))
	eth, _ := f.Float64()
	return eth
}
