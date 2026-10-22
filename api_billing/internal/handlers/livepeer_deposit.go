package handlers

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"frameworks/api_billing/internal/appconfig"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	livepeerchain "github.com/Livepeer-FrameWorks/monorepo/pkg/livepeer/chain"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/prometheus/client_golang/prometheus"
)

// LivepeerDepositMonitor monitors Livepeer gateway TicketBroker deposits on Arbitrum.
// When a gateway's on-chain deposit or reserve is low, Purser funds the
// TicketBroker directly. The gateway wallet receives no routine native ETH.
//
// Follows the GasWalletMonitor pattern.
type LivepeerDepositMonitor struct {
	logger logging.Logger
	qm     livepeerServiceDiscoveryClient
	db     *sql.DB

	// Funding wallet (same as x402 gas wallet)
	gasWalletPrivKey string
	gasWalletAddress string

	// Config
	depositLowThreshold float64 // TicketBroker deposit below this triggers Purser top-up (default 0.1 ETH)
	reserveLowThreshold float64
	topupAmountWei      *big.Int // How much ETH Purser sends per top-up (default 0.2 ETH)
	dailyCapWei         *big.Int
	pollInterval        time.Duration
	rpcEndpoint         string
	receiptTimeout      time.Duration
	receiptPollInterval time.Duration

	stopCh chan struct{}

	// Cached state
	mu       sync.RWMutex
	gateways map[string]*GatewayDepositState

	// Prometheus metrics
	depositGauge *prometheus.GaugeVec
	reserveGauge *prometheus.GaugeVec
	ethGauge     *prometheus.GaugeVec
	topupCounter prometheus.Counter
	alertCounter *prometheus.CounterVec
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
	ReserveLow bool      `json:"reserve_low"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type livepeerServiceDiscoveryClient interface {
	ListOfficialClusters(ctx context.Context) (*quartermasterpb.ListClustersResponse, error)
	DiscoverServices(ctx context.Context, serviceType, clusterID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ServiceDiscoveryResponse, error)
}

var fundDepositAndReserveForSelector = common.Hex2Bytes("989f789c")

// NewLivepeerDepositMonitor creates a deposit monitor from the runtime
// configuration. When the monitor is enabled, invalid signing or RPC
// configuration is fatal at startup rather than silently degrading into
// read-only monitoring.
func NewLivepeerDepositMonitor(log logging.Logger, db *sql.DB, qm *qmclient.GRPCClient) (*LivepeerDepositMonitor, error) {
	rt := appconfig.Runtime()
	privKey := rt.X402GasWalletPrivkey
	address := rt.X402GasWalletAddress
	privKeyHex := strings.TrimPrefix(privKey, "0x")
	key, err := crypto.HexToECDSA(privKeyHex)
	if err != nil {
		return nil, fmt.Errorf("X402_GAS_WALLET_PRIVKEY is required and must be a valid secp256k1 private key: %w", err)
	}
	derivedAddress := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	if strings.TrimSpace(address) == "" {
		address = derivedAddress
	} else if !common.IsHexAddress(address) || !strings.EqualFold(address, derivedAddress) {
		return nil, fmt.Errorf("X402_GAS_WALLET_ADDRESS does not match X402_GAS_WALLET_PRIVKEY")
	}

	depositThreshold := 0.1
	if v := rt.LivepeerDepositLowThreshold; v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			depositThreshold = f
		} else {
			log.Warn("invalid LIVEPEER_DEPOSIT_LOW_THRESHOLD, using default", "value", v, "error", err)
		}
	}

	topupETH := 0.2
	if v := rt.LivepeerTopupAmount; v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			topupETH = f
		} else {
			log.Warn("invalid LIVEPEER_TOPUP_AMOUNT, using default", "value", v, "error", err)
		}
	}
	topupWei := ethToWei(topupETH)
	dailyCapETH := 1.0
	if v := rt.LivepeerFundingDailyCap; v != "" {
		if f, parseErr := strconv.ParseFloat(v, 64); parseErr == nil && f > 0 {
			dailyCapETH = f
		} else {
			log.Warn("invalid LIVEPEER_FUNDING_DAILY_CAP, using default", "value", v, "error", parseErr)
		}
	}

	rpcEndpoint := rt.ArbitrumRPCEndpoint
	parsedRPC, err := url.Parse(rpcEndpoint)
	if err != nil || parsedRPC.Host == "" || (parsedRPC.Scheme != "http" && parsedRPC.Scheme != "https") {
		return nil, fmt.Errorf("ARBITRUM_RPC_ENDPOINT must be an absolute http(s) URL")
	}

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
	alertCounter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "livepeer_funding_alerts_total",
		Help: "Purser TicketBroker funding alerts by reason",
	}, []string{"reason"})
	prometheus.Register(alertCounter) //nolint:errcheck

	return &LivepeerDepositMonitor{
		logger:              log,
		db:                  db,
		qm:                  qm,
		gasWalletPrivKey:    privKey,
		gasWalletAddress:    address,
		depositLowThreshold: depositThreshold,
		reserveLowThreshold: depositThreshold,
		topupAmountWei:      topupWei,
		dailyCapWei:         ethToWei(dailyCapETH),
		pollInterval:        5 * time.Minute,
		rpcEndpoint:         rpcEndpoint,
		receiptTimeout:      2 * time.Minute,
		receiptPollInterval: 2 * time.Second,
		stopCh:              make(chan struct{}),
		gateways:            make(map[string]*GatewayDepositState),
		depositGauge:        depositGauge,
		reserveGauge:        reserveGauge,
		ethGauge:            ethGauge,
		topupCounter:        topupCounter,
		alertCounter:        alertCounter,
	}, nil
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
		label := state.Address

		m.mu.Lock()
		m.gateways[label] = state
		m.mu.Unlock()

		m.depositGauge.WithLabelValues(label).Set(state.DepositETH)
		m.reserveGauge.WithLabelValues(label).Set(state.ReserveETH)
		m.ethGauge.WithLabelValues(label).Set(state.BalanceETH)

		if state.DepositLow || state.ReserveLow {
			m.logger.WithFields(logging.Fields{
				"gateway":     label,
				"deposit_eth": state.DepositETH,
				"balance_eth": state.BalanceETH,
				"threshold":   m.depositLowThreshold,
			}).Warn("Livepeer gateway TicketBroker deposit is LOW")

			if m.gasWalletPrivKey != "" {
				m.fundTicketBroker(ctx, state, label)
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
	address string
}

// discoverGatewayAddresses finds livepeer-gateway instances via Quartermaster
// and resolves each wallet address from service instance metadata. Gateways
// are pool-assigned to the media clusters they serve, which are not Purser's
// own cluster, so every platform-official cluster is queried; a cluster whose
// discovery fails is logged and skipped so the others are still monitored.
func (m *LivepeerDepositMonitor) discoverGatewayAddresses(ctx context.Context) []discoveredGateway {
	if m.qm == nil {
		return nil
	}

	clusters, err := m.qm.ListOfficialClusters(ctx)
	if err != nil {
		m.logger.WithError(err).Error("Failed to list official clusters for livepeer-gateway discovery")
		return nil
	}

	var instances []*quartermasterpb.ServiceInstance
	for _, cluster := range clusters.GetClusters() {
		clusterID := cluster.GetClusterId()
		if clusterID == "" {
			continue
		}
		resp, discoverErr := m.qm.DiscoverServices(ctx, "livepeer-gateway", clusterID, nil)
		if discoverErr != nil {
			m.logger.WithError(discoverErr).WithField("cluster_id", clusterID).Error("Failed to discover livepeer-gateway instances")
			continue
		}
		instances = append(instances, resp.GetInstances()...)
	}

	seen := make(map[string]bool)
	var gateways []discoveredGateway
	for _, inst := range instances {
		if inst.Status != "running" {
			continue
		}
		host := inst.GetHost()
		port := inst.GetPort()
		if host == "" {
			continue
		}
		addr := strings.TrimSpace(inst.GetMetadata()[servicedefs.LivepeerGatewayMetadataWalletAddress])
		if addr == "" {
			m.logger.WithFields(logging.Fields{
				"gateway": fmt.Sprintf("%s:%d", host, port),
			}).Warn("Livepeer gateway missing wallet_address metadata")
			continue
		}
		if !common.IsHexAddress(addr) {
			m.logger.WithField("gateway", host).Warn("Livepeer gateway has invalid wallet_address metadata")
			continue
		}
		addr = strings.ToLower(addr)
		if seen[addr] {
			continue
		}
		seen[addr] = true

		gateways = append(gateways, discoveredGateway{
			host:    host,
			port:    port,
			address: addr,
		})
	}

	return gateways
}

// queryGatewayState reads on-chain balance and TicketBroker deposit/reserve.
func (m *LivepeerDepositMonitor) queryGatewayState(ctx context.Context, ethAddress string) (*GatewayDepositState, error) {
	chainClient := livepeerchain.NewClient(m.rpcEndpoint, http.DefaultClient)
	balance, err := chainClient.ETHBalance(ctx, ethAddress)
	if err != nil {
		return nil, fmt.Errorf("eth_getBalance: %w", err)
	}

	senderInfo, err := chainClient.GetSenderInfo(ctx, ethAddress)
	if err != nil {
		return nil, fmt.Errorf("getSenderInfo: %w", err)
	}

	balanceETH := weiToETH(balance)
	depositETH := weiToETH(senderInfo.Deposit)
	reserveETH := weiToETH(senderInfo.Reserve)

	return &GatewayDepositState{
		Address:    ethAddress,
		DepositETH: depositETH,
		ReserveETH: reserveETH,
		BalanceETH: balanceETH,
		DepositLow: depositETH < m.depositLowThreshold,
		ReserveLow: reserveETH < m.reserveLowThreshold,
		UpdatedAt:  time.Now(),
	}, nil
}

func (m *LivepeerDepositMonitor) fundTicketBroker(ctx context.Context, state *GatewayDepositState, label string) {
	if fundingAmounts(state, m.topupAmountWei).total.Sign() <= 0 {
		return
	}
	signer, unlockSigner, err := m.lockFundingSigner(ctx)
	if err != nil {
		m.alertCounter.WithLabelValues("signer_lock").Inc()
		m.logger.WithError(err).WithField("gateway", label).Error("Livepeer TicketBroker funding blocked by signer lock")
		return
	}
	defer unlockSigner()
	if idleErr := m.checkFundingWalletIdle(ctx); idleErr != nil {
		m.alertCounter.WithLabelValues("pending_transaction").Inc()
		m.logger.WithError(idleErr).WithField("gateway", label).Error("Livepeer TicketBroker funding skipped")
		return
	}
	m.recheckUnconfirmedFundingAttempt(ctx)
	// Every Purser replica runs this monitor. The low reading was taken before
	// the lock, so another replica may have funded the gateway while this one
	// waited; only a reading taken under the lock decides the top-up.
	fresh, err := m.queryGatewayState(ctx, state.Address)
	if err != nil {
		m.logger.WithError(err).WithField("gateway", label).Error("Livepeer TicketBroker funding skipped: gateway state could not be re-read under the signer lock")
		return
	}
	amounts := fundingAmounts(fresh, m.topupAmountWei)
	if amounts.total.Sign() <= 0 {
		m.logger.WithFields(logging.Fields{
			"gateway":     label,
			"deposit_eth": fresh.DepositETH,
			"reserve_eth": fresh.ReserveETH,
		}).Info("Livepeer TicketBroker funding skipped: deposit and reserve are no longer low")
		return
	}
	deposit, reserve, total := amounts.deposit, amounts.reserve, amounts.total
	attemptID, repeated, err := m.reserveFundingAttempt(ctx, signer, state.Address, deposit, reserve)
	if errors.Is(err, errFundingSignerLockLost) || errors.Is(err, errFundingAttemptInFlight) {
		reason := "signer_lock"
		if errors.Is(err, errFundingAttemptInFlight) {
			reason = "pending_transaction"
		}
		m.alertCounter.WithLabelValues(reason).Inc()
		m.logger.WithError(err).WithField("gateway", label).Error("Livepeer TicketBroker funding skipped")
		return
	}
	if err != nil {
		m.alertCounter.WithLabelValues("daily_cap_or_ledger").Inc()
		m.logger.WithError(err).WithField("gateway", label).Error("Livepeer TicketBroker funding blocked by daily policy")
		return
	}
	if repeated {
		m.alertCounter.WithLabelValues("repeated_burn").Inc()
		m.logger.WithField("gateway", label).Error("Livepeer gateway required repeated TicketBroker funding within 24 hours")
	}
	m.logger.WithFields(logging.Fields{
		"gateway":     label,
		"deposit_wei": deposit.String(),
		"reserve_wei": reserve.String(),
	}).Info("Funding Livepeer TicketBroker deposit and reserve")

	txHash, err := m.sendTicketBrokerFunding(ctx, state.Address, deposit, reserve)
	if err != nil {
		m.finishFundingAttempt(ctx, attemptID, txHash, err)
		m.logger.WithFields(logging.Fields{
			"error":   err,
			"gateway": label,
		}).Error("Failed to fund Livepeer TicketBroker")
		return
	}

	m.finishFundingAttempt(ctx, attemptID, txHash, nil)
	m.topupCounter.Inc()
	m.logger.WithFields(logging.Fields{
		"gateway": label,
		"tx_hash": txHash,
		"amount":  weiToETH(total),
	}).Info("Livepeer TicketBroker funding transaction sent")
}

type ticketBrokerFunding struct {
	deposit, reserve, total *big.Int
}

// fundingAmounts tops up each of deposit and reserve that reads low by topup.
func fundingAmounts(state *GatewayDepositState, topup *big.Int) ticketBrokerFunding {
	f := ticketBrokerFunding{deposit: new(big.Int), reserve: new(big.Int)}
	if state.DepositLow && topup != nil {
		f.deposit.Set(topup)
	}
	if state.ReserveLow && topup != nil {
		f.reserve.Set(topup)
	}
	f.total = new(big.Int).Add(new(big.Int).Set(f.deposit), f.reserve)
	return f
}

// lockFundingSigner serializes nonce allocation and submission across Purser
// replicas that share the funding wallet. PostgreSQL releases this session lock
// automatically if the process or connection disappears, so a session whose
// lock or unlock statement fails is discarded rather than pooled. The returned
// session is the one holding the lock; the funding attempt is reserved on it so
// reserveFundingAttempt can prove the hold.
func (m *LivepeerDepositMonitor) lockFundingSigner(ctx context.Context) (*sql.Conn, func(), error) {
	if m.db == nil {
		return nil, nil, fmt.Errorf("durable Livepeer funding signer lock unavailable")
	}
	conn, err := m.db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext('purser-livepeer-funding-signer'))`); err != nil {
		database.DiscardSession(conn)
		return nil, nil, err
	}
	lock := database.AdvisoryLock{UnlockSQL: `SELECT pg_advisory_unlock(hashtext('purser-livepeer-funding-signer'))`}
	return conn, func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := database.ReleaseSessionLock(unlockCtx, conn, lock); err != nil {
			m.logger.WithError(err).Error("Failed to release Livepeer funding signer lock; discarded its session")
		}
	}, nil
}

