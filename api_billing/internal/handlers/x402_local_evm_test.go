//go:build crypto_evm

package handlers

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

// Compiled with solc --optimize --bin-runtime from testdata/MockEIP3009.sol.
const mockEIP3009Runtime = "608060405234801561000f575f5ffd5b5060043610610029575f3560e01c8063e3ee160e1461002d575b5f5ffd5b61004061003b3660046100b5565b610042565b005b876001600160a01b0316896001600160a01b03167fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef8960405161008791815260200190565b60405180910390a3505050505050505050565b80356001600160a01b03811681146100b0575f5ffd5b919050565b5f5f5f5f5f5f5f5f5f6101208a8c0312156100ce575f5ffd5b6100d78a61009a565b98506100e560208b0161009a565b975060408a0135965060608a0135955060808a0135945060a08a0135935060c08a013560ff81168114610116575f5ffd5b989b979a50959894979396929550929360e08101359350610100013591905056fea26469706673582212205bb1553ae956648734ac4ce82d3e1f4926d05d1cb8e49786ff77081221fb36e964736f6c63430008240033"

func startX402Anvil(t *testing.T, chainID int64) string {
	t.Helper()
	if _, err := exec.LookPath("anvil"); err != nil {
		t.Fatal("anvil is required for the crypto_evm verification target")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	var output bytes.Buffer
	command := exec.Command("anvil", "--silent", "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--chain-id", strconv.FormatInt(chainID, 10), "--slots-in-an-epoch", "1") //nolint:gosec // Fixed test binary and validated numeric arguments.
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		connection, dialErr := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 50*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return endpoint
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("anvil did not start: %s", output.String())
	return ""
}

func setAnvilState(t *testing.T, rpc *RPCClient, network NetworkConfig, method string, params []any) any {
	t.Helper()
	var result any
	if err := rpc.Call(context.Background(), network, method, params, &result); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return result
}

func TestEmbeddedFacilitatorAgainstLocalEVMFaults(t *testing.T) { //nolint:funlen // One chain instance proves broadcast, finality, reorg, and revert behavior.
	network := Networks["base"]
	t.Setenv(network.RPCEndpointEnv, startX402Anvil(t, network.ChainID))
	rpc := NewRPCClient()
	setAnvilState(t, rpc, network, "anvil_setCode", []any{network.USDCContract, "0x" + mockEIP3009Runtime})

	key, err := crypto.HexToECDSA(durableBroadcastTestPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	gasAddress := crypto.PubkeyToAddress(key.PublicKey).Hex()
	setAnvilState(t, rpc, network, "anvil_setBalance", []any{gasAddress, "0x56bc75e2d63100000"})

	payload := durableBroadcastTestPayload()
	payload.Payload.Authorization.Value = "5000000"
	payload.Payload.Authorization.ValidBefore = strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	callData, err := transferWithAuthorizationCallData(payload)
	if err != nil {
		t.Fatal(err)
	}
	handler := &X402Handler{rpc: rpc, gasWalletPrivKey: durableBroadcastTestPrivateKey, gasWalletAddress: gasAddress}
	if err := handler.simulateTransfer(context.Background(), network, callData); err != nil {
		t.Fatalf("valid local EVM simulation: %v", err)
	}
	snapshot := setAnvilState(t, rpc, network, "evm_snapshot", []any{}).(string)

	nonce, err := handler.getNonce(context.Background(), network, gasAddress)
	if err != nil {
		t.Fatal(err)
	}
	gasPrice, err := handler.getGasPrice(context.Background(), network)
	if err != nil {
		t.Fatal(err)
	}
	tip, err := handler.getPriorityFee(context.Background(), network)
	if err != nil {
		t.Fatal(err)
	}
	maxFee := new(big.Int).Add(new(big.Int).Mul(gasPrice, big.NewInt(2)), tip)
	raw, err := handler.signDynamicFeeTransaction(nonce, network.USDCContract, big.NewInt(0), 150000, maxFee, tip, callData, big.NewInt(network.ChainID))
	if err != nil {
		t.Fatal(err)
	}
	wantHash := crypto.Keccak256Hash(raw).Hex()
	var txHash string
	if err := rpc.Call(context.Background(), network, "eth_sendRawTransaction", []any{"0x" + hex.EncodeToString(raw)}, &txHash); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(txHash, wantHash) {
		t.Fatalf("transaction hash=%s want=%s", txHash, wantHash)
	}
	setAnvilState(t, rpc, network, "evm_mine", []any{})
	setAnvilState(t, rpc, network, "evm_mine", []any{})
	confirmationCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	receipt, err := handler.waitForSettlementConfirmation(confirmationCtx, network, txHash, payload.Payload.Authorization)
	if err != nil || receipt == nil {
		t.Fatalf("confirmed local EVM transfer receipt=%+v err=%v", receipt, err)
	}

	setAnvilState(t, rpc, network, "evm_revert", []any{snapshot})
	setAnvilState(t, rpc, network, "evm_mine", []any{})
	var removedReceipt *TransactionReceipt
	if err := rpc.Call(context.Background(), network, "eth_getTransactionReceipt", []any{txHash}, &removedReceipt); err != nil {
		t.Fatal(err)
	}
	if removedReceipt != nil {
		t.Fatalf("receipt survived deterministic chain reorg: %+v", removedReceipt)
	}

	setAnvilState(t, rpc, network, "anvil_setCode", []any{network.USDCContract, "0x60006000fd"})
	if err := handler.simulateTransfer(context.Background(), network, callData); err == nil {
		t.Fatal("reverting transfer unexpectedly passed pre-broadcast simulation")
	}
}
