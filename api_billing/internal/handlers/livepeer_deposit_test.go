package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"github.com/DATA-DOG/go-sqlmock"
	livepeerchain "github.com/Livepeer-FrameWorks/monorepo/pkg/livepeer/chain"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/fakesession"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const testFundingPrivateKey = "0000000000000000000000000000000000000000000000000000000000000001"

func TestNewLivepeerDepositMonitorValidatesFundingIdentity(t *testing.T) {
	appconfigtest.Set(t, "ARBITRUM_RPC_ENDPOINT", "https://arb.example")
	appconfigtest.Set(t, "X402_GAS_WALLET_PRIVKEY", testFundingPrivateKey)
	key, err := crypto.HexToECDSA(testFundingPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	address := crypto.PubkeyToAddress(key.PublicKey).Hex()
	appconfigtest.Set(t, "X402_GAS_WALLET_ADDRESS", address)

	monitor, err := NewLivepeerDepositMonitor(logging.NewLogger(), nil, nil)
	if err != nil {
		t.Fatalf("valid funding identity rejected: %v", err)
	}
	if !strings.EqualFold(monitor.gasWalletAddress, address) {
		t.Fatalf("funding address=%q, want %q", monitor.gasWalletAddress, address)
	}

	appconfigtest.Set(t, "X402_GAS_WALLET_ADDRESS", "0x1111111111111111111111111111111111111111")
	if _, err := NewLivepeerDepositMonitor(logging.NewLogger(), nil, nil); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched funding identity accepted: %v", err)
	}

	appconfigtest.Set(t, "X402_GAS_WALLET_PRIVKEY", "")
	if _, err := NewLivepeerDepositMonitor(logging.NewLogger(), nil, nil); err == nil {
		t.Fatal("missing funding private key accepted")
	}
}

func TestSendTicketBrokerFundingWaitsForSuccessfulReceipt(t *testing.T) {
	key, err := crypto.HexToECDSA(testFundingPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	gateway := "0x" + strings.Repeat("12", 20)
	deposit := big.NewInt(123)
	reserve := big.NewInt(456)
	var sentTx *types.Transaction
	receiptCalls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
			t.Errorf("decode RPC request: %v", decodeErr)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "eth_getTransactionCount":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x0"}`))
		case "eth_gasPrice":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
		case "eth_estimateGas":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x186a0"}`))
		case "eth_sendRawTransaction":
			var rawHex string
			if len(req.Params) != 1 || json.Unmarshal(req.Params[0], &rawHex) != nil {
				t.Errorf("invalid send params: %s", req.Params)
				return
			}
			tx := new(types.Transaction)
			if decodeErr := tx.UnmarshalBinary(common.FromHex(rawHex)); decodeErr != nil {
				t.Errorf("decode signed transaction: %v", decodeErr)
				return
			}
			sentTx = tx
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%q}`, tx.Hash().Hex())
		case "eth_getTransactionReceipt":
			receiptCalls++
			if receiptCalls == 1 {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
				return
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"status":"0x1"}}`))
		default:
			t.Errorf("unexpected RPC method %q", req.Method)
		}
	}))
	defer server.Close()

	monitor := &LivepeerDepositMonitor{
		gasWalletPrivKey: testFundingPrivateKey,
		gasWalletAddress: strings.ToLower(from.Hex()),
		rpcEndpoint:      server.URL, receiptTimeout: time.Second, receiptPollInterval: time.Millisecond,
	}
	txHash, err := monitor.sendTicketBrokerFunding(context.Background(), gateway, deposit, reserve)
	if err != nil {
		t.Fatalf("sendTicketBrokerFunding: %v", err)
	}
	if sentTx == nil || txHash != sentTx.Hash().Hex() {
		t.Fatalf("sent transaction/hash mismatch: tx=%v hash=%q", sentTx, txHash)
	}
	if sentTx.To() == nil || !strings.EqualFold(sentTx.To().Hex(), livepeerchain.TicketBrokerAddress) {
		t.Fatalf("transaction destination=%v", sentTx.To())
	}
	if sentTx.Value().Cmp(new(big.Int).Add(deposit, reserve)) != 0 {
		t.Fatalf("transaction value=%s", sentTx.Value())
	}
	if want := fundDepositAndReserveForCallData(gateway, deposit, reserve); !strings.EqualFold(common.Bytes2Hex(sentTx.Data()), common.Bytes2Hex(want)) {
		t.Fatalf("unexpected transaction calldata")
	}
	sender, err := types.Sender(types.NewEIP155Signer(big.NewInt(42161)), sentTx)
	if err != nil || sender != from {
		t.Fatalf("transaction sender=%s err=%v, want %s", sender, err, from)
	}
	if receiptCalls != 2 {
		t.Fatalf("receipt calls=%d, want 2", receiptCalls)
	}
}

