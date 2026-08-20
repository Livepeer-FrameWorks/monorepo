//go:build crypto_evm

package grpc

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

	"frameworks/api_billing/internal/handlers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/cryptosweep"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

const sweepLocalEVMPrivateKey = "1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func startSweepAnvil(t *testing.T, chainID int64) string {
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
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		connection, dialErr := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 50*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return fmt.Sprintf("http://127.0.0.1:%d", port)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("anvil did not start: %s", output.String())
	return ""
}

func sweepAnvilCall(t *testing.T, rpc *handlers.RPCClient, network handlers.NetworkConfig, method string, params []any) any {
	t.Helper()
	var result any
	if err := rpc.Call(context.Background(), network, method, params, &result); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return result
}

func TestNativeSweepAgainstLocalEVMNonceAndFinality(t *testing.T) { //nolint:funlen // One chain proves the full signed native-asset broadcast boundary.
	network := handlers.Networks["base"]
	t.Setenv(network.RPCEndpointEnv, startSweepAnvil(t, network.ChainID))
	rpc := handlers.NewRPCClient()
	key, err := ethcrypto.HexToECDSA(sweepLocalEVMPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	source := ethcrypto.PubkeyToAddress(key.PublicKey)
	treasury := common.HexToAddress("0x02A00e8DD29B60607439472dEC769d6c7860AC48")
	sweepAnvilCall(t, rpc, network, "anvil_setBalance", []any{source.Hex(), "0x56bc75e2d63100000"})

	amount := big.NewInt(100_000_000_000_000_000)
	item := cryptosweep.ManifestItem{
		Asset: "ETH", SourceAddress: source.Hex(), DestinationAddress: treasury.Hex(),
		AmountBaseUnits: amount.String(), SourceNonce: 0,
	}
	server := &PurserServer{rpcClient: rpc, logger: logging.NewLogger()}
	if err := server.recheckSweepItemBeforeBroadcast(context.Background(), network, item); err != nil {
		t.Fatalf("fresh signed sweep preflight: %v", err)
	}

	var gasPriceHex, tipHex string
	if err := rpc.Call(context.Background(), network, "eth_gasPrice", []any{}, &gasPriceHex); err != nil {
		t.Fatal(err)
	}
	if err := rpc.Call(context.Background(), network, "eth_maxPriorityFeePerGas", []any{}, &tipHex); err != nil {
		t.Fatal(err)
	}
	gasPrice, err := parseSweepHex(gasPriceHex)
	if err != nil {
		t.Fatal(err)
	}
	tip, err := parseSweepHex(tipHex)
	if err != nil {
		t.Fatal(err)
	}
	transaction := types.NewTx(&types.DynamicFeeTx{
		ChainID: big.NewInt(network.ChainID), Nonce: item.SourceNonce, GasTipCap: tip,
		GasFeeCap: new(big.Int).Add(new(big.Int).Mul(gasPrice, big.NewInt(2)), tip),
		Gas:       21000, To: &treasury, Value: amount,
	})
	signed, err := types.SignTx(transaction, types.LatestSignerForChainID(big.NewInt(network.ChainID)), key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := signed.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var txHash string
	if err := rpc.Call(context.Background(), network, "eth_sendRawTransaction", []any{"0x" + hex.EncodeToString(raw)}, &txHash); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(txHash, signed.Hash().Hex()) {
		t.Fatalf("broadcast hash=%s want=%s", txHash, signed.Hash().Hex())
	}
	sweepAnvilCall(t, rpc, network, "evm_mine", []any{})
	sweepAnvilCall(t, rpc, network, "evm_mine", []any{})

	deadline := time.Now().Add(5 * time.Second)
	for {
		receipt, canonical, receiptErr := server.canonicalSweepReceipt(context.Background(), network, txHash)
		if receiptErr == nil && receipt != nil && canonical {
			if receipt.Status != "0x1" {
				t.Fatalf("sweep reverted: %+v", receipt)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sweep did not reach canonical finality: receipt=%+v canonical=%v err=%v", receipt, canonical, receiptErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := server.recheckSweepItemBeforeBroadcast(context.Background(), network, item); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("stale signed sweep was not fenced after nonce advanced: %v", err)
	}
	var treasuryBalanceHex string
	if err := rpc.Call(context.Background(), network, "eth_getBalance", []any{treasury.Hex(), "latest"}, &treasuryBalanceHex); err != nil {
		t.Fatal(err)
	}
	treasuryBalance, err := parseSweepHex(treasuryBalanceHex)
	if err != nil || treasuryBalance.Cmp(amount) != 0 {
		t.Fatalf("treasury balance=%v err=%v want=%s", treasuryBalance, err, amount)
	}
}
