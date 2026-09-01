package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	livepeerchain "github.com/Livepeer-FrameWorks/monorepo/pkg/livepeer/chain"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

const testFundingPrivateKey = "0000000000000000000000000000000000000000000000000000000000000001"

func TestNewLivepeerDepositMonitorValidatesFundingIdentity(t *testing.T) {
	t.Setenv("ARBITRUM_RPC_ENDPOINT", "https://arb.example")
	t.Setenv("X402_GAS_WALLET_PRIVKEY", testFundingPrivateKey)
	key, err := crypto.HexToECDSA(testFundingPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	address := crypto.PubkeyToAddress(key.PublicKey).Hex()
	t.Setenv("X402_GAS_WALLET_ADDRESS", address)

	monitor, err := NewLivepeerDepositMonitor(logging.NewLogger(), nil, nil)
	if err != nil {
		t.Fatalf("valid funding identity rejected: %v", err)
	}
	if !strings.EqualFold(monitor.gasWalletAddress, address) {
		t.Fatalf("funding address=%q, want %q", monitor.gasWalletAddress, address)
	}

	t.Setenv("X402_GAS_WALLET_ADDRESS", "0x1111111111111111111111111111111111111111")
	if _, err := NewLivepeerDepositMonitor(logging.NewLogger(), nil, nil); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched funding identity accepted: %v", err)
	}

	t.Setenv("X402_GAS_WALLET_PRIVKEY", "")
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

type fakeLivepeerServiceDiscoveryClient struct {
	resp *quartermasterpb.ServiceDiscoveryResponse
	err  error
}

func (f *fakeLivepeerServiceDiscoveryClient) DiscoverServices(_ context.Context, _, _ string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
	return f.resp, f.err
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
	mock.ExpectExec("pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT COALESCE\\(SUM\\(amount_wei\\), 0\\)::text").
		WillReturnRows(sqlmock.NewRows([]string{"spent"}).AddRow("150"))
	mock.ExpectRollback()
	if _, _, err := monitor.reserveFundingAttempt(context.Background(), "0x"+strings.Repeat("ab", 20), big.NewInt(40), big.NewInt(20)); err == nil || !strings.Contains(err.Error(), "daily Livepeer funding cap exceeded") {
		t.Fatalf("expected cap rejection, got %v", err)
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
	mock.ExpectExec("pg_advisory_unlock").WillReturnResult(sqlmock.NewResult(0, 1))

	unlock, err := monitor.lockFundingSigner(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
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