func TestWaitForFundingReceiptRejectsRevert(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"status":"0x0"}}`))
	}))
	defer server.Close()
	monitor := &LivepeerDepositMonitor{rpcEndpoint: server.URL, receiptTimeout: time.Second, receiptPollInterval: time.Millisecond}
	if err := monitor.waitForFundingReceipt(context.Background(), "0x"+strings.Repeat("ab", 32)); err == nil || !strings.Contains(err.Error(), "reverted") {
		t.Fatalf("reverted receipt accepted: %v", err)
	}
}

func TestEthToWei_OneETH(t *testing.T) {
	wei := ethToWei(1.0)
	expected := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil) // 1e18
	if wei.Cmp(expected) != 0 {
		t.Fatalf("expected %s, got %s", expected, wei)
	}
}

func TestEthToWei_FractionalETH(t *testing.T) {
	wei := ethToWei(0.2)
	expected := new(big.Int).Mul(big.NewInt(2), new(big.Int).Exp(big.NewInt(10), big.NewInt(17), nil)) // 2e17
	if wei.Cmp(expected) != 0 {
		t.Fatalf("expected %s, got %s", expected, wei)
	}
}

func TestEthToWei_Zero(t *testing.T) {
	wei := ethToWei(0)
	if wei.Cmp(big.NewInt(0)) != 0 {
		t.Fatalf("expected 0, got %s", wei)
	}
}

func TestWeiToETH_Roundtrip(t *testing.T) {
	original := 0.123456
	wei := ethToWei(original)
	result := weiToETH(wei)
	diff := original - result
	if diff < 0 {
		diff = -diff
	}
	if diff > 1e-10 {
		t.Fatalf("roundtrip: expected %f, got %f (diff: %e)", original, result, diff)
	}
}

// fakeLivepeerServiceDiscoveryClient answers pool-assigned discovery like
// Quartermaster: a gateway is only visible under a cluster it is assigned to.
// With byCluster unset, resp is served for the single official cluster
// "media-a".
type fakeLivepeerServiceDiscoveryClient struct {
	resp      *quartermasterpb.ServiceDiscoveryResponse
	err       error
	clusters  []string
	byCluster map[string]*quartermasterpb.ServiceDiscoveryResponse
	errFor    map[string]error
	queried   []string
}

func (f *fakeLivepeerServiceDiscoveryClient) ListOfficialClusters(context.Context) (*quartermasterpb.ListClustersResponse, error) {
	ids := f.clusters
	if len(ids) == 0 {
		ids = []string{"media-a"}
	}
	out := &quartermasterpb.ListClustersResponse{}
	for _, id := range ids {
		out.Clusters = append(out.Clusters, &quartermasterpb.InfrastructureCluster{ClusterId: id})
	}
	return out, nil
}

func (f *fakeLivepeerServiceDiscoveryClient) DiscoverServices(_ context.Context, serviceType, clusterID string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
	f.queried = append(f.queried, clusterID)
	if serviceType != "livepeer-gateway" {
		return nil, fmt.Errorf("unexpected service type %q", serviceType)
	}
	if clusterID == "" {
		return nil, fmt.Errorf("cluster_id required for pool-assigned service discovery")
	}
	if err := f.errFor[clusterID]; err != nil {
		return nil, err
	}
	if f.byCluster != nil {
		if resp := f.byCluster[clusterID]; resp != nil {
			return resp, nil
		}
		return &quartermasterpb.ServiceDiscoveryResponse{}, nil
	}
	return f.resp, f.err
}

// Gateways are assigned to the media clusters, not to Purser's own control
// cluster, so discovery must cover every platform-official cluster.
func TestDiscoverGatewayAddressesCoversEveryOfficialCluster(t *testing.T) {
	walletEU := "0x" + strings.Repeat("ab", 20)
	walletUS := "0x" + strings.Repeat("cd", 20)
	gateway := func(host, wallet string) *quartermasterpb.ServiceDiscoveryResponse {
		return &quartermasterpb.ServiceDiscoveryResponse{Instances: []*quartermasterpb.ServiceInstance{{
			Status: "running", Host: stringPtr(host), Port: int32Ptr(8935),
			Metadata: map[string]string{"wallet_address": wallet},
		}}}
	}
	qm := &fakeLivepeerServiceDiscoveryClient{
		clusters: []string{"staging-core", "staging-media-eu", "staging-media-us", "staging-media-broken"},
		byCluster: map[string]*quartermasterpb.ServiceDiscoveryResponse{
			"staging-media-eu": gateway("10.0.0.1", walletEU),
			"staging-media-us": gateway("10.0.1.1", walletUS),
		},
		errFor: map[string]error{"staging-media-broken": fmt.Errorf("unavailable")},
	}
	monitor := &LivepeerDepositMonitor{logger: logging.NewLogger(), qm: qm}

	gateways := monitor.discoverGatewayAddresses(context.Background())
	got := make([]string, 0, len(gateways))
	for _, gw := range gateways {
		got = append(got, gw.address)
	}
	if strings.Join(got, ",") != walletEU+","+walletUS {
		t.Fatalf("discovered wallets %v, want EU and US gateway wallets (queried clusters %v)", got, qm.queried)
	}
	if strings.Join(qm.queried, ",") != "staging-core,staging-media-eu,staging-media-us,staging-media-broken" {
		t.Fatalf("queried clusters %v, want every official cluster", qm.queried)
	}
}

// Arbitrum reverts calls to an unknown selector, so the monitor only reads a
// sender when it calls TicketBroker.getSenderInfo(address) (0xe1a589da).
func TestQueryGatewayStateReadsTicketBrokerSenderInfo(t *testing.T) {
	gateway := "0x8c9f0152000000000000000000000000000e3e6f"
	milliETH := new(big.Int).Exp(big.NewInt(10), big.NewInt(15), nil)
	word := func(v *big.Int) string { return fmt.Sprintf("%064x", v) }
	senderInfo := "0x" + word(new(big.Int).Mul(milliETH, big.NewInt(50))) + word(big.NewInt(0)) +
		word(new(big.Int).Mul(milliETH, big.NewInt(120))) + word(big.NewInt(0))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
			t.Errorf("decode RPC request: %v", decodeErr)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "eth_getBalance":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0xde0b6b3a7640000"}`))
		case "eth_call":
			var call map[string]string
			if err := json.Unmarshal(req.Params[0], &call); err != nil {
				t.Errorf("decode eth_call: %v", err)
				return
			}
			if !strings.HasPrefix(call["data"], "0xe1a589da") {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"execution reverted"}}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%q}`, senderInfo)
		default:
			t.Errorf("unexpected RPC method %q", req.Method)
		}
	}))
	defer server.Close()

	monitor := &LivepeerDepositMonitor{logger: logging.NewLogger(), rpcEndpoint: server.URL, depositLowThreshold: 0.1, reserveLowThreshold: 0.1}
	state, err := monitor.queryGatewayState(context.Background(), gateway)
	if err != nil {
		t.Fatalf("queryGatewayState: %v", err)
	}
	if state.DepositETH != 0.05 || state.ReserveETH != 0.12 || state.BalanceETH != 1 {
		t.Fatalf("state = %+v, want deposit 0.05 reserve 0.12 balance 1", state)
	}
	if !state.DepositLow || state.ReserveLow {
		t.Fatalf("low flags = deposit %v reserve %v, want deposit low only", state.DepositLow, state.ReserveLow)
	}
}

func TestDiscoverGatewayAddressesUsesMetadataAndDeduplicatesWallets(t *testing.T) {
	walletA := "0x" + strings.Repeat("ab", 20)
	walletB := "0x" + strings.Repeat("cd", 20)
	monitor := &LivepeerDepositMonitor{
		logger: logging.NewLogger(),
		qm: &fakeLivepeerServiceDiscoveryClient{
			resp: &quartermasterpb.ServiceDiscoveryResponse{
				Instances: []*quartermasterpb.ServiceInstance{
					{
						Status:   "running",
						Host:     stringPtr("10.0.0.1"),
						Port:     int32Ptr(8935),
						Metadata: map[string]string{"wallet_address": strings.ToUpper(walletA[:2]) + strings.ToUpper(walletA[2:])},
					},
					{
						Status:   "running",
						Host:     stringPtr("10.0.0.2"),
						Port:     int32Ptr(8935),
						Metadata: map[string]string{"wallet_address": walletA},
					},
					{
						Status:   "running",
						Host:     stringPtr("10.0.0.3"),
						Port:     int32Ptr(8935),
						Metadata: map[string]string{"wallet_address": walletB},
					},
				},
			},
		},
	}

	gateways := monitor.discoverGatewayAddresses(context.Background())
	if len(gateways) != 2 {
		t.Fatalf("expected 2 unique gateway wallets, got %d", len(gateways))
	}
	if gateways[0].address != walletA {
		t.Fatalf("expected normalized wallet address, got %q", gateways[0].address)
	}
	if gateways[1].address != walletB {
		t.Fatalf("expected second wallet address, got %q", gateways[1].address)
	}
}

func TestFundDepositAndReserveForCallData(t *testing.T) {
	gateway := "0x" + strings.Repeat("12", 20)
	deposit := big.NewInt(123)
	reserve := big.NewInt(456)
	data := fundDepositAndReserveForCallData(gateway, deposit, reserve)
	if len(data) != 4+32*3 {
		t.Fatalf("calldata len=%d", len(data))
	}
	if got := fmt.Sprintf("%x", data[:4]); got != "989f789c" {
		t.Fatalf("selector=%s", got)
	}
	if got := new(big.Int).SetBytes(data[36:68]); got.Cmp(deposit) != 0 {
		t.Fatalf("deposit=%s", got)
	}
	if got := new(big.Int).SetBytes(data[68:100]); got.Cmp(reserve) != 0 {
		t.Fatalf("reserve=%s", got)
	}
}

func TestReserveFundingAttemptEnforcesDurableDailyCap(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	monitor := &LivepeerDepositMonitor{db: db, dailyCapWei: big.NewInt(200)}
	mock.ExpectBegin()
	mock.ExpectQuery(`pg_try_advisory_xact_lock\(hashtext\('purser-livepeer-funding-signer'\)\)`).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectExec("pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("tx_hash IS NULL AND error_message IS NULL").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT COALESCE\\(SUM\\(amount_wei\\), 0\\)::text").
		WillReturnRows(sqlmock.NewRows([]string{"spent"}).AddRow("150"))
	mock.ExpectRollback()
	if _, _, err := monitor.reserveFundingAttempt(context.Background(), db, "0x"+strings.Repeat("ab", 20), big.NewInt(40), big.NewInt(20)); err == nil || !strings.Contains(err.Error(), "daily Livepeer funding cap exceeded") {
		t.Fatalf("expected cap rejection, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Another session holds the signer lock, so this session lost it: nothing is
// reserved and the transaction stops before the ledger is read.
func TestReserveFundingAttemptRefusesWithoutTheSignerLock(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	monitor := &LivepeerDepositMonitor{db: db, dailyCapWei: big.NewInt(200)}
	mock.ExpectBegin()
	mock.ExpectQuery(`pg_try_advisory_xact_lock\(hashtext\('purser-livepeer-funding-signer'\)\)`).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(false))
	mock.ExpectRollback()
	if _, _, err := monitor.reserveFundingAttempt(context.Background(), db, "0x"+strings.Repeat("ab", 20), big.NewInt(40), big.NewInt(20)); !errors.Is(err, errFundingSignerLockLost) {
		t.Fatalf("reserve without the signer lock = %v, want errFundingSignerLockLost", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// An attempt without a recorded result may still be unmined: a second one
// would take the next nonce of the same wallet and fund the gateway twice.
func TestReserveFundingAttemptRefusesWhileAnAttemptIsInFlight(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	monitor := &LivepeerDepositMonitor{db: db, dailyCapWei: big.NewInt(200), receiptTimeout: time.Minute}
	mock.ExpectBegin()
	mock.ExpectQuery(`pg_try_advisory_xact_lock`).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectExec("pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("tx_hash IS NULL AND error_message IS NULL").
		WithArgs(float64(180), fundingUnconfirmedPattern).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()
	if _, _, err := monitor.reserveFundingAttempt(context.Background(), db, "0x"+strings.Repeat("ab", 20), big.NewInt(40), big.NewInt(20)); !errors.Is(err, errFundingAttemptInFlight) {
		t.Fatalf("reserve while an attempt is in flight = %v, want errFundingAttemptInFlight", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFundingSignerLockIsSessionScoped(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	monitor := &LivepeerDepositMonitor{db: db, logger: logging.NewLogger()}
	mock.ExpectExec("pg_advisory_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("pg_advisory_unlock").WillReturnRows(sqlmock.NewRows([]string{"pg_advisory_unlock"}).AddRow(true))

	_, unlock, err := monitor.lockFundingSigner(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Another Purser replica funded the gateway while this one waited for the
// signer lock: the reading taken under the lock is healthy, so nothing is
// reserved or sent.
func TestFundTicketBrokerRereadsGatewayUnderSignerLock(t *testing.T) {
	gateway := "0x" + strings.Repeat("34", 20)
	oneETH := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	word := func(v *big.Int) string { return fmt.Sprintf("%064x", v) }
	healthy := "0x" + word(new(big.Int).Mul(oneETH, big.NewInt(5))) + word(big.NewInt(0)) +
		word(new(big.Int).Mul(oneETH, big.NewInt(5))) + word(big.NewInt(0))
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
			t.Errorf("decode RPC request: %v", decodeErr)
			return
		}
		methods = append(methods, req.Method)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "eth_getBalance":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0xde0b6b3a7640000"}`))
		case "eth_call":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%q}`, healthy)
		case "eth_getTransactionCount":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x5"}`))
		default:
			t.Errorf("unexpected RPC method %q: a stale low reading must not fund", req.Method)
		}
	}))
	defer server.Close()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectExec("pg_advisory_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id, tx_hash FROM purser.livepeer_funding_attempts").WillReturnRows(sqlmock.NewRows([]string{"id", "tx_hash"}))
	mock.ExpectQuery("pg_advisory_unlock").WillReturnRows(sqlmock.NewRows([]string{"pg_advisory_unlock"}).AddRow(true))

	monitor := &LivepeerDepositMonitor{
		db: db, logger: logging.NewLogger(), rpcEndpoint: server.URL,
		gasWalletPrivKey: testFundingPrivateKey, topupAmountWei: oneETH, dailyCapWei: big.NewInt(1),
		depositLowThreshold: 1, reserveLowThreshold: 1,
	}
	stale := &GatewayDepositState{Address: gateway, DepositLow: true, ReserveLow: true}
	monitor.fundTicketBroker(context.Background(), stale, gateway)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("funding attempt reserved from a stale reading: %v", err)
	}
	if strings.Join(methods, ",") != "eth_getTransactionCount,eth_getTransactionCount,eth_getBalance,eth_call" {
		t.Fatalf("RPC calls = %v, want only the wallet nonce check and the re-read under the lock", methods)
	}
}