func (m *LivepeerDepositMonitor) sendTicketBrokerFunding(ctx context.Context, gateway string, deposit, reserve *big.Int) (string, error) {
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

	callData := fundDepositAndReserveForCallData(gateway, deposit, reserve)
	value := new(big.Int).Add(new(big.Int).Set(deposit), reserve)
	var gasHex string
	if err = m.arbRPCCall(ctx, "eth_estimateGas", []interface{}{map[string]string{
		"from": m.gasWalletAddress, "to": livepeerchain.TicketBrokerAddress,
		"value": "0x" + value.Text(16), "data": "0x" + hex.EncodeToString(callData),
	}}, &gasHex); err != nil {
		return "", fmt.Errorf("estimate TicketBroker funding gas: %w", err)
	}
	gas := new(big.Int)
	if _, ok := gas.SetString(strings.TrimPrefix(gasHex, "0x"), 16); !ok || !gas.IsUint64() {
		return "", fmt.Errorf("invalid estimated gas %q", gasHex)
	}
	gasLimit := gas.Uint64() + gas.Uint64()/5
	chainID := big.NewInt(42161) // Arbitrum One

	toAddr := common.HexToAddress(livepeerchain.TicketBrokerAddress)
	tx := types.NewTransaction(nonce.Uint64(), toAddr, value, gasLimit, gasPrice, callData)
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
	if !common.IsHexHash(txHash) {
		return txHash, fmt.Errorf("send tx returned invalid hash %q", txHash)
	}
	if err := m.waitForFundingReceipt(ctx, txHash); err != nil {
		return txHash, err
	}
	return txHash, nil
}