// The funding wallet's pending nonce runs ahead of its latest one: a top-up
// from an earlier cycle is still unmined, so nothing is re-read, reserved or
// sent.
func TestFundTicketBrokerWaitsForThePendingTransaction(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
			t.Errorf("decode RPC request: %v", decodeErr)
			return
		}
		methods = append(methods, req.Method)
		if req.Method != "eth_getTransactionCount" || len(req.Params) != 2 {
			t.Errorf("unexpected RPC call %s %s while a transaction is pending", req.Method, req.Params)
			return
		}
		nonce := "0x7"
		if string(req.Params[1]) == `"pending"` {
			nonce = "0x8"
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%q}`, nonce)
	}))
	defer server.Close()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectExec("pg_advisory_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("pg_advisory_unlock").WillReturnRows(sqlmock.NewRows([]string{"pg_advisory_unlock"}).AddRow(true))

	alerts := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_alerts"}, []string{"reason"})
	monitor := &LivepeerDepositMonitor{
		db: db, logger: logging.NewLogger(), rpcEndpoint: server.URL, alertCounter: alerts,
		gasWalletPrivKey: testFundingPrivateKey, topupAmountWei: big.NewInt(1), dailyCapWei: big.NewInt(10),
	}
	gateway := "0x" + strings.Repeat("34", 20)
	monitor.fundTicketBroker(context.Background(), &GatewayDepositState{Address: gateway, DepositLow: true}, gateway)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("funding went on while the wallet had a pending transaction: %v", err)
	}
	if got := testutil.ToFloat64(alerts.WithLabelValues("pending_transaction")); got != 1 {
		t.Fatalf("pending_transaction alerts = %v, want 1", got)
	}
	if strings.Join(methods, ",") != "eth_getTransactionCount,eth_getTransactionCount" {
		t.Fatalf("RPC calls = %v, want only the wallet nonce check", methods)
	}
}

func TestDiscoverGatewayAddressesSkipsMissingWalletMetadata(t *testing.T) {
	monitor := &LivepeerDepositMonitor{
		logger: logging.NewLogger(),
		qm: &fakeLivepeerServiceDiscoveryClient{
			resp: &quartermasterpb.ServiceDiscoveryResponse{
				Instances: []*quartermasterpb.ServiceInstance{
					{
						Status: "running",
						Host:   stringPtr("10.0.0.1"),
						Port:   int32Ptr(8935),
					},
				},
			},
		},
	}

	gateways := monitor.discoverGatewayAddresses(context.Background())
	if len(gateways) != 0 {
		t.Fatalf("expected no gateways without wallet metadata, got %d", len(gateways))
	}
}

func stringPtr(v string) *string {
	return &v
}

func int32Ptr(v int32) *int32 {
	return &v
}

// The funding signer lock pins one session. When that session cannot drop
// the lock it is closed, so the lock ends with it instead of staying held by
// a pooled connection.
func TestFundingSignerLockDiscardsASessionThatCannotDropTheLock(t *testing.T) {
	db, server := fakesession.New(func(_ int, query string) (bool, error) {
		if strings.Contains(query, "pg_advisory_unlock") {
			return false, fakesession.ExpiredSessionError()
		}
		return true, nil
	})
	defer db.Close()
	monitor := &LivepeerDepositMonitor{db: db, logger: logging.NewLogger()}
	_, unlock, err := monitor.lockFundingSigner(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if !server.Closed(1) {
		t.Fatal("a session whose unlock failed went back to the pool still holding the signer lock")
	}
}

func TestFundingSignerLockDiscardsASessionThatCannotTakeTheLock(t *testing.T) {
	db, server := fakesession.New(func(session int, _ string) (bool, error) {
		if session == 1 {
			return false, fakesession.ExpiredSessionError()
		}
		return true, nil
	})
	defer db.Close()
	monitor := &LivepeerDepositMonitor{db: db, logger: logging.NewLogger()}
	if _, _, err := monitor.lockFundingSigner(context.Background()); err == nil {
		t.Fatal("lockFundingSigner on the expired session succeeded")
	}
	_, unlock, err := monitor.lockFundingSigner(context.Background())
	if err != nil {
		t.Fatalf("lockFundingSigner after the expired session = %v; the pool handed the expired session back", err)
	}
	unlock()
	if server.Opened() != 2 || !server.Closed(1) {
		t.Fatalf("opened=%d closed(1)=%v; want the expired session closed and a second one opened", server.Opened(), server.Closed(1))
	}
}