func (m *LivepeerDepositMonitor) fundingReceiptTimeout() time.Duration {
	if m.receiptTimeout <= 0 {
		return 2 * time.Minute
	}
	return m.receiptTimeout
}

// fundingAttemptInFlightWindow is how long after its reservation an attempt
// without a recorded result still counts as being submitted. It outlasts the
// four 15 s RPC calls before the receipt wait, the wait itself and the result
// write, so only an attempt whose holder died before recording a result
// expires.
func (m *LivepeerDepositMonitor) fundingAttemptInFlightWindow() time.Duration {
	return m.fundingReceiptTimeout() + 2*time.Minute
}

func (m *LivepeerDepositMonitor) waitForFundingReceipt(ctx context.Context, txHash string) error {
	timeout := m.fundingReceiptTimeout()
	interval := m.receiptPollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		mined, outcome, err := m.fundingReceipt(waitCtx, txHash)
		if err != nil {
			if waitCtx.Err() != nil {
				return fmt.Errorf("%w: %s: %w", errFundingUnconfirmed, txHash, waitCtx.Err())
			}
			return fmt.Errorf("%w: %s: %w", errFundingUnconfirmed, txHash, err)
		}
		if mined {
			return outcome
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("%w: %s: %w", errFundingUnconfirmed, txHash, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

// fundingReceipt reads txHash's receipt once. mined is false while the
// transaction has no receipt; outcome is nil for a mined transaction that
// succeeded and describes one that did not; err is a failed read.
func (m *LivepeerDepositMonitor) fundingReceipt(ctx context.Context, txHash string) (mined bool, outcome, err error) {
	var raw json.RawMessage
	if err := m.arbRPCCall(ctx, "eth_getTransactionReceipt", []interface{}{txHash}, &raw); err != nil {
		return false, nil, fmt.Errorf("get TicketBroker funding receipt: %w", err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return false, nil, nil
	}
	var receipt struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return false, nil, fmt.Errorf("decode TicketBroker funding receipt: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(receipt.Status)) {
	case "0x1":
		return true, nil, nil
	case "0x0":
		return true, fmt.Errorf("TicketBroker funding transaction %s reverted", txHash), nil
	default:
		return true, fmt.Errorf("TicketBroker funding transaction %s returned invalid receipt status %q", txHash, receipt.Status), nil
	}
}

// checkFundingWalletIdle refuses while the funding wallet has a transaction
// that is not mined yet: its pending nonce runs ahead of its latest one. A
// top-up whose send or receipt wait failed can still be in the mempool, and
// a new one would take the next nonce and fund the gateway twice.
func (m *LivepeerDepositMonitor) checkFundingWalletIdle(ctx context.Context) error {
	nonces := make(map[string]*big.Int, 2)
	for _, block := range []string{"latest", "pending"} {
		var nonceHex string
		if err := m.arbRPCCall(ctx, "eth_getTransactionCount", []interface{}{m.gasWalletAddress, block}, &nonceHex); err != nil {
			return fmt.Errorf("read the funding wallet's %s nonce: %w", block, err)
		}
		nonce, ok := new(big.Int).SetString(strings.TrimPrefix(nonceHex, "0x"), 16)
		if !ok {
			return fmt.Errorf("invalid %s nonce %q for the funding wallet", block, nonceHex)
		}
		nonces[block] = nonce
	}
	if nonces["pending"].Cmp(nonces["latest"]) > 0 {
		return fmt.Errorf("%w: pending nonce %s, latest nonce %s", errFundingWalletPending, nonces["pending"], nonces["latest"])
	}
	return nil
}

// recheckUnconfirmedFundingAttempt reads the receipt of the most recent
// attempt whose receipt wait ended without one, and records a receipt it now
// finds, so a mined top-up stops counting as in flight. An attempt still
// without a receipt is left as it is. Only attempts inside
// fundingAttemptInFlightWindow are read, since only those block funding.
func (m *LivepeerDepositMonitor) recheckUnconfirmedFundingAttempt(ctx context.Context) {
	if m.db == nil {
		return
	}
	var id int64
	var txHash string
	err := m.db.QueryRowContext(ctx, `SELECT id, tx_hash FROM purser.livepeer_funding_attempts WHERE funding_day IN ((CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date, (CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date - 1) AND created_at > CURRENT_TIMESTAMP - make_interval(secs => $2::double precision) AND tx_hash IS NOT NULL AND error_message LIKE $1 ORDER BY created_at DESC LIMIT 1`, fundingUnconfirmedPattern, m.fundingAttemptInFlightWindow().Seconds()).Scan(&id, &txHash)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		m.logger.WithError(err).Warn("Failed to read the last unconfirmed Livepeer funding attempt")
		return
	}
	mined, outcome, err := m.fundingReceipt(ctx, txHash)
	if err != nil {
		m.logger.WithError(err).WithField("tx_hash", txHash).Warn("Failed to re-read an unconfirmed Livepeer funding receipt")
		return
	}
	if !mined {
		return
	}
	result := ""
	if outcome != nil {
		result = outcome.Error()
	}
	if _, err := m.db.ExecContext(ctx, `UPDATE purser.livepeer_funding_attempts SET error_message = NULLIF($2, ''), updated_at = NOW() WHERE id = $1 AND error_message LIKE $3`, id, result, fundingUnconfirmedPattern); err != nil {
		m.logger.WithError(err).WithField("attempt_id", id).Error("Failed to record a Livepeer funding receipt found on re-check")
	}
}

func fundDepositAndReserveForCallData(gateway string, deposit, reserve *big.Int) []byte {
	callData := append([]byte{}, fundDepositAndReserveForSelector...)
	callData = append(callData, common.LeftPadBytes(common.HexToAddress(gateway).Bytes(), 32)...)
	callData = append(callData, common.LeftPadBytes(deposit.Bytes(), 32)...)
	return append(callData, common.LeftPadBytes(reserve.Bytes(), 32)...)
}

// fundingUnconfirmedMessage starts the recorded error of an attempt whose
// transaction was sent without its receipt being read. The ledger has no
// status column, so such an attempt is told apart from a finished one by
// this prefix.
const (
	fundingUnconfirmedMessage = "TicketBroker funding transaction not confirmed"
	fundingUnconfirmedPattern = fundingUnconfirmedMessage + "%"
)

var (
	errFundingSignerLockLost  = errors.New("this session no longer holds the Livepeer funding signer lock")
	errFundingAttemptInFlight = errors.New("another Livepeer funding attempt from the funding wallet may still be unmined")
	errFundingWalletPending   = errors.New("the funding wallet has an unmined transaction")
	errFundingUnconfirmed     = errors.New(fundingUnconfirmedMessage)
)

// reserveFundingAttempt records a funding attempt against the daily cap. It
// runs on signer, the session lockFundingSigner returned. YugabyteDB can drop
// that session lock, for one when its lock transaction expires in a storage
// stall, while the session's ordinary writes keep working, and another replica
// then takes the lock. Two guards keep a second top-up from the shared wallet
// out: the transaction re-takes the signer key as a transaction lock, which
// another session's signer lock refuses and which keeps another session from
// taking the signer lock until the attempt is committed; and an attempt inside
// fundingAttemptInFlightWindow that has no recorded result, or whose
// transaction was sent without a receipt, blocks the next one, since its
// transaction may still be unmined. Attempts carry no wallet column:
// the signer lock and the ledger serve the one funding wallet a deployment
// configures.
func (m *LivepeerDepositMonitor) reserveFundingAttempt(ctx context.Context, signer database.PostgresTxBeginner, gateway string, deposit, reserve *big.Int) (int64, bool, error) {
	if signer == nil || m.dailyCapWei == nil || m.dailyCapWei.Sign() <= 0 {
		return 0, false, fmt.Errorf("durable Livepeer funding ledger unavailable")
	}
	inFlightSeconds := m.fundingAttemptInFlightWindow().Seconds()
	var id int64
	var repeated bool
	err := database.WithRetryablePostgresTxBeginner(ctx, signer, nil, func(tx *sql.Tx) error {
		var holdsSigner bool
		if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtext('purser-livepeer-funding-signer'))`).Scan(&holdsSigner); err != nil {
			return err
		}
		if !holdsSigner {
			return errFundingSignerLockLost
		}
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('purser-livepeer-funding-daily'))`); err != nil {
			return err
		}
		// The window is minutes long, so an attempt inside it was reserved on
		// today's or yesterday's funding day; naming both days keeps the read
		// on the funding day index.
		var inFlight bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM purser.livepeer_funding_attempts WHERE funding_day IN ((CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date, (CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date - 1) AND created_at > CURRENT_TIMESTAMP - make_interval(secs => $1::double precision) AND ((tx_hash IS NULL AND error_message IS NULL) OR (tx_hash IS NOT NULL AND error_message LIKE $2)))`, inFlightSeconds, fundingUnconfirmedPattern).Scan(&inFlight); err != nil {
			return err
		}
		if inFlight {
			return errFundingAttemptInFlight
		}
		var spentText string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_wei), 0)::text FROM purser.livepeer_funding_attempts WHERE funding_day = (CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date`).Scan(&spentText); err != nil {
			return err
		}
		spent, ok := new(big.Int).SetString(spentText, 10)
		if !ok {
			return fmt.Errorf("invalid funding ledger total %q", spentText)
		}
		amount := new(big.Int).Add(new(big.Int).Set(deposit), reserve)
		if new(big.Int).Add(spent, amount).Cmp(m.dailyCapWei) > 0 {
			return fmt.Errorf("daily Livepeer funding cap exceeded: spent=%s requested=%s cap=%s", spent, amount, m.dailyCapWei)
		}
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM purser.livepeer_funding_attempts WHERE gateway_address = $1 AND created_at > NOW() - INTERVAL '24 hours')`, strings.ToLower(gateway)).Scan(&repeated); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `INSERT INTO purser.livepeer_funding_attempts (gateway_address, amount_wei, deposit_wei, reserve_wei) VALUES ($1, $2::numeric, $3::numeric, $4::numeric) RETURNING id`, strings.ToLower(gateway), amount.String(), deposit.String(), reserve.String()).Scan(&id)
	})
	if err != nil {
		return 0, false, err
	}
	return id, repeated, nil
}

func (m *LivepeerDepositMonitor) finishFundingAttempt(ctx context.Context, id int64, txHash string, fundingErr error) {
	if m.db == nil || id == 0 {
		return
	}
	errorMessage := ""
	if fundingErr != nil {
		errorMessage = fundingErr.Error()
	}
	if _, err := m.db.ExecContext(ctx, `UPDATE purser.livepeer_funding_attempts SET tx_hash = NULLIF($2, ''), error_message = NULLIF($3, ''), updated_at = NOW() WHERE id = $1`, id, txHash, errorMessage); err != nil {
		m.logger.WithError(err).WithField("attempt_id", id).Error("Failed to record Livepeer funding transaction result")
	}
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
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("RPC HTTP status %s", resp.Status)
	}

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
